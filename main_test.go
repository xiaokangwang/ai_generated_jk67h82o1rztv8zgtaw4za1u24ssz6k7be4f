package main

import (
	"bytes"
	"net/netip"
	"testing"
	"time"
)

func TestParseSocksTargetIPv4(t *testing.T) {
	payload := []byte{
		0x00, 0x00, 0x00, 0x01,
		0xd4, 0x12, 0x00, 0x0e,
		0x0d, 0x96,
		0xaa, 0xbb,
	}

	target, inner, err := parseSocksTarget(payload)
	if err != nil {
		t.Fatalf("parseSocksTarget() error = %v", err)
	}
	if target.Addr.String() != "212.18.0.14" {
		t.Fatalf("unexpected address %s", target.Addr)
	}
	if target.Port != 3478 {
		t.Fatalf("unexpected port %d", target.Port)
	}
	if !bytes.Equal(inner, []byte{0xaa, 0xbb}) {
		t.Fatalf("unexpected payload %x", inner)
	}
}

func TestParseSocksTargetIPv6(t *testing.T) {
	payload := []byte{
		0x00, 0x00, 0x00, 0x04,
		0x20, 0x01, 0x0d, 0xb8, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
		0x13, 0x88,
		0x01,
	}

	target, inner, err := parseSocksTarget(payload)
	if err != nil {
		t.Fatalf("parseSocksTarget() error = %v", err)
	}
	if target.Addr.String() != "2001:db8::1" {
		t.Fatalf("unexpected address %s", target.Addr)
	}
	if target.Port != 5000 {
		t.Fatalf("unexpected port %d", target.Port)
	}
	if !bytes.Equal(inner, []byte{0x01}) {
		t.Fatalf("unexpected payload %x", inner)
	}
}

func TestParseSocksTargetRejectsFragments(t *testing.T) {
	payload := []byte{0x00, 0x00, 0x01, 0x01, 127, 0, 0, 1, 0x1f, 0x90}
	if _, _, err := parseSocksTarget(payload); err != errFragmentedSocks {
		t.Fatalf("expected errFragmentedSocks, got %v", err)
	}
}

func TestRestorePacketOutgoing(t *testing.T) {
	raw := buildIPv4TestPacket(t, "127.0.0.1", "127.0.0.1", 55299, 11803, []byte{
		0x00, 0x00, 0x00, 0x01,
		0xd4, 0x12, 0x00, 0x0e,
		0x0d, 0x96,
		0xde, 0xad, 0xbe, 0xef,
	})

	restored, transformed, err := restorePacket(raw, netip.MustParseAddrPort("127.0.0.1:11803"))
	if err != nil {
		t.Fatalf("restorePacket() error = %v", err)
	}
	if !transformed {
		t.Fatal("expected packet to be transformed")
	}

	view, err := decodePacket(restored)
	if err != nil {
		t.Fatalf("decodePacket(restored) error = %v", err)
	}
	src, dst := view.endpoints()
	if src.String() != "127.0.0.1:55299" {
		t.Fatalf("unexpected src %s", src)
	}
	if dst.String() != "212.18.0.14:3478" {
		t.Fatalf("unexpected dst %s", dst)
	}
	if !bytes.Equal(view.udp.payload, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("unexpected payload %x", view.udp.payload)
	}
}

func TestRestorePacketIncoming(t *testing.T) {
	raw := buildIPv4TestPacket(t, "127.0.0.1", "127.0.0.1", 11803, 34564, []byte{
		0x00, 0x00, 0x00, 0x01,
		0xd4, 0x12, 0x00, 0x0e,
		0x0d, 0x96,
		0xfa, 0xce,
	})

	restored, transformed, err := restorePacket(raw, netip.MustParseAddrPort("127.0.0.1:11803"))
	if err != nil {
		t.Fatalf("restorePacket() error = %v", err)
	}
	if !transformed {
		t.Fatal("expected packet to be transformed")
	}

	view, err := decodePacket(restored)
	if err != nil {
		t.Fatalf("decodePacket(restored) error = %v", err)
	}
	src, dst := view.endpoints()
	if src.String() != "212.18.0.14:3478" {
		t.Fatalf("unexpected src %s", src)
	}
	if dst.String() != "127.0.0.1:34564" {
		t.Fatalf("unexpected dst %s", dst)
	}
	if !bytes.Equal(view.udp.payload, []byte{0xfa, 0xce}) {
		t.Fatalf("unexpected payload %x", view.udp.payload)
	}
}

func TestPickProxyEndpoint(t *testing.T) {
	proxy := netip.MustParseAddrPort("127.0.0.1:11803")
	clientA := netip.MustParseAddrPort("127.0.0.1:34564")
	clientB := netip.MustParseAddrPort("127.0.0.1:55299")

	stats := map[netip.AddrPort]*endpointStats{}
	for _, pair := range [][2]netip.AddrPort{
		{clientA, proxy},
		{proxy, clientA},
		{clientB, proxy},
		{proxy, clientB},
	} {
		srcStats := getEndpointStats(stats, pair[0])
		srcStats.srcCount++
		srcStats.peers[pair[1]] = struct{}{}

		dstStats := getEndpointStats(stats, pair[1])
		dstStats.dstCount++
		dstStats.peers[pair[0]] = struct{}{}
	}

	picked, err := pickProxyEndpoint(stats)
	if err != nil {
		t.Fatalf("pickProxyEndpoint() error = %v", err)
	}
	if picked != proxy {
		t.Fatalf("expected proxy %s, got %s", proxy, picked)
	}
}

func TestPcapngRoundTrip(t *testing.T) {
	packet := buildIPv4TestPacket(t, "127.0.0.1", "127.0.0.1", 1000, 2000, []byte{1, 2, 3, 4})
	var buf bytes.Buffer

	writer, err := newPcapngWriter(&buf)
	if err != nil {
		t.Fatalf("newPcapngWriter() error = %v", err)
	}
	ci := captureInfo{
		Timestamp:      time.Unix(123, 456).UTC(),
		InterfaceIndex: 0,
		CaptureLength:  len(packet),
		Length:         len(packet),
	}
	if err := writer.WritePacket(ci, packet); err != nil {
		t.Fatalf("writer.WritePacket() error = %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("writer.Flush() error = %v", err)
	}

	reader := newPcapngReader(bytes.NewReader(buf.Bytes()))
	gotPacket, gotCI, err := reader.ReadPacket()
	if err != nil {
		t.Fatalf("reader.ReadPacket() error = %v", err)
	}
	if !bytes.Equal(gotPacket, packet) {
		t.Fatalf("packet mismatch")
	}
	if !gotCI.Timestamp.Equal(ci.Timestamp) {
		t.Fatalf("timestamp mismatch: got %s want %s", gotCI.Timestamp, ci.Timestamp)
	}
}

func buildIPv4TestPacket(t *testing.T, srcIP, dstIP string, srcPort, dstPort uint16, payload []byte) []byte {
	t.Helper()

	template := &decodedPacket{
		eth: ethernetHeader{
			etherType: etherTypeIPv4,
		},
		ip4: &ipv4Header{
			ttl:              64,
			flagsAndFragment: 0x4000,
		},
	}
	frame, err := buildPacket(
		template,
		netip.MustParseAddr(srcIP),
		netip.MustParseAddr(dstIP),
		srcPort,
		dstPort,
		payload,
	)
	if err != nil {
		t.Fatalf("buildPacket() error = %v", err)
	}
	return frame
}
