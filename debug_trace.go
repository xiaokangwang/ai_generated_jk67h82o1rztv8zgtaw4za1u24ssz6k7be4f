package main

import (
	"fmt"
	"net"
	"syscall"
	"time"
)

// DebugTraceroute performs a traceroute with debug output
func DebugTraceroute(destIP net.IP, maxHops int, timeout time.Duration) error {
	fmt.Printf("Starting debug traceroute to %s\n", destIP)
	fmt.Printf("Max hops: %d, Timeout: %v\n\n", maxHops, timeout)

	// Create socket
	sock, err := NewRawSocket()
	if err != nil {
		return err
	}
	defer sock.Close()

	// Set read timeout
	if err := sock.SetReadTimeout(timeout); err != nil {
		return err
	}

	// Use process ID as ICMP ID
	icmpID := uint16(syscall.Getpid() & 0xFFFF)
	fmt.Printf("Our ICMP ID: %d (0x%04x)\n\n", icmpID, icmpID)

	for ttl := 1; ttl <= maxHops; ttl++ {
		fmt.Printf("=== TTL %d ===\n", ttl)

		// Set TTL for this hop
		if err := sock.SetTTL(ttl); err != nil {
			return fmt.Errorf("failed to set TTL: %v", err)
		}

		// TTL is now stored in socket struct and embedded in IP header
		fmt.Printf("TTL will be set to: %d (in custom IP header)\n", ttl)

		// Send ICMP Echo Request
		packet := CreateEchoRequest(icmpID, uint16(ttl))
		fmt.Printf("Sending ICMP Echo Request: ID=%d, Seq=%d\n", packet.Header.ID, packet.Header.Sequence)

		sendTime := time.Now()
		if err := sock.SendTo(packet, destIP); err != nil {
			fmt.Printf("Send error: %v\n\n", err)
			continue
		}

		// Wait for response
		buf := make([]byte, 1500)
		responseCount := 0

		for {
			n, srcIP, err := sock.RecvFrom(buf)
			if err != nil {
				fmt.Printf("Recv timeout/error: %v\n", err)
				break
			}

			responseCount++
			elapsed := time.Since(sendTime)
			fmt.Printf("\nResponse #%d after %v:\n", responseCount, elapsed)
			fmt.Printf("  Source IP: %s\n", srcIP)
			fmt.Printf("  Packet size: %d bytes\n", n)

			// Parse ICMP response
			respPacket, responseIP, err := ParseICMPResponse(buf, n)
			if err != nil {
				fmt.Printf("  Parse error: %v\n", err)
				if time.Since(sendTime) < timeout {
					continue
				}
				break
			}

			fmt.Printf("  Response IP (from header): %s\n", responseIP)
			fmt.Printf("  ICMP Type: %d\n", respPacket.Header.Type)
			fmt.Printf("  ICMP Code: %d\n", respPacket.Header.Code)
			fmt.Printf("  ICMP ID: %d (0x%04x)\n", respPacket.Header.ID, respPacket.Header.ID)
			fmt.Printf("  ICMP Seq: %d\n", respPacket.Header.Sequence)
			fmt.Printf("  ICMP Data length: %d bytes\n", len(respPacket.Data))

			// Check if this is our packet
			switch respPacket.Header.Type {
			case ICMPTypeEchoReply:
				fmt.Printf("  Type: Echo Reply\n")
				if respPacket.Header.ID == icmpID && respPacket.Header.Sequence == uint16(ttl) {
					fmt.Printf("  ✓ This is OUR Echo Reply!\n")
					fmt.Printf("  ✓ Reached destination at TTL %d\n", ttl)
					return nil
				} else {
					fmt.Printf("  ✗ Not our packet (ID/Seq mismatch)\n")
					fmt.Printf("    Expected ID=%d, Seq=%d\n", icmpID, ttl)
				}

			case ICMPTypeTimeExceeded:
				fmt.Printf("  Type: Time Exceeded (from intermediate router)\n")
				if len(respPacket.Data) >= 28 {
					fmt.Printf("  Embedded packet data available (%d bytes)\n", len(respPacket.Data))
					embeddedICMPStart := 28
					if len(respPacket.Data) >= embeddedICMPStart+8 {
						embeddedID := uint16(respPacket.Data[embeddedICMPStart+4])<<8 |
							uint16(respPacket.Data[embeddedICMPStart+5])
						embeddedSeq := uint16(respPacket.Data[embeddedICMPStart+6])<<8 |
							uint16(respPacket.Data[embeddedICMPStart+7])

						fmt.Printf("  Embedded ICMP ID: %d (0x%04x)\n", embeddedID, embeddedID)
						fmt.Printf("  Embedded ICMP Seq: %d\n", embeddedSeq)

						if embeddedID == icmpID && embeddedSeq == uint16(ttl) {
							fmt.Printf("  ✓ This is OUR Time Exceeded message!\n")
							fmt.Printf("  ✓ Intermediate hop: %s\n", srcIP)
							break
						} else {
							fmt.Printf("  ✗ Not our packet (embedded ID/Seq mismatch)\n")
							fmt.Printf("    Expected ID=%d, Seq=%d\n", icmpID, ttl)
						}
					}
				} else {
					fmt.Printf("  ✗ Embedded packet data too short\n")
				}

			case ICMPTypeDestUnreach:
				fmt.Printf("  Type: Destination Unreachable\n")

			default:
				fmt.Printf("  Type: Unknown (%d)\n", respPacket.Header.Type)
			}

			if time.Since(sendTime) >= timeout {
				fmt.Printf("\nTimeout reached\n")
				break
			}

			// If we got a valid response for our packet, move to next TTL
			if respPacket.Header.Type == ICMPTypeTimeExceeded || respPacket.Header.Type == ICMPTypeEchoReply {
				break
			}
		}

		fmt.Printf("\n")

		if responseCount == 0 {
			fmt.Printf("No responses received\n\n")
		}
	}

	return nil
}
