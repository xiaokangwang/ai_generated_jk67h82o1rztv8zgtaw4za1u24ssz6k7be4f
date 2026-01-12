package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

func main() {
	// CLI flags
	ipRange := flag.String("range", "", "IP range to scan (e.g., 8.8.8.0/24, 1.1.1.1-1.1.1.10, or 8.8.8.8)")
	output := flag.String("output", "traceroute_results.jsonl", "Output file for results")
	workers := flag.Int("workers", 10, "Number of concurrent workers")
	maxHops := flag.Int("max-hops", 30, "Maximum number of hops")
	timeout := flag.Duration("timeout", 3*time.Second, "Timeout per hop")
	mode := flag.String("mode", "external", "Traceroute mode: 'raw' (requires root) or 'external' (uses system traceroute)")
	shuffle := flag.Bool("shuffle", true, "Shuffle IP order to avoid obvious scanning patterns (recommended for stealth)")
	flag.Parse()

	if *ipRange == "" {
		fmt.Println("Usage: ./traceroute-scanner -range <IP_RANGE> [OPTIONS]")
		fmt.Println("\nExamples:")
		fmt.Println("  # Using external traceroute (no root required):")
		fmt.Println("  ./traceroute-scanner -range 8.8.8.8 -mode external")
		fmt.Println("  ./traceroute-scanner -range 8.8.8.0/24 -mode external")
		fmt.Println("")
		fmt.Println("  # Using raw ICMP (requires root):")
		fmt.Println("  sudo ./traceroute-scanner -range 8.8.8.8 -mode raw")
		fmt.Println("  sudo ./traceroute-scanner -range 1.1.1.1-1.1.1.10 -mode raw")
		fmt.Println("\nOptions:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Validate mode
	if *mode != "raw" && *mode != "external" {
		log.Fatalf("Invalid mode '%s'. Must be 'raw' or 'external'", *mode)
	}

	// Check for traceroute binary if external mode
	if *mode == "external" {
		if !CheckTracerouteBinary() {
			log.Fatalf("External traceroute mode requires 'traceroute' command to be installed")
		}
	}

	// Parse IP range
	ips, err := ParseIPRange(*ipRange)
	if err != nil {
		log.Fatalf("Error parsing IP range: %v", err)
	}

	// Shuffle IPs to avoid obvious scanning patterns
	if *shuffle {
		ShuffleIPs(ips)
		fmt.Printf("Starting traceroute scan for %d IP addresses (shuffled)...\n", len(ips))
	} else {
		fmt.Printf("Starting traceroute scan for %d IP addresses (sequential)...\n", len(ips))
	}
	fmt.Printf("Mode: %s, Workers: %d, Max Hops: %d, Timeout: %v\n", *mode, *workers, *maxHops, *timeout)
	fmt.Printf("Output: %s\n\n", *output)

	// Open output file
	outFile, err := os.Create(*output)
	if err != nil {
		log.Fatalf("Error creating output file: %v", err)
	}
	defer outFile.Close()

	encoder := json.NewEncoder(outFile)

	// Create result channel
	results := make(chan *TraceResult, 100)

	// Start result writer
	done := make(chan bool)
	go func() {
		count := 0
		for result := range results {
			if err := encoder.Encode(result); err != nil {
				log.Printf("Error writing result: %v", err)
			}
			count++
			if count%10 == 0 {
				fmt.Printf("Processed %d/%d addresses...\n", count, len(ips))
			}
		}
		done <- true
	}()

	// Create worker pool
	pool := NewWorkerPool(*workers, *maxHops, *timeout, *mode)
	pool.Start()

	// Submit all IPs
	for _, ip := range ips {
		pool.Submit(ip, results)
	}

	// Wait for completion
	pool.Wait()
	close(results)
	<-done

	fmt.Printf("\nScan complete! Results saved to %s\n", *output)
}
