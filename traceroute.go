package main

import (
	"fmt"
	"net"
	"syscall"
	"time"
)

// Hop represents a single hop in the traceroute
type Hop struct {
	TTL      int           `json:"ttl"`
	IP       string        `json:"ip"`
	RTT      time.Duration `json:"rtt_ns"`
	Timeout  bool          `json:"timeout"`
}

// TraceResult represents the complete traceroute result for an IP
type TraceResult struct {
	DestIP    string        `json:"dest_ip"`
	Hops      []Hop         `json:"hops"`
	Reached   bool          `json:"reached"`
	Timestamp time.Time     `json:"timestamp"`
	Duration  time.Duration `json:"duration_ns"`
}

// Traceroute performs a traceroute to the specified destination using UDP (like standard traceroute)
func Traceroute(destIP net.IP, maxHops int, timeout time.Duration) (*TraceResult, error) {
	// Use UDP-based traceroute (works better with NAT)
	return UDPTraceroute(destIP, maxHops, timeout)
}

// TracerouteICMP performs a traceroute using ICMP (kept for reference, but doesn't work in NAT VMs)
func TracerouteICMP(destIP net.IP, maxHops int, timeout time.Duration) (*TraceResult, error) {
	startTime := time.Now()
	result := &TraceResult{
		DestIP:    destIP.String(),
		Hops:      make([]Hop, 0),
		Reached:   false,
		Timestamp: startTime,
	}

	// Create socket
	sock, err := NewRawSocket()
	if err != nil {
		return nil, err
	}
	defer sock.Close()

	// Set read timeout
	if err := sock.SetReadTimeout(timeout); err != nil {
		return nil, err
	}

	// Use process ID as ICMP ID
	icmpID := uint16(syscall.Getpid() & 0xFFFF)

	for ttl := 1; ttl <= maxHops; ttl++ {
		hop := Hop{
			TTL:     ttl,
			Timeout: true,
		}

		// Set TTL for this hop
		if err := sock.SetTTL(ttl); err != nil {
			return nil, fmt.Errorf("failed to set TTL: %v", err)
		}

		// Send ICMP Echo Request
		packet := CreateEchoRequest(icmpID, uint16(ttl))
		sendTime := time.Now()

		if err := sock.SendTo(packet, destIP); err != nil {
			// If send fails, record timeout and continue
			result.Hops = append(result.Hops, hop)
			continue
		}

		// Wait for response
		buf := make([]byte, 1500)
		gotResponse := false

		for {
			n, srcIP, err := sock.RecvFrom(buf)
			if err != nil {
				// Timeout or error - record as timeout
				break
			}

			rtt := time.Since(sendTime)

			// Parse ICMP response
			respPacket, responseIP, err := ParseICMPResponse(buf, n)
			if err != nil {
				continue
			}

			// Verify this response is for our packet
			isOurPacket := false

			switch respPacket.Header.Type {
			case ICMPTypeEchoReply:
				// For Echo Reply, check the ID directly
				if respPacket.Header.ID == icmpID && respPacket.Header.Sequence == uint16(ttl) {
					isOurPacket = true
				}

			case ICMPTypeTimeExceeded, ICMPTypeDestUnreach:
				// For Time Exceeded and Dest Unreachable, the original packet is embedded in the data
				// The embedded packet starts at byte 8 of the ICMP payload (after ICMP header)
				// Format: [4 bytes unused] [4 bytes unused] [IP header] [ICMP header with our ID]
				if len(respPacket.Data) >= 28 {
					// Skip 8 bytes of ICMP Time Exceeded header data
					// Then skip 20 bytes of embedded IP header
					// Now we're at the embedded ICMP header
					embeddedICMPStart := 28
					if len(respPacket.Data) >= embeddedICMPStart+8 {
						// Extract ID from embedded ICMP packet (bytes 4-5 of ICMP header)
						embeddedID := uint16(respPacket.Data[embeddedICMPStart+4])<<8 | uint16(respPacket.Data[embeddedICMPStart+5])
						embeddedSeq := uint16(respPacket.Data[embeddedICMPStart+6])<<8 | uint16(respPacket.Data[embeddedICMPStart+7])

						if embeddedID == icmpID && embeddedSeq == uint16(ttl) {
							isOurPacket = true
						}
					}
				}
			}

			if !isOurPacket {
				// Not our packet, keep waiting
				if time.Since(sendTime) < timeout {
					continue
				}
				break
			}

			// We got a response for our packet
			gotResponse = true
			hop.Timeout = false
			hop.RTT = rtt

			if srcIP != nil {
				hop.IP = srcIP.String()
			} else if responseIP != nil {
				hop.IP = responseIP.String()
			}

			// Check response type
			switch respPacket.Header.Type {
			case ICMPTypeEchoReply:
				// Reached destination
				result.Reached = true
				result.Hops = append(result.Hops, hop)
				result.Duration = time.Since(startTime)
				return result, nil

			case ICMPTypeTimeExceeded:
				// Intermediate hop
				break

			case ICMPTypeDestUnreach:
				// Destination unreachable
				result.Hops = append(result.Hops, hop)
				result.Duration = time.Since(startTime)
				return result, nil
			}

			break
		}

		// Only append hop if we're recording it
		if gotResponse || hop.Timeout {
			result.Hops = append(result.Hops, hop)
		}

		// If we reached the destination, stop
		if result.Reached {
			break
		}

		// Small delay between hops to avoid overwhelming routers
		time.Sleep(10 * time.Millisecond)
	}

	result.Duration = time.Since(startTime)
	return result, nil
}
