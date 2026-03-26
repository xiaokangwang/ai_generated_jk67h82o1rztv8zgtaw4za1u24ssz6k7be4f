package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	maxDatagramSize         = 65535
	associationPollInterval = 5 * time.Second
)

type proxy struct {
	listener     *net.UDPConn
	idleTimeout  time.Duration
	splitByDest  bool
	maxOpenSockets int
	filterIncoming bool
	incomingAllowPeriod time.Duration
	maxAllowedDestinations int
	logger       *log.Logger
	mu           sync.Mutex
	associations map[string]*association
}

type association struct {
	key        string
	clientAddr *net.UDPAddr
	upstream   upstreamSocket
	destinations map[string]*destinationAssociation
	recentDestinations map[string]time.Time

	mu        sync.Mutex
	lastSeen  time.Time
	closeOnce sync.Once
}

type destinationAssociation struct {
	targetAddr *net.UDPAddr
	upstream   upstreamSocket

	mu        sync.Mutex
	lastSeen  time.Time
	closeOnce sync.Once
}

type upstreamSocket interface {
	readFromUDP([]byte) (int, *net.UDPAddr, error)
	writeToUDP([]byte, *net.UDPAddr) error
	setReadDeadline(time.Time) error
	close() error
}

type dialedUpstream struct {
	conn       *net.UDPConn
	targetAddr *net.UDPAddr
}

type boundUpstream struct {
	conn *net.UDPConn
}

func main() {
	listenAddr := flag.String("listen", ":1080", "UDP address to listen on for SOCKS5 UDP packets")
	idleTimeout := flag.Duration("idle-timeout", 2*time.Minute, "close idle client/target associations after this duration")
	splitDestinations := flag.Bool("split-destinations", true, "create separate upstream UDP sockets per destination using dialed UDP connections")
	maxOpenSockets := flag.Int("max-open-sockets", 0, "maximum open destination sockets per client when split-destinations=true; 0 means unlimited")
	filterIncoming := flag.Bool("incoming-filter", false, "when split-destinations=false, only allow inbound UDP from destinations the client sent to recently")
	incomingAllowPeriod := flag.Duration("incoming-allow-period", 30*time.Second, "how long a destination stays allowed for inbound UDP after the client sends to it")
	maxAllowedDestinations := flag.Int("max-allowed-destinations", 0, "maximum recent allowed destinations per client when incoming-filter=true; 0 means unlimited")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)

	p, err := newProxyWithSettings(*listenAddr, *idleTimeout, *splitDestinations, *maxOpenSockets, *filterIncoming, *incomingAllowPeriod, *maxAllowedDestinations, logger)
	if err != nil {
		logger.Fatalf("failed to start proxy: %v", err)
	}

	logger.Printf(
		"listening for direct SOCKS5 UDP packets on %s (split-destinations=%t max-open-sockets=%d incoming-filter=%t incoming-allow-period=%s max-allowed-destinations=%d)",
		p.listener.LocalAddr(),
		p.splitByDest,
		p.maxOpenSockets,
		p.filterIncoming,
		p.incomingAllowPeriod,
		p.maxAllowedDestinations,
	)

	if err := p.serve(); err != nil {
		logger.Fatalf("proxy stopped: %v", err)
	}
}

func newProxy(listenAddr string, idleTimeout time.Duration, logger *log.Logger) (*proxy, error) {
	return newProxyWithSettings(listenAddr, idleTimeout, true, 0, false, 30*time.Second, 0, logger)
}

func newProxyWithOptions(listenAddr string, idleTimeout time.Duration, splitByDest bool, logger *log.Logger) (*proxy, error) {
	return newProxyWithSettings(listenAddr, idleTimeout, splitByDest, 0, false, 30*time.Second, 0, logger)
}

func newProxyWithSettings(listenAddr string, idleTimeout time.Duration, splitByDest bool, maxOpenSockets int, filterIncoming bool, incomingAllowPeriod time.Duration, maxAllowedDestinations int, logger *log.Logger) (*proxy, error) {
	if maxOpenSockets < 0 {
		return nil, errors.New("max open sockets must be non-negative")
	}
	if maxAllowedDestinations < 0 {
		return nil, errors.New("max allowed destinations must be non-negative")
	}
	if incomingAllowPeriod <= 0 {
		return nil, errors.New("incoming allow period must be positive")
	}

	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve listen address: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	return &proxy{
		listener:     conn,
		idleTimeout:  idleTimeout,
		splitByDest:  splitByDest,
		maxOpenSockets: maxOpenSockets,
		filterIncoming: filterIncoming,
		incomingAllowPeriod: incomingAllowPeriod,
		maxAllowedDestinations: maxAllowedDestinations,
		logger:       logger,
		associations: make(map[string]*association),
	}, nil
}

func (p *proxy) serve() error {
	buffer := make([]byte, maxDatagramSize)

	for {
		n, clientAddr, err := p.listener.ReadFromUDP(buffer)
		if err != nil {
			return fmt.Errorf("read client packet: %w", err)
		}

		packet := append([]byte(nil), buffer[:n]...)
		go p.handleClientPacket(cloneUDPAddr(clientAddr), packet)
	}
}

func (p *proxy) handleClientPacket(clientAddr *net.UDPAddr, packet []byte) {
	targetAddr, payload, err := parseSocks5UDPRequest(packet)
	if err != nil {
		p.logger.Printf("dropping packet from %s: %v", clientAddr, err)
		return
	}

	assoc, err := p.getAssociation(clientAddr)
	if err != nil {
		p.logger.Printf("association error client=%s target=%s: %v", clientAddr, targetAddr, err)
		return
	}

	assoc.touch()

	if p.splitByDest {
		destination, err := p.getDestinationAssociation(assoc, targetAddr)
		if err != nil {
			p.logger.Printf("destination association error client=%s target=%s: %v", clientAddr, targetAddr, err)
			return
		}

		destination.touch()

		if err := destination.upstream.writeToUDP(payload, targetAddr); err != nil {
			p.logger.Printf("upstream write failed client=%s target=%s: %v", clientAddr, targetAddr, err)
			p.removeDestinationAssociation(assoc, p.destinationKey(targetAddr), destination)
		}
		return
	}

	if err := assoc.upstream.writeToUDP(payload, targetAddr); err != nil {
		p.logger.Printf("upstream write failed client=%s target=%s: %v", clientAddr, targetAddr, err)
		p.removeAssociation(assoc.key, assoc)
		return
	}

	if p.filterIncoming {
		assoc.noteRecentDestination(targetAddr, p.maxAllowedDestinations)
	}
}

func (p *proxy) getAssociation(clientAddr *net.UDPAddr) (*association, error) {
	key := p.associationKey(clientAddr)

	p.mu.Lock()
	if assoc, ok := p.associations[key]; ok {
		p.mu.Unlock()
		return assoc, nil
	}
	p.mu.Unlock()

	assoc := &association{
		key:        key,
		clientAddr: cloneUDPAddr(clientAddr),
		lastSeen:   time.Now(),
	}

	if p.splitByDest {
		assoc.destinations = make(map[string]*destinationAssociation)
	} else {
		upstream, _, err := p.newUpstreamSocket(nil)
		if err != nil {
			return nil, err
		}
		assoc.upstream = upstream
	}

	p.mu.Lock()
	if existing, ok := p.associations[key]; ok {
		p.mu.Unlock()
		_ = assoc.close()
		return existing, nil
	}
	p.associations[key] = assoc
	p.mu.Unlock()

	if !p.splitByDest {
		go p.relayResponses(assoc, "", nil)
	}

	return assoc, nil
}

func (p *proxy) getDestinationAssociation(assoc *association, targetAddr *net.UDPAddr) (*destinationAssociation, error) {
	key := p.destinationKey(targetAddr)

	assoc.mu.Lock()
	if destination, ok := assoc.destinations[key]; ok {
		assoc.mu.Unlock()
		return destination, nil
	}

	if p.maxOpenSockets > 0 && len(assoc.destinations) >= p.maxOpenSockets {
		evictKey, evictDestination := leastRecentlyUsedDestination(assoc.destinations)
		if evictDestination != nil {
			delete(assoc.destinations, evictKey)
			_ = evictDestination.close()
		}
	}

	upstream, mappedTarget, err := p.newUpstreamSocket(targetAddr)
	if err != nil {
		assoc.mu.Unlock()
		return nil, err
	}

	destination := &destinationAssociation{
		targetAddr: mappedTarget,
		upstream:   upstream,
		lastSeen:   time.Now(),
	}

	if existing, ok := assoc.destinations[key]; ok {
		assoc.mu.Unlock()
		_ = destination.close()
		return existing, nil
	}
	assoc.destinations[key] = destination
	assoc.mu.Unlock()

	go p.relayResponses(assoc, key, destination)

	return destination, nil
}

func (p *proxy) relayResponses(assoc *association, destinationKey string, destination *destinationAssociation) {
	buffer := make([]byte, maxDatagramSize)
	upstream := assoc.upstream
	logTarget := "*"

	if destination != nil {
		upstream = destination.upstream
		logTarget = destination.targetAddr.String()
	}

	for {
		_ = upstream.setReadDeadline(time.Now().Add(associationPollInterval))

		n, from, err := upstream.readFromUDP(buffer)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if destination != nil {
					if destination.idleFor() >= p.idleTimeout {
						p.removeDestinationAssociation(assoc, destinationKey, destination)
						return
					}
				} else if assoc.idleFor() >= p.idleTimeout {
					p.removeAssociation(assoc.key, assoc)
					return
				}
				continue
			}

			if !errors.Is(err, net.ErrClosed) {
				p.logger.Printf("upstream read failed client=%s target=%s: %v", assoc.clientAddr, logTarget, err)
			}
			if destination != nil {
				p.removeDestinationAssociation(assoc, destinationKey, destination)
			} else {
				p.removeAssociation(assoc.key, assoc)
			}
			return
		}

		if destination == nil && p.filterIncoming && !assoc.allowsSource(from, p.incomingAllowPeriod) {
			p.logger.Printf("dropping inbound packet from %s for client=%s: not in recent destination allow list", from, assoc.clientAddr)
			continue
		}

		assoc.touch()
		if destination != nil {
			destination.touch()
		}

		response, err := buildSocks5UDPDatagram(from, buffer[:n])
		if err != nil {
			p.logger.Printf("failed to encode response from %s: %v", from, err)
			continue
		}

		if _, err := p.listener.WriteToUDP(response, assoc.clientAddr); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				p.logger.Printf("client write failed client=%s target=%s: %v", assoc.clientAddr, logTarget, err)
			}
			if destination != nil {
				p.removeDestinationAssociation(assoc, destinationKey, destination)
			} else {
				p.removeAssociation(assoc.key, assoc)
			}
			return
		}
	}
}

func (p *proxy) removeAssociation(key string, assoc *association) {
	p.mu.Lock()
	current, ok := p.associations[key]
	if ok && current == assoc {
		delete(p.associations, key)
	}
	p.mu.Unlock()

	_ = assoc.close()
}

func (p *proxy) removeDestinationAssociation(assoc *association, destinationKey string, destination *destinationAssociation) {
	assoc.mu.Lock()
	current, ok := assoc.destinations[destinationKey]
	if ok && current == destination {
		delete(assoc.destinations, destinationKey)
	}
	remaining := len(assoc.destinations)
	assoc.mu.Unlock()

	_ = destination.close()

	if remaining == 0 {
		p.removeAssociation(assoc.key, assoc)
	}
}

func (a *association) touch() {
	a.mu.Lock()
	a.lastSeen = time.Now()
	a.mu.Unlock()
}

func (a *association) idleFor() time.Duration {
	a.mu.Lock()
	lastSeen := a.lastSeen
	a.mu.Unlock()
	return time.Since(lastSeen)
}

func (a *association) close() error {
	var err error
	a.closeOnce.Do(func() {
		if a.upstream != nil {
			err = a.upstream.close()
		}

		a.mu.Lock()
		destinations := make([]*destinationAssociation, 0, len(a.destinations))
		for _, destination := range a.destinations {
			destinations = append(destinations, destination)
		}
		a.mu.Unlock()

		for _, destination := range destinations {
			if closeErr := destination.close(); err == nil {
				err = closeErr
			}
		}
	})
	return err
}

func (a *association) noteRecentDestination(targetAddr *net.UDPAddr, maxAllowedDestinations int) {
	if targetAddr == nil {
		return
	}

	key := targetAddr.String()

	a.mu.Lock()
	if a.recentDestinations == nil {
		a.recentDestinations = make(map[string]time.Time)
	}
	if _, ok := a.recentDestinations[key]; !ok && maxAllowedDestinations > 0 && len(a.recentDestinations) >= maxAllowedDestinations {
		oldestKey, _ := leastRecentlyUsedRecentDestination(a.recentDestinations)
		if oldestKey != "" {
			delete(a.recentDestinations, oldestKey)
		}
	}
	a.recentDestinations[key] = time.Now()
	a.mu.Unlock()
}

func (a *association) allowsSource(source *net.UDPAddr, allowPeriod time.Duration) bool {
	if source == nil {
		return false
	}

	cutoff := time.Now().Add(-allowPeriod)
	key := source.String()

	a.mu.Lock()
	defer a.mu.Unlock()

	for destination, lastSeen := range a.recentDestinations {
		if lastSeen.Before(cutoff) {
			delete(a.recentDestinations, destination)
		}
	}

	lastSeen, ok := a.recentDestinations[key]
	return ok && !lastSeen.Before(cutoff)
}

func (d *destinationAssociation) touch() {
	d.mu.Lock()
	d.lastSeen = time.Now()
	d.mu.Unlock()
}

func (d *destinationAssociation) idleFor() time.Duration {
	d.mu.Lock()
	lastSeen := d.lastSeen
	d.mu.Unlock()
	return time.Since(lastSeen)
}

func (d *destinationAssociation) close() error {
	var err error
	d.closeOnce.Do(func() {
		err = d.upstream.close()
	})
	return err
}

func (d *destinationAssociation) lastSeenAt() time.Time {
	d.mu.Lock()
	lastSeen := d.lastSeen
	d.mu.Unlock()
	return lastSeen
}

func parseSocks5UDPRequest(packet []byte) (*net.UDPAddr, []byte, error) {
	if len(packet) < 4 {
		return nil, nil, errors.New("packet too short")
	}

	if packet[0] != 0x00 || packet[1] != 0x00 {
		return nil, nil, errors.New("invalid reserved bytes")
	}

	if packet[2] != 0x00 {
		return nil, nil, errors.New("fragmented UDP packets are not supported")
	}

	index := 4
	var host string

	switch packet[3] {
	case 0x01:
		if len(packet) < index+4+2 {
			return nil, nil, errors.New("truncated IPv4 request")
		}
		host = net.IP(packet[index : index+4]).String()
		index += 4
	case 0x03:
		return nil, nil, errors.New("domain name requests are not supported")
	case 0x04:
		if len(packet) < index+16+2 {
			return nil, nil, errors.New("truncated IPv6 request")
		}
		host = net.IP(packet[index : index+16]).String()
		index += 16
	default:
		return nil, nil, fmt.Errorf("unsupported address type 0x%02x", packet[3])
	}

	port := int(binary.BigEndian.Uint16(packet[index : index+2]))
	index += 2

	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve target: %w", err)
	}

	return addr, packet[index:], nil
}

func buildSocks5UDPDatagram(addr *net.UDPAddr, payload []byte) ([]byte, error) {
	if addr == nil {
		return nil, errors.New("missing source address")
	}

	var atyp byte
	var rawAddr []byte

	if ip4 := addr.IP.To4(); ip4 != nil {
		atyp = 0x01
		rawAddr = ip4
	} else if ip16 := addr.IP.To16(); ip16 != nil {
		atyp = 0x04
		rawAddr = ip16
	} else {
		return nil, fmt.Errorf("unsupported response IP %q", addr.IP.String())
	}

	packet := make([]byte, 4+len(rawAddr)+2+len(payload))
	packet[3] = atyp
	copy(packet[4:], rawAddr)
	binary.BigEndian.PutUint16(packet[4+len(rawAddr):], uint16(addr.Port))
	copy(packet[4+len(rawAddr)+2:], payload)

	return packet, nil
}

func (p *proxy) associationKey(clientAddr *net.UDPAddr) string {
	return clientAddr.String()
}

func (p *proxy) destinationKey(targetAddr *net.UDPAddr) string {
	return targetAddr.String()
}

func leastRecentlyUsedDestination(destinations map[string]*destinationAssociation) (string, *destinationAssociation) {
	var (
		oldestKey string
		oldest    *destinationAssociation
		oldestAt  time.Time
	)

	for key, destination := range destinations {
		lastSeen := destination.lastSeenAt()
		if oldest == nil || lastSeen.Before(oldestAt) {
			oldestKey = key
			oldest = destination
			oldestAt = lastSeen
		}
	}

	return oldestKey, oldest
}

func leastRecentlyUsedRecentDestination(destinations map[string]time.Time) (string, time.Time) {
	var (
		oldestKey string
		oldestAt  time.Time
	)

	for key, lastSeen := range destinations {
		if oldestKey == "" || lastSeen.Before(oldestAt) {
			oldestKey = key
			oldestAt = lastSeen
		}
	}

	return oldestKey, oldestAt
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}

	ip := make(net.IP, len(addr.IP))
	copy(ip, addr.IP)

	return &net.UDPAddr{
		IP:   ip,
		Port: addr.Port,
		Zone: addr.Zone,
	}
}

func (p *proxy) newUpstreamSocket(targetAddr *net.UDPAddr) (upstreamSocket, *net.UDPAddr, error) {
	if p.splitByDest {
		if targetAddr == nil {
			return nil, nil, errors.New("missing target address for split destination mode")
		}

		conn, err := net.DialUDP("udp", nil, targetAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("dial target: %w", err)
		}

		return &dialedUpstream{
			conn:       conn,
			targetAddr: cloneUDPAddr(targetAddr),
		}, cloneUDPAddr(targetAddr), nil
	}

	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("listen upstream udp: %w", err)
	}

	return &boundUpstream{conn: conn}, nil, nil
}

func (u *dialedUpstream) readFromUDP(buffer []byte) (int, *net.UDPAddr, error) {
	n, addr, err := u.conn.ReadFromUDP(buffer)
	if addr == nil {
		addr = cloneUDPAddr(u.targetAddr)
	}
	return n, addr, err
}

func (u *dialedUpstream) writeToUDP(payload []byte, _ *net.UDPAddr) error {
	_, err := u.conn.Write(payload)
	return err
}

func (u *dialedUpstream) setReadDeadline(deadline time.Time) error {
	return u.conn.SetReadDeadline(deadline)
}

func (u *dialedUpstream) close() error {
	return u.conn.Close()
}

func (u *boundUpstream) readFromUDP(buffer []byte) (int, *net.UDPAddr, error) {
	return u.conn.ReadFromUDP(buffer)
}

func (u *boundUpstream) writeToUDP(payload []byte, targetAddr *net.UDPAddr) error {
	if targetAddr == nil {
		return errors.New("missing target address")
	}

	_, err := u.conn.WriteToUDP(payload, targetAddr)
	return err
}

func (u *boundUpstream) setReadDeadline(deadline time.Time) error {
	return u.conn.SetReadDeadline(deadline)
}

func (u *boundUpstream) close() error {
	return u.conn.Close()
}
