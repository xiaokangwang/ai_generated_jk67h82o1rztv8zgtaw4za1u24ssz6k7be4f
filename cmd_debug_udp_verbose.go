// +build ignore

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"syscall"
	"time"
)

func main() {
	ipStr := flag.String("ip", "8.8.8.8", "IP to trace")
	maxHops := flag.Int("hops", 5, "Max hops")
	timeout := flag.Duration("timeout", 2*time.Second, "Timeout")
	flag.Parse()

	if os.Geteuid() != 0 {
		log.Fatal("This debug tool requires root privileges (use sudo)")
	}

	ip := net.ParseIP(*ipStr)
	if ip == nil {
		log.Fatalf("Invalid IP: %s", *ipStr)
	}

	fmt.Println("===========================================")
	fmt.Println("UDP Traceroute Debug Tool (VERBOSE)")
	fmt.Println("===========================================")
	fmt.Println()

	result, err := UDPTracerouteVerbose(ip, *maxHops, *timeout)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("\n=== Results ===\n")
	fmt.Printf("Destination: %s\n", result.DestIP)
	fmt.Printf("Reached: %v\n", result.Reached)
	fmt.Printf("Total hops: %d\n", len(result.Hops))
	fmt.Printf("Duration: %.2fs\n\n", result.Duration.Seconds())

	for _, hop := range result.Hops {
		status := "*"
		if !hop.Timeout {
			status = hop.IP
		}
		rtt := "-"
		if hop.RTT > 0 {
			rtt = fmt.Sprintf("%.2fms", float64(hop.RTT)/1e6)
		}
		fmt.Printf("  %2d. %-20s %s\n", hop.TTL, status, rtt)
	}
}

func UDPTracerouteVerbose(destIP net.IP, maxHops int, timeout time.Duration) (*TraceResult, error) {
	startTime := time.Now()
	result := &TraceResult{
		DestIP:    destIP.String(),
		Hops:      make([]Hop, 0),
		Reached:   false,
		Timestamp: startTime,
	}

	fmt.Printf("DEBUG: Creating sockets...\n")

	// Create UDP socket for sending
	sendFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		return nil, fmt.Errorf("failed to create UDP socket: %v", err)
	}
	defer syscall.Close(sendFd)
	fmt.Printf("DEBUG: UDP send socket created (fd=%d)\n", sendFd)

	// Create ICMP socket for receiving responses
	recvFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_ICMP)
	if err != nil {
		syscall.Close(sendFd)
		return nil, fmt.Errorf("failed to create ICMP socket (requires root): %v", err)
	}
	defer syscall.Close(recvFd)
	fmt.Printf("DEBUG: ICMP recv socket created (fd=%d)\n", recvFd)

	// Set receive timeout
	tv := syscall.Timeval{
		Sec:  int64(timeout / time.Second),
		Usec: int64((timeout % time.Second) / time.Microsecond),
	}
	if err := syscall.SetsockoptTimeval(recvFd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return nil, fmt.Errorf("failed to set timeout: %v", err)
	}
	fmt.Printf("DEBUG: Receive timeout set to %v\n", timeout)

	destIP4 := destIP.To4()
	if destIP4 == nil {
		return nil, fmt.Errorf("not an IPv4 address")
	}

	// Base destination port (will increment)
	basePort := 33434

	fmt.Printf("\nDEBUG: Starting traceroute to %s\n", destIP)
	fmt.Printf("DEBUG: Base port: %d\n\n", basePort)

	for ttl := 1; ttl <= maxHops; ttl++ {
		hop := Hop{
			TTL:     ttl,
			Timeout: true,
		}

		fmt.Printf("DEBUG: === Hop %d ===\n", ttl)

		// Set TTL on UDP socket
		if err := syscall.SetsockoptInt(sendFd, syscall.IPPROTO_IP, syscall.IP_TTL, ttl); err != nil {
			return nil, fmt.Errorf("failed to set TTL: %v", err)
		}
		fmt.Printf("DEBUG: TTL set to %d\n", ttl)

		// Destination address (UDP port increments with TTL)
		destAddr := syscall.SockaddrInet4{
			Port: basePort + ttl,
		}
		copy(destAddr.Addr[:], destIP4)

		fmt.Printf("DEBUG: Destination: %s:%d\n", destIP, basePort+ttl)

		// Send UDP packet
		sendTime := time.Now()
		payload := []byte("TRACEROUTE")

		fmt.Printf("DEBUG: Sending %d bytes...\n", len(payload))
		if err := syscall.Sendto(sendFd, payload, 0, &destAddr); err != nil {
			fmt.Printf("DEBUG: Send failed: %v\n", err)
			result.Hops = append(result.Hops, hop)
			continue
		}
		fmt.Printf("DEBUG: Packet sent successfully\n")

		// Wait for ICMP response
		buf := make([]byte, 1500)
		gotResponse := false

		fmt.Printf("DEBUG: Waiting for response (timeout: %v)...\n", timeout)

		for {
			n, from, err := syscall.Recvfrom(recvFd, buf, 0)
			if err != nil {
				// Check if it's a timeout
				if errno, ok := err.(syscall.Errno); ok {
					if errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK {
						fmt.Printf("DEBUG: Timeout - no response received\n")
					} else {
						fmt.Printf("DEBUG: Receive error: %v (errno=%d)\n", err, errno)
					}
				} else {
					fmt.Printf("DEBUG: Receive error: %v\n", err)
				}
				break
			}

			rtt := time.Since(sendTime)
			fmt.Printf("DEBUG: Received %d bytes from %v (RTT: %.2fms)\n", n, from, float64(rtt)/1e6)

			// Extract source IP
			var srcIP net.IP
			if from != nil {
				switch sa := from.(type) {
				case *syscall.SockaddrInet4:
					srcIP = net.IPv4(sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3])
					fmt.Printf("DEBUG: Source IP: %s\n", srcIP)
				}
			}

			// Print first 64 bytes of packet for inspection
			dumpLen := n
			if dumpLen > 64 {
				dumpLen = 64
			}
			fmt.Printf("DEBUG: Packet dump (first %d bytes): % x\n", dumpLen, buf[:dumpLen])

			// Parse ICMP response
			respPacket, responseIP, err := ParseICMPResponse(buf, n)
			if err != nil {
				fmt.Printf("DEBUG: Failed to parse ICMP: %v\n", err)
				if time.Since(sendTime) < timeout {
					fmt.Printf("DEBUG: Continue waiting...\n")
					continue
				}
				break
			}

			fmt.Printf("DEBUG: ICMP Type: %d, Code: %d\n", respPacket.Header.Type, respPacket.Header.Code)

			// Verify this ICMP response is for our UDP packet
			isOurPacket := false

			switch respPacket.Header.Type {
			case ICMPTypeTimeExceeded, ICMPTypeDestUnreach:
				fmt.Printf("DEBUG: Checking embedded packet...\n")
				fmt.Printf("DEBUG: respPacket.Data length: %d bytes\n", len(respPacket.Data))
				// ICMP error messages contain the original packet
				// respPacket.Data starts with the embedded IP header (ICMP header already stripped)
				if len(respPacket.Data) >= 28 {
					embeddedIPStart := 0  // Fixed: was 8, should be 0

					if len(respPacket.Data) >= 20 {
						embeddedProtocol := respPacket.Data[embeddedIPStart+9]
						fmt.Printf("DEBUG: Embedded protocol at byte 9: %d (should be 17 for UDP)\n", embeddedProtocol)

						if embeddedProtocol == 17 { // UDP
							udpHeaderStart := 20  // Fixed: was embeddedIPStart + 20
							if len(respPacket.Data) >= udpHeaderStart+4 {
								embeddedDestPort := int(respPacket.Data[udpHeaderStart+2])<<8 | int(respPacket.Data[udpHeaderStart+3])
								fmt.Printf("DEBUG: Embedded dest port: %d (should be %d)\n", embeddedDestPort, basePort+ttl)

								if embeddedDestPort == basePort+ttl {
									isOurPacket = true
									fmt.Printf("DEBUG: ✓ This is our packet!\n")
								} else {
									fmt.Printf("DEBUG: ✗ Port mismatch\n")
								}
							}
						} else {
							fmt.Printf("DEBUG: ✗ Not UDP (protocol %d)\n", embeddedProtocol)
						}
					}
				} else {
					fmt.Printf("DEBUG: ✗ Response data too short (%d bytes)\n", len(respPacket.Data))
				}
			case ICMPTypeEchoReply:
				fmt.Printf("DEBUG: Echo reply (unexpected for UDP traceroute)\n")
				isOurPacket = true
			default:
				fmt.Printf("DEBUG: Unknown ICMP type: %d\n", respPacket.Header.Type)
			}

			if !isOurPacket {
				fmt.Printf("DEBUG: Not our packet, keep waiting...\n")
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

			fmt.Printf("DEBUG: ✓ Valid response from %s\n", hop.IP)

			// Check response type
			switch respPacket.Header.Type {
			case ICMPTypeTimeExceeded:
				fmt.Printf("DEBUG: TTL expired at intermediate hop\n")
				result.Hops = append(result.Hops, hop)
				break

			case ICMPTypeDestUnreach:
				fmt.Printf("DEBUG: Destination unreachable (code %d)\n", respPacket.Header.Code)
				if respPacket.Header.Code == 3 { // Port unreachable
					result.Reached = true
					fmt.Printf("DEBUG: Destination reached!\n")
				}
				result.Hops = append(result.Hops, hop)
				result.Duration = time.Since(startTime)
				return result, nil

			case ICMPTypeEchoReply:
				result.Hops = append(result.Hops, hop)
				break
			}

			break
		}

		// Append hop if we got timeout and didn't already append
		if !gotResponse {
			fmt.Printf("DEBUG: Recording timeout hop\n")
			result.Hops = append(result.Hops, hop)
		}

		fmt.Printf("\n")

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
