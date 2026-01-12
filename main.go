package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
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
	shuffleSeed := flag.Uint64("shuffle-seed", 0, "Seed for shuffle permutation (0 = random, same seed = same order)")
	onePer24 := flag.Bool("one-per-24", false, "Sample only one IP per /24 block (reduces scan size by 256x)")
	pingOnly := flag.Bool("ping-only", false, "Use ICMP ping instead of traceroute (much faster, checks reachability only)")
	fresh := flag.Bool("fresh", false, "Start a fresh scan (default: auto-resume if progress file exists)")
	archive := flag.Bool("archive", false, "If previous scan is complete, archive it with timestamp and start fresh")
	streaming := flag.Bool("streaming", false, "Use streaming mode for large ranges (memory efficient, supports shuffle)")
	flag.Parse()

	if *ipRange == "" {
		fmt.Println("Usage: ./traceroute-scanner -range <IP_RANGE> [OPTIONS]")
		fmt.Println("\nExamples:")
		fmt.Println("  # Small ranges (< 10M IPs, loads in memory):")
		fmt.Println("  ./traceroute-scanner -range 8.8.8.8 -mode external")
		fmt.Println("  sudo ./traceroute-scanner -range 192.168.0.0/16 -mode raw")
		fmt.Println("")
		fmt.Println("  # Large ranges (> 10M IPs, auto-streams):")
		fmt.Println("  sudo ./traceroute-scanner -range 10.0.0.0/8 -mode raw")
		fmt.Println("")
		fmt.Println("  # Force streaming mode (memory efficient):")
		fmt.Println("  sudo ./traceroute-scanner -range 192.168.0.0/16 -mode raw -streaming")
		fmt.Println("")
		fmt.Println("  # All IPv4 addresses (4.3 billion IPs!):")
		fmt.Println("  sudo ./traceroute-scanner -range 0.0.0.0/0 -mode raw -streaming -workers 1000")
		fmt.Println("\nOptions:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Validate mode
	if *mode != "raw" && *mode != "external" {
		log.Fatalf("Invalid mode '%s'. Must be 'raw' or 'external'", *mode)
	}

	// Check for required binaries
	if *mode == "external" && !*pingOnly {
		if !CheckTracerouteBinary() {
			log.Fatalf("External traceroute mode requires 'traceroute' command to be installed")
		}
	}
	if *pingOnly {
		if !CheckPingBinary() {
			log.Fatalf("Ping-only mode requires 'ping' command to be installed")
		}
	}

	// Check if streaming mode should be used
	if *streaming {
		// Use streaming mode - delegate to streaming function
		mainStreamingMode(ipRange, output, workers, maxHops, timeout, mode, shuffle, shuffleSeed, onePer24, pingOnly, fresh, archive)
		return
	}

	// Auto-detect if range is too large (> 10 million IPs)
	iterator, err := NewIPRangeIteratorFromString(*ipRange)
	if err == nil && iterator.Total > 10000000 {
		fmt.Printf("⚠️  Large IP range detected (%d IPs > 10M limit)\n", iterator.Total)
		fmt.Printf("Switching to streaming mode for memory efficiency...\n\n")
		mainStreamingMode(ipRange, output, workers, maxHops, timeout, mode, shuffle, shuffleSeed, onePer24, pingOnly, fresh, archive)
		return
	}

	// Parse IP range (memory mode for small ranges)
	ips, err := ParseIPRange(*ipRange)
	if err != nil {
		log.Fatalf("Error parsing IP range: %v", err)
	}

	// Create checkpoint manager
	checkpoint, err := NewCheckpoint(*output)
	if err != nil {
		log.Fatalf("Error creating checkpoint: %v", err)
	}
	defer checkpoint.Close()

	// Check if progress file exists
	_, progressExists := os.Stat(checkpoint.filename)
	hasProgress := progressExists == nil

	// Check if output file exists (completed scan with no progress file)
	_, outputExists := os.Stat(*output)
	hasOutput := outputExists == nil

	// Determine mode: resume, fresh, or archive
	var openMode int
	var isResuming bool

	if *fresh {
		// User explicitly wants fresh start
		if hasProgress {
			fmt.Printf("Deleting existing progress file: %s\n", checkpoint.filename)
			checkpoint.DeleteCheckpoint()
			if _, err := os.Stat(*output); err == nil {
				fmt.Printf("Deleting existing output file: %s\n", *output)
				os.Remove(*output)
			}
		}
		isResuming = false
		openMode = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	} else if *archive && (hasProgress || hasOutput) {
		// Archive mode: check if scan is complete
		if hasProgress {
			// Has progress file - check if scan is complete
			fmt.Printf("Checking if previous scan is complete...\n")

			// Load progress to check completion
			_, err := checkpoint.LoadProgress()
			if err != nil {
				log.Fatalf("Error loading checkpoint: %v", err)
			}

			// Filter to see if any IPs remain
			originalCount := len(ips)
			remainingIPs := checkpoint.FilterCompleted(ips)

			if len(remainingIPs) == 0 {
				// Scan is complete - archive it!
				timestamp := time.Now().Format("2006-01-02_15-04-05")
				archiveName := fmt.Sprintf("%s.%s", *output, timestamp)

				fmt.Printf("Previous scan is complete! Archiving...\n")
				fmt.Printf("  Old file: %s\n", *output)
				fmt.Printf("  New file: %s\n", archiveName)

				// Rename output file with timestamp
				if _, err := os.Stat(*output); err == nil {
					if err := os.Rename(*output, archiveName); err != nil {
						log.Fatalf("Error archiving output file: %v", err)
					}
					fmt.Printf("✓ Archived to: %s\n", archiveName)
				}

				// Delete progress file
				checkpoint.DeleteCheckpoint()
				fmt.Printf("✓ Deleted progress file\n\n")

				// Start fresh scan
				fmt.Printf("Starting fresh scan...\n")
				isResuming = false
				openMode = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
			} else {
				// Scan not complete - resume normally
				fmt.Printf("Previous scan not complete (%d/%d IPs remaining)\n", len(remainingIPs), originalCount)
				fmt.Printf("Resuming scan...\n\n")

				ips = remainingIPs
				isResuming = true
				openMode = os.O_APPEND | os.O_WRONLY
			}
		} else if hasOutput {
			// Has output file but no progress file = completed scan
			// Archive it and start fresh
			timestamp := time.Now().Format("2006-01-02_15-04-05")
			archiveName := fmt.Sprintf("%s.%s", *output, timestamp)

			fmt.Printf("Found completed scan! Archiving...\n")
			fmt.Printf("  Old file: %s\n", *output)
			fmt.Printf("  New file: %s\n", archiveName)

			// Rename output file with timestamp
			if err := os.Rename(*output, archiveName); err != nil {
				log.Fatalf("Error archiving output file: %v", err)
			}
			fmt.Printf("✓ Archived to: %s\n\n", archiveName)

			// Start fresh scan
			fmt.Printf("Starting fresh scan...\n")
			isResuming = false
			openMode = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
		}
	} else if hasProgress {
		// Progress file exists, auto-resume
		fmt.Printf("Found existing progress file, resuming scan...\n")

		// Try to load existing progress
		_, err := checkpoint.LoadProgress()
		if err != nil {
			log.Fatalf("Error loading checkpoint: %v", err)
		}

		// Filter out already-completed IPs
		originalCount := len(ips)
		ips = checkpoint.FilterCompleted(ips)

		if len(ips) == 0 {
			fmt.Println("\nAll IPs already scanned! Nothing to do.")
			fmt.Printf("Results are in: %s\n", *output)
			fmt.Printf("\nTo start a fresh scan, use: -fresh flag\n")
			fmt.Printf("To archive and start new scan, use: -archive flag\n")
			// Delete checkpoint on complete resume
			checkpoint.DeleteCheckpoint()
			return
		}

		fmt.Printf("Resuming scan: %d/%d IPs remaining\n\n", len(ips), originalCount)
		isResuming = true
		openMode = os.O_APPEND | os.O_WRONLY
	} else {
		// No progress file, start new scan
		if *archive {
			fmt.Printf("No previous scan found, starting new scan...\n")
		}
		isResuming = false
		openMode = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	}

	// Initialize checkpoint for new scans
	if !isResuming {
		if err := checkpoint.InitProgress(len(ips)); err != nil {
			log.Fatalf("Error initializing checkpoint: %v", err)
		}
	}

	// Shuffle IPs to avoid obvious scanning patterns (only if not resuming)
	if *shuffle && !isResuming {
		ShuffleIPs(ips)
		fmt.Printf("Starting traceroute scan for %d IP addresses (shuffled)...\n", len(ips))
	} else if !isResuming {
		fmt.Printf("Starting traceroute scan for %d IP addresses (sequential)...\n", len(ips))
	}
	fmt.Printf("Mode: %s, Workers: %d, Max Hops: %d, Timeout: %v\n", *mode, *workers, *maxHops, *timeout)
	fmt.Printf("Output: %s\n", *output)
	fmt.Printf("Progress: %s\n\n", checkpoint.filename)

	// Open output file
	outFile, err := os.OpenFile(*output, openMode, 0644)
	if err != nil {
		log.Fatalf("Error opening output file: %v", err)
	}
	defer outFile.Close()

	encoder := json.NewEncoder(outFile)

	// Create result channel
	results := make(chan *TraceResult, 100)

	// Start result writer with checkpoint updates
	done := make(chan bool)
	totalIPs := checkpoint.totalIPs
	go func() {
		for result := range results {
			// Write result
			if err := encoder.Encode(result); err != nil {
				log.Printf("Error writing result: %v", err)
			}

			// Update checkpoint
			ip := net.ParseIP(result.DestIP)
			if ip != nil {
				if err := checkpoint.MarkCompleted(ip); err != nil {
					log.Printf("Error updating checkpoint: %v", err)
				}
			}

			// Progress update
			scanned, total, pct := checkpoint.GetProgress()
			if scanned%10 == 0 || scanned == total {
				fmt.Printf("Progress: %d/%d (%.1f%%)\n", scanned, total, pct)
			}
		}
		done <- true
	}()

	// Create worker pool
	pool := NewWorkerPool(*workers, *maxHops, *timeout, *mode, *pingOnly)
	pool.Start()

	// Submit all IPs
	for _, ip := range ips {
		pool.Submit(ip, results)
	}

	// Wait for completion
	pool.Wait()
	close(results)
	<-done

	// Final save
	if err := checkpoint.Close(); err != nil {
		log.Printf("Warning: Error saving final checkpoint: %v", err)
	}

	scanned, total, pct := checkpoint.GetProgress()
	fmt.Printf("\nScan complete! %d/%d IPs scanned (%.1f%%)\n", scanned, total, pct)
	fmt.Printf("Results saved to %s\n", *output)

	// Delete checkpoint file on successful completion
	if scanned == totalIPs {
		if err := checkpoint.DeleteCheckpoint(); err != nil {
			log.Printf("Warning: Could not delete checkpoint file: %v", err)
		} else {
			fmt.Printf("Checkpoint deleted (scan complete)\n")
		}
	}
}
