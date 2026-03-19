package main

import (
	"net"
	"time"
)

type packetConnAdapter struct {
	conn       net.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
}

func newPacketConnAdapter(conn net.Conn, localAddr, remoteAddr net.Addr) net.PacketConn {
	return &packetConnAdapter{
		conn:       conn,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
	}
}

func (p *packetConnAdapter) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := p.conn.Read(b)
	return n, p.remoteAddr, err
}

func (p *packetConnAdapter) WriteTo(b []byte, _ net.Addr) (int, error) {
	return p.conn.Write(b)
}

func (p *packetConnAdapter) Close() error {
	return p.conn.Close()
}

func (p *packetConnAdapter) LocalAddr() net.Addr {
	return p.localAddr
}

func (p *packetConnAdapter) SetDeadline(t time.Time) error {
	return p.conn.SetDeadline(t)
}

func (p *packetConnAdapter) SetReadDeadline(t time.Time) error {
	return p.conn.SetReadDeadline(t)
}

func (p *packetConnAdapter) SetWriteDeadline(t time.Time) error {
	return p.conn.SetWriteDeadline(t)
}
