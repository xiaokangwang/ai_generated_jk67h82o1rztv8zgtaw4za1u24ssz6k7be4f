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
	fmt.Println("Raw ICMP Traceroute Debug Tool")
	fmt.Println("===========================================")
	fmt.Println()

	if err := DebugTraceroute(ip, *maxHops, *timeout); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
