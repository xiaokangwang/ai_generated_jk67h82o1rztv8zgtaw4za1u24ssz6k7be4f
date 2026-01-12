package main

import (
	"fmt"
	"net"
	"syscall"
	"time"
)

// RawSocket represents a raw ICMP socket
type RawSocket struct {
	sendFd int    // Socket for sending (IPPROTO_RAW with custom IP header)
	recvFd int    // Socket for receiving (IPPROTO_ICMP)
	addr   syscall.SockaddrInet4
	ttl    int
	srcIP  net.IP
}

// NewRawSocket creates a new raw ICMP socket
func NewRawSocket() (*RawSocket, error) {
	// Create send socket with IPPROTO_RAW for full IP header control
	sendFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		return nil, fmt.Errorf("failed to create send socket (requires root): %v", err)
	}

	// Enable IP_HDRINCL for send socket
	if err := syscall.SetsockoptInt(sendFd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1); err != nil {
		syscall.Close(sendFd)
		return nil, fmt.Errorf("failed to set IP_HDRINCL: %v", err)
	}

	// Create receive socket with IPPROTO_ICMP
	recvFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_ICMP)
	if err != nil {
		syscall.Close(sendFd)
		return nil, fmt.Errorf("failed to create recv socket (requires root): %v", err)
	}

	// Get local IP address for source
	srcIP := net.IPv4(0, 0, 0, 0) // Kernel will fill this in

	return &RawSocket{sendFd: sendFd, recvFd: recvFd, ttl: 64, srcIP: srcIP}, nil
}

// SetTTL sets the Time To Live for outgoing packets
func (s *RawSocket) SetTTL(ttl int) error {
	s.ttl = ttl
	return nil
}

// SetReadTimeout sets the read timeout for the socket
func (s *RawSocket) SetReadTimeout(timeout time.Duration) error {
	tv := syscall.Timeval{
		Sec:  int64(timeout / time.Second),
		Usec: int64((timeout % time.Second) / time.Microsecond),
	}
	return syscall.SetsockoptTimeval(s.recvFd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
}

// SendTo sends an ICMP packet to the specified destination
func (s *RawSocket) SendTo(packet *ICMPPacket, destIP net.IP) error {
	icmpData, err := packet.MarshalBinary()
	if err != nil {
		return err
	}

	destIP4 := destIP.To4()
	if destIP4 == nil {
		return fmt.Errorf("not an IPv4 address")
	}

	// Build IP header
	ipHeader := make([]byte, 20)
	ipHeader[0] = 0x45                      // Version 4, Header length 5 (20 bytes)
	ipHeader[1] = 0                         // TOS
	totalLen := 20 + len(icmpData)
	ipHeader[2] = byte(totalLen >> 8)       // Total length (high byte)
	ipHeader[3] = byte(totalLen)            // Total length (low byte)
	ipHeader[4] = 0                         // ID (high)
	ipHeader[5] = 0                         // ID (low)
	ipHeader[6] = 0                         // Flags and fragment offset (high)
	ipHeader[7] = 0                         // Fragment offset (low)
	ipHeader[8] = byte(s.ttl)               // TTL - THIS IS THE KEY!
	ipHeader[9] = 1                         // Protocol (ICMP)
	ipHeader[10] = 0                        // Checksum (high) - will calculate
	ipHeader[11] = 0                        // Checksum (low)
	copy(ipHeader[12:16], s.srcIP.To4())    // Source IP
	copy(ipHeader[16:20], destIP4)          // Destination IP

	// Calculate IP header checksum
	checksum := uint32(0)
	for i := 0; i < 20; i += 2 {
		checksum += uint32(ipHeader[i])<<8 | uint32(ipHeader[i+1])
	}
	for checksum > 0xffff {
		checksum = (checksum >> 16) + (checksum & 0xffff)
	}
	checksum = ^checksum
	ipHeader[10] = byte(checksum >> 8)
	ipHeader[11] = byte(checksum)

	// Combine IP header + ICMP data
	fullPacket := append(ipHeader, icmpData...)

	// DEBUG: Print the IP header to verify TTL
	if false { // Set to true for debugging
		fmt.Printf("DEBUG: IP Header bytes: % x\n", ipHeader)
		fmt.Printf("DEBUG: TTL at byte 8: %d\n", ipHeader[8])
		fmt.Printf("DEBUG: Protocol at byte 9: %d\n", ipHeader[9])
		fmt.Printf("DEBUG: Dest IP: %d.%d.%d.%d\n", ipHeader[16], ipHeader[17], ipHeader[18], ipHeader[19])
	}

	// Convert IP to sockaddr
	var addr syscall.SockaddrInet4
	copy(addr.Addr[:], destIP4)

	return syscall.Sendto(s.sendFd, fullPacket, 0, &addr)
}

// RecvFrom receives data from the socket
func (s *RawSocket) RecvFrom(buf []byte) (int, net.IP, error) {
	n, from, err := syscall.Recvfrom(s.recvFd, buf, 0)
	if err != nil {
		return 0, nil, err
	}

	var srcIP net.IP
	if from != nil {
		switch sa := from.(type) {
		case *syscall.SockaddrInet4:
			srcIP = net.IPv4(sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3])
		}
	}

	return n, srcIP, nil
}

// Close closes the socket
func (s *RawSocket) Close() error {
	syscall.Close(s.sendFd)
	return syscall.Close(s.recvFd)
}

// ParseICMPResponse parses an ICMP response from raw bytes
// The buffer contains IP header + ICMP packet
func ParseICMPResponse(buf []byte, n int) (*ICMPPacket, net.IP, error) {
	if n < 20 {
		return nil, nil, fmt.Errorf("packet too short")
	}

	// Extract IP header length (first 4 bits of second byte * 4)
	ipHeaderLen := int(buf[0]&0x0f) * 4
	if n < ipHeaderLen {
		return nil, nil, fmt.Errorf("invalid IP header length")
	}

	// Extract source IP from IP header
	srcIP := net.IPv4(buf[12], buf[13], buf[14], buf[15])

	// Parse ICMP packet
	if n < ipHeaderLen+8 {
		return nil, srcIP, fmt.Errorf("ICMP packet too short")
	}

	icmpData := buf[ipHeaderLen:n]
	packet := &ICMPPacket{}
	if err := packet.UnmarshalBinary(icmpData); err != nil {
		return nil, srcIP, err
	}

	return packet, srcIP, nil
}
