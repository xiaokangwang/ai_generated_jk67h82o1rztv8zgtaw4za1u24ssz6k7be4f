package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xiaokangwang/VLite/interfaces"
)

func TestEncodeDecodeSocks5UDPDatagramIPv4(t *testing.T) {
	t.Parallel()

	packet, err := encodeSocks5UDPDatagram("127.0.0.1:21978", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	address, payload, err := decodeSocks5UDPDatagram(packet)
	if err != nil {
		t.Fatal(err)
	}
	if address != "127.0.0.1:21978" {
		t.Fatalf("unexpected decoded address: %q", address)
	}
	if got := string(payload); got != "hello" {
		t.Fatalf("unexpected decoded payload: %q", got)
	}
}

func TestEncodeDecodeSocks5UDPDatagramDomain(t *testing.T) {
	t.Parallel()

	packet, err := encodeSocks5UDPDatagram("relay.example:5300", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	address, payload, err := decodeSocks5UDPDatagram(packet)
	if err != nil {
		t.Fatal(err)
	}
	if address != "relay.example:5300" {
		t.Fatalf("unexpected decoded address: %q", address)
	}
	if got := string(payload); got != "hello" {
		t.Fatalf("unexpected decoded payload: %q", got)
	}
}

func TestDecodeSocks5UDPDatagramRejectsFragments(t *testing.T) {
	t.Parallel()

	packet := []byte{0x00, 0x00, 0x01, 0x01, 127, 0, 0, 1, 0x55, 0xca}
	_, _, err := decodeSocks5UDPDatagram(packet)
	if !errors.Is(err, errFragmentedSocks5UDPDatagram) {
		t.Fatalf("expected fragment error, got %v", err)
	}
}

func TestSocks5UDPConnWrapsWritesAndUnwrapsReads(t *testing.T) {
	t.Parallel()

	readPacket, err := encodeSocks5UDPDatagram("198.51.100.20:21978", []byte("reply"))
	if err != nil {
		t.Fatal(err)
	}

	targetAddr, err := net.ResolveUDPAddr("udp", "198.51.100.20:21978")
	if err != nil {
		t.Fatal(err)
	}

	relayConn := &stubConn{
		readPacket: readPacket,
		localAddr:  stubAddr("127.0.0.1:40000"),
		remoteAddr: stubAddr("127.0.0.1:1080"),
	}
	conn := &socks5UDPConn{
		relayConn:     relayConn,
		targetAddr:    targetAddr,
		targetAddress: "198.51.100.20:21978",
	}

	written, err := conn.Write([]byte("ping"))
	if err != nil {
		t.Fatal(err)
	}
	if written != len("ping") {
		t.Fatalf("unexpected payload write size: %d", written)
	}

	address, payload, err := decodeSocks5UDPDatagram(relayConn.writePacket)
	if err != nil {
		t.Fatal(err)
	}
	if address != "198.51.100.20:21978" {
		t.Fatalf("unexpected encoded address: %q", address)
	}
	if got := string(payload); got != "ping" {
		t.Fatalf("unexpected encoded payload: %q", got)
	}

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "reply" {
		t.Fatalf("unexpected decoded payload from conn.Read: %q", got)
	}

	if conn.RemoteAddr().String() != "198.51.100.20:21978" {
		t.Fatalf("unexpected remote addr: %s", conn.RemoteAddr())
	}
}

func TestNewRemoteConnContextSetsConnID(t *testing.T) {
	t.Parallel()

	ctx := newRemoteConnContext(context.Background(), &stubConn{
		localAddr:  stubAddr("127.0.0.1:45678"),
		remoteAddr: stubAddr("127.0.0.1:1080"),
	})

	connID, ok := ctx.Value(interfaces.ExtraOptionsConnID).([]byte)
	if !ok {
		t.Fatal("missing conn id from context")
	}
	if string(connID) != "127.0.0.1:45678" {
		t.Fatalf("unexpected conn id: %q", string(connID))
	}
	if ctx.Value(interfaces.ExtraOptionsMessageBusByConn) == nil {
		t.Fatal("missing per-connection message bus")
	}
}

type stubAddr string

func (a stubAddr) Network() string { return "udp" }

func (a stubAddr) String() string { return string(a) }

type stubConn struct {
	readPacket  []byte
	writePacket []byte
	localAddr   net.Addr
	remoteAddr  net.Addr
}

func (c *stubConn) Read(b []byte) (int, error) {
	if len(c.readPacket) == 0 {
		return 0, io.EOF
	}
	n := copy(b, c.readPacket)
	c.readPacket = nil
	return n, nil
}

func (c *stubConn) Write(b []byte) (int, error) {
	c.writePacket = bytes.Clone(b)
	return len(b), nil
}

func (c *stubConn) Close() error { return nil }

func (c *stubConn) LocalAddr() net.Addr { return c.localAddr }

func (c *stubConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *stubConn) SetDeadline(time.Time) error { return nil }

func (c *stubConn) SetReadDeadline(time.Time) error { return nil }

func (c *stubConn) SetWriteDeadline(time.Time) error { return nil }
