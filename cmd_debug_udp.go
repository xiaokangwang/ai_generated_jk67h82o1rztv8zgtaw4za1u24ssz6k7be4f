// +build ignore

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
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
	fmt.Println("UDP Traceroute Debug Tool")
	fmt.Println("===========================================")
	fmt.Println()

	result, err := UDPTraceroute(ip, *maxHops, *timeout)
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
