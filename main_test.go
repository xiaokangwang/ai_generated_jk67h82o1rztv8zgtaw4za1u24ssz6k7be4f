package main

import (
	"bytes"
	"io"
	"log"
	"net"
	"testing"
	"time"
)

func TestParseSocks5UDPRequestIPv4(t *testing.T) {
	packet := []byte{
		0x00, 0x00, 0x00, 0x01,
		1, 2, 3, 4,
		0x14, 0xe9,
	}
	packet = append(packet, []byte("ping")...)

	addr, payload, err := parseSocks5UDPRequest(packet)
	if err != nil {
		t.Fatalf("parseSocks5UDPRequest returned error: %v", err)
	}

	if got, want := addr.IP.String(), "1.2.3.4"; got != want {
		t.Fatalf("unexpected IP: got %s want %s", got, want)
	}

	if got, want := addr.Port, 5353; got != want {
		t.Fatalf("unexpected port: got %d want %d", got, want)
	}

	if got, want := string(payload), "ping"; got != want {
		t.Fatalf("unexpected payload: got %q want %q", got, want)
	}
}

func TestParseSocks5UDPRequestRejectsFragments(t *testing.T) {
	packet := []byte{
		0x00, 0x00, 0x01, 0x01,
		127, 0, 0, 1,
		0x00, 0x35,
	}

	if _, _, err := parseSocks5UDPRequest(packet); err == nil {
		t.Fatal("expected fragmented packet to be rejected")
	}
}

func TestParseSocks5UDPRequestRejectsDomainNames(t *testing.T) {
	packet := []byte{
		0x00, 0x00, 0x00, 0x03,
		0x0b,
	}
	packet = append(packet, []byte("example.com")...)
	packet = append(packet, 0x00, 0x35)
	packet = append(packet, []byte("ping")...)

	if _, _, err := parseSocks5UDPRequest(packet); err == nil {
		t.Fatal("expected domain name request to be rejected")
	}
}

func TestParseSocks5UDPRequestIPv6(t *testing.T) {
	packet := []byte{
		0x00, 0x00, 0x00, 0x04,
		0x20, 0x01, 0x0d, 0xb8,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x01,
		0x14, 0xe9,
	}
	packet = append(packet, []byte("ping6")...)

	addr, payload, err := parseSocks5UDPRequest(packet)
	if err != nil {
		t.Fatalf("parseSocks5UDPRequest returned error: %v", err)
	}

	if got, want := addr.IP.String(), "2001:db8::1"; got != want {
		t.Fatalf("unexpected IP: got %s want %s", got, want)
	}

	if got, want := addr.Port, 5353; got != want {
		t.Fatalf("unexpected port: got %d want %d", got, want)
	}

	if got, want := string(payload), "ping6"; got != want {
		t.Fatalf("unexpected payload: got %q want %q", got, want)
	}
}

func TestBuildSocks5UDPDatagramIPv4(t *testing.T) {
	addr := &net.UDPAddr{
		IP:   net.IPv4(9, 8, 7, 6),
		Port: 9999,
	}

	packet, err := buildSocks5UDPDatagram(addr, []byte("pong"))
	if err != nil {
		t.Fatalf("buildSocks5UDPDatagram returned error: %v", err)
	}

	want := []byte{
		0x00, 0x00, 0x00, 0x01,
		9, 8, 7, 6,
		0x27, 0x0f,
	}
	want = append(want, []byte("pong")...)

	if !bytes.Equal(packet, want) {
		t.Fatalf("unexpected packet bytes:\n got %v\nwant %v", packet, want)
	}
}

func TestBuildSocks5UDPDatagramIPv6(t *testing.T) {
	addr := &net.UDPAddr{
		IP:   net.ParseIP("2001:db8::2"),
		Port: 9999,
	}

	packet, err := buildSocks5UDPDatagram(addr, []byte("pong6"))
	if err != nil {
		t.Fatalf("buildSocks5UDPDatagram returned error: %v", err)
	}

	want := []byte{
		0x00, 0x00, 0x00, 0x04,
		0x20, 0x01, 0x0d, 0xb8,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x02,
		0x27, 0x0f,
	}
	want = append(want, []byte("pong6")...)

	if !bytes.Equal(packet, want) {
		t.Fatalf("unexpected packet bytes:\n got %v\nwant %v", packet, want)
	}
}

func TestProxyRelaysUDP(t *testing.T) {
	echoAddr, stopEcho := startUDPEchoServer(t)
	defer stopEcho()

	logger := log.New(io.Discard, "", 0)
	p, err := newProxy("127.0.0.1:0", time.Minute, logger)
	if err != nil {
		t.Fatalf("newProxy returned error: %v", err)
	}
	defer p.listener.Close()

	done := make(chan error, 1)
	go func() {
		done <- p.serve()
	}()

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP client returned error: %v", err)
	}
	defer client.Close()

	request, err := buildSocks5UDPDatagram(echoAddr, []byte("hello over socks5 udp"))
	if err != nil {
		t.Fatalf("buildSocks5UDPDatagram request returned error: %v", err)
	}

	proxyAddr := p.listener.LocalAddr().(*net.UDPAddr)
	if _, err := client.WriteToUDP(request, proxyAddr); err != nil {
		t.Fatalf("client WriteToUDP returned error: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))

	buffer := make([]byte, 2048)
	n, _, err := client.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("client ReadFromUDP returned error: %v", err)
	}

	addr, payload, err := parseSocks5UDPRequest(buffer[:n])
	if err != nil {
		t.Fatalf("parseSocks5UDPRequest response returned error: %v", err)
	}

	if got, want := string(payload), "hello over socks5 udp"; got != want {
		t.Fatalf("unexpected response payload: got %q want %q", got, want)
	}

	if got, want := addr.String(), echoAddr.String(); got != want {
		t.Fatalf("unexpected response source: got %s want %s", got, want)
	}

	_ = p.listener.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected serve to stop with an error after closing listener")
		}
	case <-time.After(time.Second):
		t.Fatal("proxy serve did not stop after closing listener")
	}
}

func TestSplitDestinationsUseDifferentSourcePorts(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	p, err := newProxyWithOptions("127.0.0.1:0", time.Minute, true, logger)
	if err != nil {
		t.Fatalf("newProxyWithOptions returned error: %v", err)
	}
	defer p.listener.Close()

	clientAddr := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 40000,
	}
	targetOne := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 50001,
	}
	targetTwo := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 50002,
	}

	assoc, err := p.getAssociation(clientAddr)
	if err != nil {
		t.Fatalf("getAssociation returned error: %v", err)
	}

	destOne, err := p.getDestinationAssociation(assoc, targetOne)
	if err != nil {
		t.Fatalf("getDestinationAssociation targetOne returned error: %v", err)
	}

	destTwo, err := p.getDestinationAssociation(assoc, targetTwo)
	if err != nil {
		t.Fatalf("getDestinationAssociation targetTwo returned error: %v", err)
	}

	upstreamOne, ok := destOne.upstream.(*dialedUpstream)
	if !ok {
		t.Fatal("expected destination one upstream to be a dialed socket")
	}

	upstreamTwo, ok := destTwo.upstream.(*dialedUpstream)
	if !ok {
		t.Fatal("expected destination two upstream to be a dialed socket")
	}

	portOne := upstreamOne.conn.LocalAddr().(*net.UDPAddr).Port
	portTwo := upstreamTwo.conn.LocalAddr().(*net.UDPAddr).Port

	if portOne == portTwo {
		t.Fatalf("expected different upstream source ports, got %d and %d", portOne, portTwo)
	}

	if got := len(assoc.destinations); got != 2 {
		t.Fatalf("unexpected destination map size: got %d want 2", got)
	}
}

func TestSplitDestinationsRelayResponsesFromMultipleDestinations(t *testing.T) {
	echoAddrOne, stopEchoOne := startUDPEchoServer(t)
	defer stopEchoOne()

	echoAddrTwo, stopEchoTwo := startUDPEchoServer(t)
	defer stopEchoTwo()

	logger := log.New(io.Discard, "", 0)
	p, err := newProxyWithOptions("127.0.0.1:0", time.Minute, true, logger)
	if err != nil {
		t.Fatalf("newProxyWithOptions returned error: %v", err)
	}
	defer p.listener.Close()

	done := make(chan error, 1)
	go func() {
		done <- p.serve()
	}()

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP client returned error: %v", err)
	}
	defer client.Close()

	requestOne, err := buildSocks5UDPDatagram(echoAddrOne, []byte("payload-one"))
	if err != nil {
		t.Fatalf("buildSocks5UDPDatagram requestOne returned error: %v", err)
	}

	requestTwo, err := buildSocks5UDPDatagram(echoAddrTwo, []byte("payload-two"))
	if err != nil {
		t.Fatalf("buildSocks5UDPDatagram requestTwo returned error: %v", err)
	}

	proxyAddr := p.listener.LocalAddr().(*net.UDPAddr)
	if _, err := client.WriteToUDP(requestOne, proxyAddr); err != nil {
		t.Fatalf("client WriteToUDP requestOne returned error: %v", err)
	}

	if _, err := client.WriteToUDP(requestTwo, proxyAddr); err != nil {
		t.Fatalf("client WriteToUDP requestTwo returned error: %v", err)
	}

	responses := make(map[string]string)
	buffer := make([]byte, 2048)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))

	for i := 0; i < 2; i++ {
		n, _, err := client.ReadFromUDP(buffer)
		if err != nil {
			t.Fatalf("client ReadFromUDP returned error: %v", err)
		}

		addr, payload, err := parseSocks5UDPRequest(buffer[:n])
		if err != nil {
			t.Fatalf("parseSocks5UDPRequest returned error: %v", err)
		}

		responses[addr.String()] = string(payload)
	}

	if got, want := responses[echoAddrOne.String()], "payload-one"; got != want {
		t.Fatalf("unexpected response from destination one: got %q want %q", got, want)
	}

	if got, want := responses[echoAddrTwo.String()], "payload-two"; got != want {
		t.Fatalf("unexpected response from destination two: got %q want %q", got, want)
	}

	_ = p.listener.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected serve to stop with an error after closing listener")
		}
	case <-time.After(time.Second):
		t.Fatal("proxy serve did not stop after closing listener")
	}
}

func TestSplitDestinationsEvictLeastRecentlyUsedSocket(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	p, err := newProxyWithSettings("127.0.0.1:0", time.Minute, true, 2, false, time.Second, 0, logger)
	if err != nil {
		t.Fatalf("newProxyWithSettings returned error: %v", err)
	}
	defer p.listener.Close()

	clientAddr := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 41000,
	}
	targetOne := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 51001,
	}
	targetTwo := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 51002,
	}
	targetThree := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 51003,
	}

	assoc, err := p.getAssociation(clientAddr)
	if err != nil {
		t.Fatalf("getAssociation returned error: %v", err)
	}

	destOne, err := p.getDestinationAssociation(assoc, targetOne)
	if err != nil {
		t.Fatalf("getDestinationAssociation targetOne returned error: %v", err)
	}

	destTwo, err := p.getDestinationAssociation(assoc, targetTwo)
	if err != nil {
		t.Fatalf("getDestinationAssociation targetTwo returned error: %v", err)
	}

	destOne.mu.Lock()
	destOne.lastSeen = time.Unix(1, 0)
	destOne.mu.Unlock()

	destTwo.mu.Lock()
	destTwo.lastSeen = time.Unix(2, 0)
	destTwo.mu.Unlock()

	destThree, err := p.getDestinationAssociation(assoc, targetThree)
	if err != nil {
		t.Fatalf("getDestinationAssociation targetThree returned error: %v", err)
	}

	if destThree == nil {
		t.Fatal("expected targetThree destination to be created")
	}

	if got := len(assoc.destinations); got != 2 {
		t.Fatalf("unexpected destination map size: got %d want 2", got)
	}

	if _, ok := assoc.destinations[p.destinationKey(targetOne)]; ok {
		t.Fatal("expected least recently used destination to be evicted")
	}

	if _, ok := assoc.destinations[p.destinationKey(targetTwo)]; !ok {
		t.Fatal("expected second destination to remain open")
	}

	if _, ok := assoc.destinations[p.destinationKey(targetThree)]; !ok {
		t.Fatal("expected new destination to be added")
	}
}

func TestIncomingFilterAllowsRecentDestination(t *testing.T) {
	targetConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP target returned error: %v", err)
	}
	defer targetConn.Close()

	logger := log.New(io.Discard, "", 0)
	p, err := newProxyWithSettings("127.0.0.1:0", time.Minute, false, 0, true, time.Second, 0, logger)
	if err != nil {
		t.Fatalf("newProxyWithSettings returned error: %v", err)
	}
	defer p.listener.Close()

	done := make(chan error, 1)
	go func() {
		done <- p.serve()
	}()

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP client returned error: %v", err)
	}
	defer client.Close()

	request, err := buildSocks5UDPDatagram(targetConn.LocalAddr().(*net.UDPAddr), []byte("hello moderate nat"))
	if err != nil {
		t.Fatalf("buildSocks5UDPDatagram request returned error: %v", err)
	}

	if _, err := client.WriteToUDP(request, p.listener.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("client WriteToUDP returned error: %v", err)
	}

	targetBuffer := make([]byte, 2048)
	_ = targetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, upstreamAddr, err := targetConn.ReadFromUDP(targetBuffer)
	if err != nil {
		t.Fatalf("target ReadFromUDP returned error: %v", err)
	}

	if got, want := string(targetBuffer[:n]), "hello moderate nat"; got != want {
		t.Fatalf("unexpected target payload: got %q want %q", got, want)
	}

	if _, err := targetConn.WriteToUDP([]byte("reply moderate nat"), upstreamAddr); err != nil {
		t.Fatalf("target WriteToUDP returned error: %v", err)
	}

	clientBuffer := make([]byte, 2048)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = client.ReadFromUDP(clientBuffer)
	if err != nil {
		t.Fatalf("client ReadFromUDP returned error: %v", err)
	}

	addr, payload, err := parseSocks5UDPRequest(clientBuffer[:n])
	if err != nil {
		t.Fatalf("parseSocks5UDPRequest returned error: %v", err)
	}

	if got, want := addr.String(), targetConn.LocalAddr().String(); got != want {
		t.Fatalf("unexpected response source: got %s want %s", got, want)
	}

	if got, want := string(payload), "reply moderate nat"; got != want {
		t.Fatalf("unexpected response payload: got %q want %q", got, want)
	}

	_ = p.listener.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected serve to stop with an error after closing listener")
		}
	case <-time.After(time.Second):
		t.Fatal("proxy serve did not stop after closing listener")
	}
}

func TestIncomingFilterBlocksExpiredDestination(t *testing.T) {
	targetConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP target returned error: %v", err)
	}
	defer targetConn.Close()

	logger := log.New(io.Discard, "", 0)
	p, err := newProxyWithSettings("127.0.0.1:0", time.Minute, false, 0, true, 40*time.Millisecond, 0, logger)
	if err != nil {
		t.Fatalf("newProxyWithSettings returned error: %v", err)
	}
	defer p.listener.Close()

	done := make(chan error, 1)
	go func() {
		done <- p.serve()
	}()

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP client returned error: %v", err)
	}
	defer client.Close()

	request, err := buildSocks5UDPDatagram(targetConn.LocalAddr().(*net.UDPAddr), []byte("prime filter"))
	if err != nil {
		t.Fatalf("buildSocks5UDPDatagram request returned error: %v", err)
	}

	if _, err := client.WriteToUDP(request, p.listener.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("client WriteToUDP returned error: %v", err)
	}

	targetBuffer := make([]byte, 2048)
	_ = targetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, upstreamAddr, err := targetConn.ReadFromUDP(targetBuffer)
	if err != nil {
		t.Fatalf("target ReadFromUDP returned error: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if _, err := targetConn.WriteToUDP([]byte("late packet"), upstreamAddr); err != nil {
		t.Fatalf("target WriteToUDP returned error: %v", err)
	}

	clientBuffer := make([]byte, 2048)
	_ = client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := client.ReadFromUDP(clientBuffer); err == nil {
		t.Fatal("expected expired source packet to be dropped")
	}

	_ = p.listener.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected serve to stop with an error after closing listener")
		}
	case <-time.After(time.Second):
		t.Fatal("proxy serve did not stop after closing listener")
	}
}

func TestIncomingFilterEvictsLeastRecentlyUsedAllowedDestination(t *testing.T) {
	assoc := &association{
		recentDestinations: map[string]time.Time{
			"127.0.0.1:53001": time.Unix(1, 0),
			"127.0.0.1:53002": time.Unix(2, 0),
		},
	}

	assoc.noteRecentDestination(&net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 53003,
	}, 2)

	if got := len(assoc.recentDestinations); got != 2 {
		t.Fatalf("unexpected allow list size: got %d want 2", got)
	}

	if _, ok := assoc.recentDestinations["127.0.0.1:53001"]; ok {
		t.Fatal("expected least recently used destination to be evicted")
	}

	if _, ok := assoc.recentDestinations["127.0.0.1:53002"]; !ok {
		t.Fatal("expected second destination to remain allowed")
	}

	if _, ok := assoc.recentDestinations["127.0.0.1:53003"]; !ok {
		t.Fatal("expected new destination to be added to allow list")
	}
}

func startUDPEchoServer(t *testing.T) (*net.UDPAddr, func()) {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP echo returned error: %v", err)
	}

	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		buffer := make([]byte, 2048)
		for {
			n, clientAddr, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}

			if _, err := conn.WriteToUDP(buffer[:n], clientAddr); err != nil {
				return
			}
		}
	}()

	return conn.LocalAddr().(*net.UDPAddr), func() {
		_ = conn.Close()
		<-stopped
	}
}
