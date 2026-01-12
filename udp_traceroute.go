package main

import (
	"fmt"
	"net"
	"syscall"
	"time"
)

// UDPTraceroute performs a traceroute using UDP packets (like standard traceroute)
func UDPTraceroute(destIP net.IP, maxHops int, timeout time.Duration) (*TraceResult, error) {
	startTime := time.Now()
	result := &TraceResult{
		DestIP:    destIP.String(),
		Hops:      make([]Hop, 0),
		Reached:   false,
		Timestamp: startTime,
	}

	// Create UDP socket for sending
	sendFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		return nil, fmt.Errorf("failed to create UDP socket: %v", err)
	}
	defer syscall.Close(sendFd)

	// Create ICMP socket for receiving responses
	recvFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_ICMP)
	if err != nil {
		syscall.Close(sendFd)
		return nil, fmt.Errorf("failed to create ICMP socket (requires root): %v", err)
	}
	defer syscall.Close(recvFd)

	// Set receive timeout
	tv := syscall.Timeval{
		Sec:  int64(timeout / time.Second),
		Usec: int64((timeout % time.Second) / time.Microsecond),
	}
	if err := syscall.SetsockoptTimeval(recvFd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return nil, fmt.Errorf("failed to set timeout: %v", err)
	}

	destIP4 := destIP.To4()
	if destIP4 == nil {
		return nil, fmt.Errorf("not an IPv4 address")
	}

	// Base destination port (will increment)
	basePort := 33434

	for ttl := 1; ttl <= maxHops; ttl++ {
		hop := Hop{
			TTL:     ttl,
			Timeout: true,
		}

		// Set TTL on UDP socket
		if err := syscall.SetsockoptInt(sendFd, syscall.IPPROTO_IP, syscall.IP_TTL, ttl); err != nil {
			return nil, fmt.Errorf("failed to set TTL: %v", err)
		}

		// Destination address (UDP port increments with TTL)
		destAddr := syscall.SockaddrInet4{
			Port: basePort + ttl,
		}
		copy(destAddr.Addr[:], destIP4)

		// Send UDP packet
		sendTime := time.Now()
		payload := []byte("TRACEROUTE")

		if err := syscall.Sendto(sendFd, payload, 0, &destAddr); err != nil {
			// If send fails, record timeout and continue
			result.Hops = append(result.Hops, hop)
			continue
		}

		// Wait for ICMP response
		buf := make([]byte, 1500)
		gotResponse := false

		for {
			n, from, err := syscall.Recvfrom(recvFd, buf, 0)
			if err != nil {
				// Timeout or error
				break
			}

			rtt := time.Since(sendTime)

			// Extract source IP
			var srcIP net.IP
			if from != nil {
				switch sa := from.(type) {
				case *syscall.SockaddrInet4:
					srcIP = net.IPv4(sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3])
				}
			}

			// Parse ICMP response
			respPacket, responseIP, err := ParseICMPResponse(buf, n)
			if err != nil {
				if time.Since(sendTime) < timeout {
					continue
				}
				break
			}

			// Verify this ICMP response is for our UDP packet
			isOurPacket := false

			switch respPacket.Header.Type {
			case ICMPTypeTimeExceeded, ICMPTypeDestUnreach:
				// ICMP error messages contain the original packet
				// respPacket.Data contains: [Embedded IP header 20 bytes] [Embedded UDP header 8 bytes]
				// (The ICMP header has already been stripped by ParseICMPResponse)
				if len(respPacket.Data) >= 28 {
					// Embedded IP header starts at byte 0 (not byte 8!)
					embeddedIPStart := 0

					// Check if embedded packet is UDP (protocol 17 at byte 9 of IP header)
					if len(respPacket.Data) >= 20 {
						embeddedProtocol := respPacket.Data[embeddedIPStart+9]

						if embeddedProtocol == 17 { // UDP
							// Extract destination port from embedded UDP header
							// UDP header starts after 20-byte IP header
							udpHeaderStart := 20
							if len(respPacket.Data) >= udpHeaderStart+4 {
								// Destination port is bytes 2-3 of UDP header
								embeddedDestPort := int(respPacket.Data[udpHeaderStart+2])<<8 | int(respPacket.Data[udpHeaderStart+3])

								// Verify it matches our sent port
								if embeddedDestPort == basePort+ttl {
									isOurPacket = true
								}
							}
						}
					}
				}
			case ICMPTypeEchoReply:
				// Echo replies are not expected for UDP traceroute, but accept them
				isOurPacket = true
			}

			if !isOurPacket {
				// Not our packet, keep waiting
				if time.Since(sendTime) < timeout {
					continue
				}
				break
			}

			// We got a valid ICMP response for our packet
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
			case ICMPTypeTimeExceeded:
				// Intermediate hop (TTL expired)
				result.Hops = append(result.Hops, hop)
				break

			case ICMPTypeDestUnreach:
				// Destination reached (port unreachable means host received our UDP packet)
				if respPacket.Header.Code == 3 { // Port unreachable
					result.Reached = true
				}
				result.Hops = append(result.Hops, hop)
				result.Duration = time.Since(startTime)
				return result, nil

			case ICMPTypeEchoReply:
				// Not expected for UDP traceroute, but handle it
				result.Hops = append(result.Hops, hop)
				break
			}

			break
		}

		// Append hop if we got timeout and didn't already append
		if !gotResponse {
			result.Hops = append(result.Hops, hop)
		}

		// If we reached the destination, stop
		if result.Reached {
			break
		}

		// Small delay between hops
		time.Sleep(10 * time.Millisecond)
	}

	result.Duration = time.Since(startTime)
	return result, nil
}
