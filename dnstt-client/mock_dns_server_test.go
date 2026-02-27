package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
	"www.bamsoftware.com/git/dnstt.git/dns"
	"www.bamsoftware.com/git/dnstt.git/noise"
	"www.bamsoftware.com/git/dnstt.git/turbotunnel"
)

// mockMaxUDPPayload mirrors the server's maxUDPPayload constant.
const mockMaxUDPPayload = 1232

// mockResponseTTL mirrors the server's responseTTL constant.
const mockResponseTTL = 60

// mockMaxResponseDelay mirrors the server's maxResponseDelay constant.
const mockMaxResponseDelay = 1 * time.Second

// mockIdleTimeout mirrors the server's idleTimeout constant.
const mockIdleTimeout = 2 * time.Minute

// ---------- Behavior types ----------

type mockAction int

const (
	actionRespond mockAction = iota
	actionDrop
	actionDelay
	actionCorrupt
)

type mockBehaviorResult struct {
	action mockAction
	delay  time.Duration
}

// Behavior factory functions. Each returns a function that takes the request
// number (1-based) for this listener and returns a mockBehaviorResult.
func healthyBehavior() func(int) mockBehaviorResult {
	return func(_ int) mockBehaviorResult { return mockBehaviorResult{action: actionRespond} }
}

func dropBehavior() func(int) mockBehaviorResult {
	return func(_ int) mockBehaviorResult { return mockBehaviorResult{action: actionDrop} }
}

func slowBehavior(d time.Duration) func(int) mockBehaviorResult {
	return func(_ int) mockBehaviorResult { return mockBehaviorResult{action: actionDelay, delay: d} }
}

func corruptBehavior() func(int) mockBehaviorResult {
	return func(_ int) mockBehaviorResult { return mockBehaviorResult{action: actionCorrupt} }
}

func rateLimitBehavior(n int) func(int) mockBehaviorResult {
	return func(reqNum int) mockBehaviorResult {
		if reqNum <= n {
			return mockBehaviorResult{action: actionRespond}
		}
		return mockBehaviorResult{action: actionDrop}
	}
}

func recoverBehavior(n int) func(int) mockBehaviorResult {
	return func(reqNum int) mockBehaviorResult {
		if reqNum <= n {
			return mockBehaviorResult{action: actionDrop}
		}
		return mockBehaviorResult{action: actionRespond}
	}
}

// ---------- mockRecord ----------

type mockRecord struct {
	Resp     *dns.Message
	Addr     net.Addr
	ClientID turbotunnel.ClientID
	ConnIdx  int
	Corrupt  bool
}

// ---------- Replicated server functions ----------

// mockResponseFor is replicated from dnstt-server/main.go responseFor.
func mockResponseFor(query *dns.Message, domain dns.Name) (*dns.Message, []byte) {
	resp := &dns.Message{
		ID:       query.ID,
		Flags:    0x8000,
		Question: query.Question,
	}

	if query.Flags&0x8000 != 0 {
		return nil, nil
	}

	payloadSize := 0
	for _, rr := range query.Additional {
		if rr.Type != dns.RRTypeOPT {
			continue
		}
		if len(resp.Additional) != 0 {
			resp.Flags |= dns.RcodeFormatError
			return resp, nil
		}
		resp.Additional = append(resp.Additional, dns.RR{
			Name:  dns.Name{},
			Type:  dns.RRTypeOPT,
			Class: 4096,
			TTL:   0,
			Data:  []byte{},
		})
		additional := &resp.Additional[0]

		version := (rr.TTL >> 16) & 0xff
		if version != 0 {
			resp.Flags |= dns.ExtendedRcodeBadVers & 0xf
			additional.TTL = (dns.ExtendedRcodeBadVers >> 4) << 24
			return resp, nil
		}

		payloadSize = int(rr.Class)
	}
	if payloadSize < 512 {
		payloadSize = 512
	}

	if len(query.Question) != 1 {
		resp.Flags |= dns.RcodeFormatError
		return resp, nil
	}
	question := query.Question[0]
	prefix, ok := question.Name.TrimSuffix(domain)
	if !ok {
		resp.Flags |= dns.RcodeNameError
		return resp, nil
	}
	resp.Flags |= 0x0400 // AA = 1

	if query.Opcode() != 0 {
		resp.Flags |= dns.RcodeNotImplemented
		return resp, nil
	}

	if question.Type != dns.RRTypeTXT {
		resp.Flags |= dns.RcodeNameError
		return resp, nil
	}

	encoded := bytes.ToUpper(bytes.Join(prefix, nil))
	payload := make([]byte, base32Encoding.DecodedLen(len(encoded)))
	n, err := base32Encoding.Decode(payload, encoded)
	if err != nil {
		resp.Flags |= dns.RcodeNameError
		return resp, nil
	}
	payload = payload[:n]

	if payloadSize < mockMaxUDPPayload {
		resp.Flags |= dns.RcodeFormatError
		return resp, nil
	}

	return resp, payload
}

// mockNextPacket is replicated from dnstt-server/main.go nextPacket.
func mockNextPacket(r *bytes.Reader) ([]byte, error) {
	eof := func(err error) error {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}

	for {
		prefix, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if prefix >= 224 {
			paddingLen := prefix - 224
			_, err := io.CopyN(ioutil.Discard, r, int64(paddingLen))
			if err != nil {
				return nil, eof(err)
			}
		} else {
			p := make([]byte, int(prefix))
			_, err = io.ReadFull(r, p)
			return p, eof(err)
		}
	}
}

// mockComputeMaxEncodedPayload is replicated from dnstt-server/main.go computeMaxEncodedPayload.
func mockComputeMaxEncodedPayload(limit int) int {
	maxLengthName, err := dns.NewName([][]byte{
		[]byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		[]byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		[]byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		[]byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
	})
	if err != nil {
		panic(err)
	}

	queryLimit := uint16(limit)
	if int(queryLimit) != limit {
		queryLimit = 0xffff
	}
	query := &dns.Message{
		Question: []dns.Question{
			{
				Name:  maxLengthName,
				Type:  dns.RRTypeTXT,
				Class: dns.RRTypeTXT,
			},
		},
		Additional: []dns.RR{
			{
				Name:  dns.Name{},
				Type:  dns.RRTypeOPT,
				Class: queryLimit,
				TTL:   0,
				Data:  []byte{},
			},
		},
	}
	resp, _ := mockResponseFor(query, dns.Name([][]byte{}))
	resp.Answer = []dns.RR{
		{
			Name:  query.Question[0].Name,
			Type:  query.Question[0].Type,
			Class: query.Question[0].Class,
			TTL:   mockResponseTTL,
			Data:  nil,
		},
	}

	low := 0
	high := 32768
	for low+1 < high {
		mid := (low + high) / 2
		resp.Answer[0].Data = dns.EncodeRDataTXT(make([]byte, mid))
		buf, err := resp.WireFormat()
		if err != nil {
			panic(err)
		}
		if len(buf) <= limit {
			low = mid
		} else {
			high = mid
		}
	}

	return low
}

// ---------- mockDNSServer ----------

type mockDNSServer struct {
	domain            dns.Name
	privkey, pubkey   []byte
	udpConns          []*net.UDPConn
	addrs             []*net.UDPAddr
	behaviors         []func(int) mockBehaviorResult
	reqCounts         []int64
	ttConn            *turbotunnel.QueuePacketConn
	kcpLn             *kcp.Listener
	ch                chan *mockRecord
	maxEncodedPayload int
	stop              chan struct{}
	wg                sync.WaitGroup
}

func newMockDNSServer(t *testing.T, behaviors []func(int) mockBehaviorResult) *mockDNSServer {
	t.Helper()

	domain, err := dns.ParseName("t.example.com")
	if err != nil {
		t.Fatal(err)
	}

	privkey, err := noise.GeneratePrivkey()
	if err != nil {
		t.Fatal(err)
	}
	pubkey := noise.PubkeyFromPrivkey(privkey)

	maxEncodedPayload := mockComputeMaxEncodedPayload(mockMaxUDPPayload)
	mtu := maxEncodedPayload - 2
	if mtu < 80 {
		t.Fatalf("MTU too small: %d", mtu)
	}

	ttConn := turbotunnel.NewQueuePacketConn(turbotunnel.DummyAddr{}, mockIdleTimeout*2)
	kcpLn, err := kcp.ServeConn(nil, 0, 0, ttConn)
	if err != nil {
		t.Fatal(err)
	}

	n := len(behaviors)
	s := &mockDNSServer{
		domain:            domain,
		privkey:           privkey,
		pubkey:            pubkey,
		udpConns:          make([]*net.UDPConn, n),
		addrs:             make([]*net.UDPAddr, n),
		behaviors:         behaviors,
		reqCounts:         make([]int64, n),
		ttConn:            ttConn,
		kcpLn:             kcpLn,
		ch:                make(chan *mockRecord, 1024),
		maxEncodedPayload: maxEncodedPayload,
		stop:              make(chan struct{}),
	}

	// Create one UDP listener per behavior.
	for i := 0; i < n; i++ {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		if err != nil {
			t.Fatal(err)
		}
		s.udpConns[i] = conn
		s.addrs[i] = conn.LocalAddr().(*net.UDPAddr)
	}

	// Start per-listener recv loops.
	for i := 0; i < n; i++ {
		s.wg.Add(1)
		go s.recvLoop(i)
	}

	// Start the shared send loop.
	s.wg.Add(1)
	go s.sendLoop()

	// Start session acceptor.
	s.wg.Add(1)
	go s.acceptSessions(mtu)

	return s
}

func (s *mockDNSServer) close() {
	close(s.stop)
	for _, c := range s.udpConns {
		c.Close()
	}
	s.kcpLn.Close()
	s.ttConn.Close()
	// Don't close(s.ch) — sendLoop and recvLoop exit via s.stop.
	// Closing ch would race with recvLoop's send-on-ch select.
	s.wg.Wait()
}

// recvLoop is replicated from dnstt-server/main.go recvLoop, with behavior
// injection and connIdx tracking.
func (s *mockDNSServer) recvLoop(idx int) {
	defer s.wg.Done()
	conn := s.udpConns[idx]
	for {
		select {
		case <-s.stop:
			return
		default:
		}

		var buf [4096]byte
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := conn.ReadFromUDP(buf[:])
		if err != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				continue
			}
			select {
			case <-s.stop:
				return
			default:
			}
			continue
		}

		reqNum := int(atomic.AddInt64(&s.reqCounts[idx], 1))
		beh := s.behaviors[idx](reqNum)

		query, err := dns.MessageFromWireFormat(buf[:n])
		if err != nil {
			continue
		}

		resp, payload := mockResponseFor(&query, s.domain)
		var clientID turbotunnel.ClientID
		cn := copy(clientID[:], payload)
		payload = payload[cn:]
		if cn == len(clientID) {
			r := bytes.NewReader(payload)
			for {
				p, err := mockNextPacket(r)
				if err != nil {
					break
				}
				s.ttConn.QueueIncoming(p, clientID)
			}
		} else {
			if resp != nil && resp.Rcode() == dns.RcodeNoError {
				resp.Flags |= dns.RcodeNameError
			}
		}

		if resp != nil {
			switch beh.action {
			case actionDrop:
				continue
			case actionDelay:
				time.Sleep(beh.delay)
			case actionCorrupt:
				// Mark for corruption in sendLoop.
			}

			rec := &mockRecord{
				Resp:     resp,
				Addr:     addr,
				ClientID: clientID,
				ConnIdx:  idx,
				Corrupt:  beh.action == actionCorrupt,
			}
			select {
			case s.ch <- rec:
			case <-s.stop:
				return
			default:
			}
		}
	}
}

// sendLoop is replicated from dnstt-server/main.go sendLoop, extended to route
// responses to the correct UDP conn via ConnIdx.
func (s *mockDNSServer) sendLoop() {
	defer s.wg.Done()
	var nextRec *mockRecord
	for {
		rec := nextRec
		nextRec = nil

		if rec == nil {
			var ok bool
			select {
			case rec, ok = <-s.ch:
				if !ok {
					return
				}
			case <-s.stop:
				return
			}
		}

		if rec.Resp.Rcode() == dns.RcodeNoError && len(rec.Resp.Question) == 1 {
			rec.Resp.Answer = []dns.RR{
				{
					Name:  rec.Resp.Question[0].Name,
					Type:  rec.Resp.Question[0].Type,
					Class: rec.Resp.Question[0].Class,
					TTL:   mockResponseTTL,
					Data:  nil,
				},
			}

			var payload bytes.Buffer
			limit := s.maxEncodedPayload
			timer := time.NewTimer(mockMaxResponseDelay)
			for {
				var p []byte
				unstash := s.ttConn.Unstash(rec.ClientID)
				outgoing := s.ttConn.OutgoingQueue(rec.ClientID)
				select {
				case p = <-unstash:
				default:
					select {
					case p = <-unstash:
					case p = <-outgoing:
					default:
						select {
						case p = <-unstash:
						case p = <-outgoing:
						case <-timer.C:
						case nextRec = <-s.ch:
						case <-s.stop:
							timer.Stop()
							return
						}
					}
				}
				timer.Reset(0)

				if len(p) == 0 {
					break
				}

				limit -= 2 + len(p)
				if payload.Len() == 0 {
					// First packet: allow even if oversized.
				} else if limit < 0 {
					s.ttConn.Stash(p, rec.ClientID)
					break
				}
				if int(uint16(len(p))) != len(p) {
					panic(len(p))
				}
				binary.Write(&payload, binary.BigEndian, uint16(len(p)))
				payload.Write(p)
			}
			timer.Stop()

			rec.Resp.Answer[0].Data = dns.EncodeRDataTXT(payload.Bytes())
		}

		buf, err := rec.Resp.WireFormat()
		if err != nil {
			continue
		}
		if len(buf) > mockMaxUDPPayload {
			buf = buf[:mockMaxUDPPayload]
			buf[2] |= 0x02 // TC = 1
		}

		if rec.Corrupt {
			if len(buf) > 2 {
				buf[0] ^= 0xff
			}
		}

		conn := s.udpConns[rec.ConnIdx]
		_, err = conn.WriteTo(buf, rec.Addr)
		if err != nil {
			select {
			case <-s.stop:
				return
			default:
			}
		}
	}
}

// acceptSessions is replicated from dnstt-server/main.go acceptSessions,
// but instead of forwarding to upstream, it echoes streams.
func (s *mockDNSServer) acceptSessions(mtu int) {
	defer s.wg.Done()
	for {
		conn, err := s.kcpLn.AcceptKCP()
		if err != nil {
			select {
			case <-s.stop:
				return
			default:
			}
			if nerr, ok := err.(net.Error); ok && nerr.Temporary() {
				continue
			}
			return
		}
		conn.SetStreamMode(true)
		conn.SetNoDelay(1, 10, 2, 1)
		conn.SetWindowSize(turbotunnel.QueueSize/2, turbotunnel.QueueSize/2)
		if rc := conn.SetMtu(mtu); !rc {
			panic(rc)
		}
		go func() {
			defer conn.Close()
			err := s.acceptStreams(conn)
			if err != nil && !errors.Is(err, io.ErrClosedPipe) {
				log.Printf("mock acceptStreams: %v", err)
			}
		}()
	}
}

// acceptStreams wraps a KCP session in Noise + smux and echoes all streams.
func (s *mockDNSServer) acceptStreams(conn *kcp.UDPSession) error {
	rw, err := noise.NewServer(conn, s.privkey)
	if err != nil {
		return err
	}

	smuxConfig := smux.DefaultConfig()
	smuxConfig.Version = 2
	smuxConfig.KeepAliveTimeout = mockIdleTimeout
	smuxConfig.MaxStreamBuffer = 1 * 1024 * 1024
	sess, err := smux.Server(rw, smuxConfig)
	if err != nil {
		return err
	}
	defer sess.Close()

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Temporary() {
				continue
			}
			return err
		}
		go func() {
			defer stream.Close()
			// Echo: copy everything back. For line-based tests,
			// we use a buffered approach to echo complete lines.
			r := bufio.NewReader(stream)
			for {
				line, err := r.ReadBytes('\n')
				if len(line) > 0 {
					stream.Write(line)
				}
				if err != nil {
					return
				}
			}
		}()
	}
}

// ---------- Test helper: setupMockTunnel ----------

type mockTunnel struct {
	server    *mockDNSServer
	localAddr string
	obsFile   string
	multi     *MultiDNSPacketConn
	cleanup   func()
}

// setupMockTunnel builds the full client stack in-process: MultiDNSPacketConn
// -> KCP -> Noise -> smux, with a TCP listener for local connections. It
// manages the TCP listener directly so cleanup can close it (unlike run(),
// which blocks on Accept forever).
func setupMockTunnel(t *testing.T, behaviors []func(int) mockBehaviorResult) *mockTunnel {
	t.Helper()

	srv := newMockDNSServer(t, behaviors)
	domain := srv.domain
	health := fastHealth()

	// Create a DNSPacketConn + serverInfo for each listener.
	var serverInfos []*serverInfo
	for _, udpAddr := range srv.addrs {
		conn, err := net.ListenUDP("udp", nil)
		if err != nil {
			t.Fatal(err)
		}
		dnsConn := NewDNSPacketConn(conn, udpAddr, domain, turbotunnel.NewClientID())
		si := &serverInfo{
			name:    fmt.Sprintf("127.0.0.1:%d", udpAddr.Port),
			dnsConn: dnsConn,
			addr:    udpAddr,
		}
		wireHealthCallbacks(si, dnsConn, nil, health)
		serverInfos = append(serverInfos, si)
	}

	// Create observations file.
	obsFile := filepath.Join(t.TempDir(), "obs.json")

	// Create MultiDNSPacketConn.
	multi := NewMultiDNSPacketConn(serverInfos, obsFile, health)

	// Build client stack manually (replicated from run() in main.go) so we
	// control the TCP listener and can close it during cleanup.
	remoteAddr := turbotunnel.DummyAddr{}

	mtu := dnsNameCapacity(domain) - 8 - 1 - numPadding - 1
	if mtu < 80 {
		t.Fatalf("domain %s leaves only %d bytes for payload", domain, mtu)
	}

	kcpConn, err := kcp.NewConn2(remoteAddr, nil, 0, 0, multi)
	if err != nil {
		t.Fatalf("opening KCP conn: %v", err)
	}
	kcpConn.SetStreamMode(true)
	kcpConn.SetNoDelay(1, 10, 2, 1)
	kcpConn.SetWindowSize(turbotunnel.QueueSize/2, turbotunnel.QueueSize/2)
	if rc := kcpConn.SetMtu(mtu); !rc {
		panic(rc)
	}

	rw, err := noise.NewClient(kcpConn, srv.pubkey)
	if err != nil {
		t.Fatalf("noise handshake: %v", err)
	}

	smuxConfig := smux.DefaultConfig()
	smuxConfig.Version = 2
	smuxConfig.KeepAliveTimeout = idleTimeout
	smuxConfig.MaxStreamBuffer = 1 * 1024 * 1024
	sess, err := smux.Client(rw, smuxConfig)
	if err != nil {
		t.Fatalf("smux client: %v", err)
	}

	// Create the local TCP listener.
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	localAddr := ln.Addr().String()

	// Accept loop in a goroutine.
	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer local.Close()
				err := handle(local.(*net.TCPConn), sess, kcpConn.GetConv())
				if err != nil {
					log.Printf("handle: %v", err)
				}
			}()
		}
	}()

	cleanup := func() {
		ln.Close()
		sess.Close()
		kcpConn.Close()
		multi.Close()
		srv.close()
	}

	return &mockTunnel{
		server:    srv,
		localAddr: localAddr,
		obsFile:   obsFile,
		multi:     multi,
		cleanup:   cleanup,
	}
}

// sendAndCollectMockEchoes sends N newline-terminated messages through the
// tunnel and returns the number that echoed correctly. It enforces an overall
// deadline so the test doesn't hang when the tunnel is disrupted. Responses
// need not arrive in order (KCP retransmissions can reorder delivery), so any
// response matching the "mock-msg-NNNN" pattern counts as a success.
func sendAndCollectMockEchoes(t *testing.T, localAddr string, count int, perMsgWait time.Duration) int {
	t.Helper()
	// Overall deadline: enough time for count messages with some slack,
	// but never more than 45s to prevent test hangs.
	totalTimeout := time.Duration(count) * (perMsgWait + 5*time.Second)
	if totalTimeout > 45*time.Second {
		totalTimeout = 45 * time.Second
	}
	deadline := time.Now().Add(totalTimeout)

	conn, err := net.DialTimeout("tcp", localAddr, 15*time.Second)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()

	// Build set of expected messages.
	expected := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		expected[fmt.Sprintf("mock-msg-%04d\n", i)] = true
	}

	r := bufio.NewReader(conn)
	succeeded := 0
	for i := 0; i < count; i++ {
		if time.Now().After(deadline) {
			t.Logf("overall deadline reached after %d/%d messages (%d succeeded)", i, count, succeeded)
			break
		}
		msg := fmt.Sprintf("mock-msg-%04d\n", i)
		conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, err := conn.Write([]byte(msg))
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		readTimeout := 5 * time.Second
		remaining := time.Until(deadline)
		if remaining < readTimeout {
			readTimeout = remaining
		}
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		resp, err := r.ReadBytes('\n')
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// Accept any valid echo response (responses may arrive out of
		// order due to KCP retransmissions through different servers).
		respStr := string(resp)
		if expected[respStr] {
			succeeded++
			delete(expected, respStr)
		}
		time.Sleep(perMsgWait)
	}
	return succeeded
}

// readObservations reads and parses the observations JSON file.
func readObservations(t *testing.T, obsFile string) (servers []observedServer, routing observedRouting) {
	t.Helper()
	// Wait for at least one observation write.
	var data []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		data, err = os.ReadFile(obsFile)
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatal("no observations written")
	}
	type doc struct {
		Servers []observedServer `json:"servers"`
		Routing observedRouting  `json:"routing"`
	}
	var d doc
	if err := parseJSON(data, &d); err != nil {
		t.Fatalf("parse obs: %v\n%s", err, string(data))
	}
	return d.Servers, d.Routing
}

type observedServer struct {
	Name           string  `json:"name"`
	Working        bool    `json:"working"`
	NotBefore      string  `json:"not_before"`
	Requests       int64   `json:"requests"`
	RateEstimate   float64 `json:"rate_estimate"`
	TokensAvail    float64 `json:"tokens_avail"`
	RecheckBackoff string  `json:"recheck_backoff"`
}

type observedRouting struct {
	Phase1Writes int64 `json:"phase1_writes"`
	Phase2Writes int64 `json:"phase2_writes"`
	Phase3Writes int64 `json:"phase3_writes"`
	Phase4Writes int64 `json:"phase4_writes"`
}

// parseJSON is a small helper that wraps json.Unmarshal.
func parseJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
