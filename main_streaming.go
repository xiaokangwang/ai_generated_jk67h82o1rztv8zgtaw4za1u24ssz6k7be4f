package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"time"
)

func mainStreamingMode(ipRange, output *string, workers, maxHops *int, timeout *time.Duration, mode *string, shuffle *bool, shuffleSeed *uint64, fresh, archive *bool) {

	// Create IP range iterator
	iterator, err := NewIPRangeIteratorFromString(*ipRange)
	if err != nil {
		log.Fatalf("Error parsing IP range: %v", err)
	}

	fmt.Printf("IP Range: %s to %s (%d IPs)\n",
		Uint32ToIP(iterator.Start),
		Uint32ToIP(iterator.End),
		iterator.Total)

	// Create range-based checkpoint manager
	checkpoint, err := NewRangeCheckpoint(*output, iterator.Start, iterator.End)
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
	var excludeRanges []CompletedRange

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
		// Archive completed scans
		if hasProgress {
			// Check completion
			checkpoint.LoadProgress()
			remaining := checkpoint.GetRemainingCount()

			if remaining == 0 {
				// Complete - archive
				timestamp := time.Now().Format("2006-01-02_15-04-05")
				archiveName := fmt.Sprintf("%s.%s", *output, timestamp)

				fmt.Printf("Previous scan is complete! Archiving...\n")
				if _, err := os.Stat(*output); err == nil {
					os.Rename(*output, archiveName)
					fmt.Printf("✓ Archived to: %s\n\n", archiveName)
				}
				checkpoint.DeleteCheckpoint()

				isResuming = false
				openMode = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
			} else {
				// Incomplete - resume
				fmt.Printf("Previous scan not complete (%d IPs remaining)\n", remaining)
				fmt.Printf("Resuming scan...\n\n")
				isResuming = true
				excludeRanges = checkpoint.GetCompletedRanges()
				openMode = os.O_APPEND | os.O_WRONLY
			}
		} else if hasOutput {
			// Completed scan, no progress file
			timestamp := time.Now().Format("2006-01-02_15-04-05")
			archiveName := fmt.Sprintf("%s.%s", *output, timestamp)

			fmt.Printf("Found completed scan! Archiving...\n")
			os.Rename(*output, archiveName)
			fmt.Printf("✓ Archived to: %s\n\n", archiveName)

			isResuming = false
			openMode = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
		}
	} else if hasProgress {
		// Auto-resume
		fmt.Printf("Found existing progress file, resuming scan...\n")

		if err := checkpoint.LoadProgress(); err != nil {
			log.Fatalf("Error loading checkpoint: %v", err)
		}

		remaining := checkpoint.GetRemainingCount()

		if remaining == 0 {
			fmt.Println("\nAll IPs already scanned! Nothing to do.")
			fmt.Printf("Results are in: %s\n", *output)
			fmt.Printf("\nTo start a fresh scan, use: -fresh flag\n")
			fmt.Printf("To archive and start new scan, use: -archive flag\n")
			checkpoint.DeleteCheckpoint()
			return
		}

		fmt.Printf("Resuming scan: %d IPs remaining\n\n", remaining)
		isResuming = true
		excludeRanges = checkpoint.GetCompletedRanges()
		openMode = os.O_APPEND | os.O_WRONLY
	} else {
		// No progress file, start new scan
		isResuming = false
		openMode = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	}

	// Initialize checkpoint for new scans
	if !isResuming {
		if err := checkpoint.InitProgress(iterator.Total); err != nil {
			log.Fatalf("Error initializing checkpoint: %v", err)
		}
	}

	// Determine seed for shuffle
	seed := *shuffleSeed
	shuffleMode := "sequential"
	if *shuffle {
		if seed == 0 {
			// Generate random seed
			var randomBytes [8]byte
			if _, err := rand.Read(randomBytes[:]); err == nil {
				seed = binary.LittleEndian.Uint64(randomBytes[:])
			} else {
				seed = uint64(time.Now().UnixNano())
			}
		}
		shuffleMode = fmt.Sprintf("shuffled (seed: %d)", seed)
	}

	fmt.Printf("Mode: %s (streaming), Workers: %d, Max Hops: %d, Timeout: %v\n", *mode, *workers, *maxHops, *timeout)
	fmt.Printf("Scan order: %s\n", shuffleMode)
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
				ipInt := IPToUint32(ip)
				if err := checkpoint.MarkCompleted(ipInt); err != nil {
					log.Printf("Error updating checkpoint: %v", err)
				}
			}

			// Progress update
			scanned, total, pct := checkpoint.GetProgress()
			if scanned%100 == 0 || scanned == total {
				fmt.Printf("Progress: %d/%d (%.2f%%)\n", scanned, total, pct)
			}
		}
		done <- true
	}()

	// Generate IPs on demand (streaming)
	var ipChan <-chan net.IP
	if *shuffle {
		ipChan = iterator.GenerateShuffled(excludeRanges, seed)
	} else {
		ipChan = iterator.Generate(excludeRanges)
	}

	// Create worker pool
	pool := NewWorkerPool(*workers, *maxHops, *timeout, *mode)
	pool.Start()

	// Feed IPs to workers from generator
	fmt.Printf("Starting IP submission...\n")
	jobCount := uint64(0)
	for ip := range ipChan {
		if jobCount < 5 {
			fmt.Printf("Submitting job %d: %s\n", jobCount+1, ip.String())
		} else if jobCount == 5 {
			fmt.Printf("(further submissions will be logged every 100 jobs)\n")
		}
		pool.Submit(ip, results)
		jobCount++
		if jobCount > 5 && jobCount%100 == 0 {
			fmt.Printf("Submitted %d jobs...\n", jobCount)
		}
	}
	fmt.Printf("Total jobs submitted: %d\n", jobCount)

	// Wait for completion
	pool.Wait()
	close(results)
	<-done

	// Final save
	if err := checkpoint.Close(); err != nil {
		log.Printf("Warning: Error saving final checkpoint: %v", err)
	}

	scanned, total, pct := checkpoint.GetProgress()
	fmt.Printf("\nScan complete! %d/%d IPs scanned (%.2f%%)\n", scanned, total, pct)
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
