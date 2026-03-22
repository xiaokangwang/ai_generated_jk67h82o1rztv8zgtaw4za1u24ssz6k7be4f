package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/xiaokangwang/VLite/interfaces"
	"github.com/xiaokangwang/VLite/interfaces/ibus"
)

const socks5UDPMaxHeaderLength = 262

var (
	errInvalidSocks5UDPDatagram    = errors.New("invalid SOCKS5 UDP datagram")
	errFragmentedSocks5UDPDatagram = errors.New("fragmented SOCKS5 UDP datagram not supported")
)

// socks5UDPConn wraps a UDP socket connected to a relay and emits SOCKS5 UDP datagrams.
type socks5UDPConn struct {
	relayConn     net.Conn
	targetAddr    *net.UDPAddr
	targetAddress string
}

func dialSocks5UDPConn(targetAddress, relayAddress string) (net.Conn, error) {
	targetAddr, err := net.ResolveUDPAddr("udp", targetAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve target address %q: %w", targetAddress, err)
	}

	relayAddr, err := net.ResolveUDPAddr("udp", relayAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve socks5udp relay %q: %w", relayAddress, err)
	}

	relayConn, err := net.DialUDP("udp", nil, relayAddr)
	if err != nil {
		return nil, fmt.Errorf("dial socks5udp relay %q: %w", relayAddress, err)
	}

	return &socks5UDPConn{
		relayConn:     relayConn,
		targetAddr:    targetAddr,
		targetAddress: targetAddress,
	}, nil
}

func newRemoteConnContext(base context.Context, conn net.Conn) context.Context {
	connCtx := context.WithValue(base, interfaces.ExtraOptionsConnID, []byte(conn.LocalAddr().String()))
	return context.WithValue(connCtx, interfaces.ExtraOptionsMessageBusByConn, ibus.NewMessageBus())
}

func (c *socks5UDPConn) Read(b []byte) (int, error) {
	raw := make([]byte, len(b)+socks5UDPMaxHeaderLength)
	n, err := c.relayConn.Read(raw)
	if err != nil {
		return 0, err
	}

	_, payload, err := decodeSocks5UDPDatagram(raw[:n])
	if err != nil {
		return 0, err
	}
	return copy(b, payload), nil
}

func (c *socks5UDPConn) Write(b []byte) (int, error) {
	packet, err := encodeSocks5UDPDatagram(c.targetAddress, b)
	if err != nil {
		return 0, err
	}

	n, err := c.relayConn.Write(packet)
	if err != nil {
		return 0, err
	}
	if n != len(packet) {
		return 0, io.ErrShortWrite
	}
	return len(b), nil
}

func (c *socks5UDPConn) Close() error {
	return c.relayConn.Close()
}

func (c *socks5UDPConn) LocalAddr() net.Addr {
	return c.relayConn.LocalAddr()
}

func (c *socks5UDPConn) RemoteAddr() net.Addr {
	return c.targetAddr
}

func (c *socks5UDPConn) SetDeadline(t time.Time) error {
	return c.relayConn.SetDeadline(t)
}

func (c *socks5UDPConn) SetReadDeadline(t time.Time) error {
	return c.relayConn.SetReadDeadline(t)
}

func (c *socks5UDPConn) SetWriteDeadline(t time.Time) error {
	return c.relayConn.SetWriteDeadline(t)
}

func encodeSocks5UDPDatagram(address string, payload []byte) ([]byte, error) {
	host, portString, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split socks5udp target address %q: %w", address, err)
	}

	port, err := net.LookupPort("udp", portString)
	if err != nil {
		return nil, fmt.Errorf("lookup socks5udp target port %q: %w", portString, err)
	}

	host = stripIPv6Zone(host)
	packet := []byte{0x00, 0x00, 0x00}
	ip := net.ParseIP(host)
	switch {
	case ip != nil && ip.To4() != nil:
		packet = append(packet, 0x01)
		packet = append(packet, ip.To4()...)
	case ip != nil && ip.To16() != nil:
		packet = append(packet, 0x04)
		packet = append(packet, ip.To16()...)
	case host != "":
		if len(host) > 255 {
			return nil, fmt.Errorf("socks5udp target host %q too long", host)
		}
		packet = append(packet, 0x03, byte(len(host)))
		packet = append(packet, host...)
	default:
		return nil, fmt.Errorf("invalid socks5udp target host in %q", address)
	}

	packet = binary.BigEndian.AppendUint16(packet, uint16(port))
	packet = append(packet, payload...)
	return packet, nil
}

func decodeSocks5UDPDatagram(packet []byte) (string, []byte, error) {
	if len(packet) < 4 {
		return "", nil, errInvalidSocks5UDPDatagram
	}
	if packet[0] != 0x00 || packet[1] != 0x00 {
		return "", nil, errInvalidSocks5UDPDatagram
	}
	if packet[2] != 0x00 {
		return "", nil, errFragmentedSocks5UDPDatagram
	}

	offset := 4
	var host string
	switch packet[3] {
	case 0x01:
		if len(packet) < offset+net.IPv4len+2 {
			return "", nil, errInvalidSocks5UDPDatagram
		}
		host = net.IP(packet[offset : offset+net.IPv4len]).String()
		offset += net.IPv4len
	case 0x04:
		if len(packet) < offset+net.IPv6len+2 {
			return "", nil, errInvalidSocks5UDPDatagram
		}
		host = net.IP(packet[offset : offset+net.IPv6len]).String()
		offset += net.IPv6len
	case 0x03:
		if len(packet) < offset+1+2 {
			return "", nil, errInvalidSocks5UDPDatagram
		}
		domainLength := int(packet[offset])
		offset++
		if len(packet) < offset+domainLength+2 {
			return "", nil, errInvalidSocks5UDPDatagram
		}
		host = string(packet[offset : offset+domainLength])
		offset += domainLength
	default:
		return "", nil, errInvalidSocks5UDPDatagram
	}

	port := binary.BigEndian.Uint16(packet[offset : offset+2])
	offset += 2
	return net.JoinHostPort(host, strconv.Itoa(int(port))), packet[offset:], nil
}

func stripIPv6Zone(host string) string {
	if i := strings.LastIndex(host, "%"); i >= 0 {
		return host[:i]
	}
	return host
}
