package main

import (
	"encoding/binary"
	"net"
	"time"
)

const (
	ICMPTypeEchoReply      = 0
	ICMPTypeDestUnreach    = 3
	ICMPTypeEchoRequest    = 8
	ICMPTypeTimeExceeded   = 11

	// ICMP Destination Unreachable codes
	ICMPCodeNetUnreach     = 0
	ICMPCodeHostUnreach    = 1
	ICMPCodeProtocolUnreach = 2
	ICMPCodePortUnreach    = 3
)

// ICMPHeader represents an ICMP packet header
type ICMPHeader struct {
	Type     uint8
	Code     uint8
	Checksum uint16
	ID       uint16
	Sequence uint16
}

// ICMPPacket represents a full ICMP packet
type ICMPPacket struct {
	Header ICMPHeader
	Data   []byte
}

// MarshalBinary encodes the ICMP packet to bytes
func (p *ICMPPacket) MarshalBinary() ([]byte, error) {
	b := make([]byte, 8+len(p.Data))
	b[0] = p.Header.Type
	b[1] = p.Header.Code
	// Checksum is initially zero
	binary.BigEndian.PutUint16(b[2:4], 0)
	binary.BigEndian.PutUint16(b[4:6], p.Header.ID)
	binary.BigEndian.PutUint16(b[6:8], p.Header.Sequence)
	copy(b[8:], p.Data)

	// Calculate checksum
	checksum := calculateChecksum(b)
	binary.BigEndian.PutUint16(b[2:4], checksum)

	return b, nil
}

// UnmarshalBinary decodes bytes into an ICMP packet
func (p *ICMPPacket) UnmarshalBinary(b []byte) error {
	if len(b) < 8 {
		return nil
	}

	p.Header.Type = b[0]
	p.Header.Code = b[1]
	p.Header.Checksum = binary.BigEndian.Uint16(b[2:4])
	p.Header.ID = binary.BigEndian.Uint16(b[4:6])
	p.Header.Sequence = binary.BigEndian.Uint16(b[6:8])

	if len(b) > 8 {
		p.Data = make([]byte, len(b)-8)
		copy(p.Data, b[8:])
	}

	return nil
}

// calculateChecksum computes the ICMP checksum
func calculateChecksum(data []byte) uint16 {
	sum := uint32(0)

	for i := 0; i < len(data)-1; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
	}

	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}

	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}

	return ^uint16(sum)
}

// CreateEchoRequest creates an ICMP Echo Request packet
func CreateEchoRequest(id, seq uint16) *ICMPPacket {
	// Add timestamp as data
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, uint64(time.Now().UnixNano()))

	return &ICMPPacket{
		Header: ICMPHeader{
			Type:     ICMPTypeEchoRequest,
			Code:     0,
			ID:       id,
			Sequence: seq,
		},
		Data: data,
	}
}

// ParseIPFromICMP extracts the source IP from an ICMP response
func ParseIPFromICMP(buf []byte) net.IP {
	if len(buf) < 20 {
		return nil
	}

	// IPv4 header: bytes 12-15 contain source IP
	return net.IPv4(buf[12], buf[13], buf[14], buf[15])
}
