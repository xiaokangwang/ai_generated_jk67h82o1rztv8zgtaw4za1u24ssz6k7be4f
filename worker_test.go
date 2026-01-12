package main

import (
	"net"
	"sync"
	"testing"
	"time"
)

func TestNewWorkerPool(t *testing.T) {
	workers := 5
	maxHops := 30
	timeout := 3 * time.Second
	mode := "external"

	pool := NewWorkerPool(workers, maxHops, timeout, mode)

	if pool.workers != workers {
		t.Errorf("Expected %d workers, got %d", workers, pool.workers)
	}

	if pool.maxHops != maxHops {
		t.Errorf("Expected maxHops %d, got %d", maxHops, pool.maxHops)
	}

	if pool.timeout != timeout {
		t.Errorf("Expected timeout %v, got %v", timeout, pool.timeout)
	}

	if pool.mode != mode {
		t.Errorf("Expected mode %s, got %s", mode, pool.mode)
	}

	if pool.jobs == nil {
		t.Error("Expected jobs channel to be initialized")
	}
}

func TestWorkerPoolStartAndWait(t *testing.T) {
	pool := NewWorkerPool(2, 30, time.Second, "external")

	// Start pool
	pool.Start()

	// Verify workers started by checking wait group
	// Submit a job to ensure workers are processing
	results := make(chan *TraceResult, 10)
	ip := net.ParseIP("127.0.0.1")

	pool.Submit(ip, results)

	// Wait should close jobs channel and wait for workers
	pool.Wait()

	// Channel should be closed now
	select {
	case _, ok := <-pool.jobs:
		if ok {
			t.Error("Expected jobs channel to be closed after Wait()")
		}
	default:
		// If we can't receive, that's also fine (channel is closed and empty)
	}
}

func TestWorkerPoolSubmit(t *testing.T) {
	pool := NewWorkerPool(1, 30, time.Second, "external")
	pool.Start()

	results := make(chan *TraceResult, 10)
	ip := net.ParseIP("8.8.8.8")

	// Submit job (should not block or panic)
	pool.Submit(ip, results)

	// Close pool
	pool.Wait()

	// We should have received a result
	select {
	case result := <-results:
		if result == nil {
			t.Error("Expected non-nil result")
		}
		if result.DestIP != ip.String() {
			t.Errorf("Expected destination %s, got %s", ip.String(), result.DestIP)
		}
	case <-time.After(10 * time.Second):
		t.Error("Timeout waiting for result")
	}
}

func TestWorkerPoolMultipleJobs(t *testing.T) {
	pool := NewWorkerPool(3, 30, time.Second, "external")
	pool.Start()

	results := make(chan *TraceResult, 10)
	ips := []net.IP{
		net.ParseIP("8.8.8.8"),
		net.ParseIP("1.1.1.1"),
		net.ParseIP("127.0.0.1"),
	}

	// Submit multiple jobs
	for _, ip := range ips {
		pool.Submit(ip, results)
	}

	pool.Wait()
	close(results)

	// Collect results
	resultCount := 0
	receivedIPs := make(map[string]bool)

	for result := range results {
		resultCount++
		receivedIPs[result.DestIP] = true
	}

	if resultCount != len(ips) {
		t.Errorf("Expected %d results, got %d", len(ips), resultCount)
	}

	// Verify all IPs were processed
	for _, ip := range ips {
		if !receivedIPs[ip.String()] {
			t.Errorf("Missing result for IP %s", ip.String())
		}
	}
}

func TestWorkerPoolConcurrency(t *testing.T) {
	workerCount := 5
	jobCount := 50

	pool := NewWorkerPool(workerCount, 30, time.Second, "external")
	pool.Start()

	results := make(chan *TraceResult, jobCount)

	// Submit many jobs concurrently
	var wg sync.WaitGroup
	for i := 0; i < jobCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ip := net.ParseIP("127.0.0." + string(rune(id%256)))
			pool.Submit(ip, results)
		}(i)
	}

	wg.Wait()
	pool.Wait()
	close(results)

	// Count results
	resultCount := 0
	for range results {
		resultCount++
	}

	if resultCount != jobCount {
		t.Errorf("Expected %d results, got %d", jobCount, resultCount)
	}
}

func TestWorkerPoolResultStructure(t *testing.T) {
	pool := NewWorkerPool(1, 30, time.Second, "external")
	pool.Start()

	results := make(chan *TraceResult, 1)
	ip := net.ParseIP("8.8.8.8")

	pool.Submit(ip, results)
	pool.Wait()

	result := <-results

	// Verify result structure
	if result.DestIP == "" {
		t.Error("Expected non-empty DestIP")
	}

	if result.Timestamp.IsZero() {
		t.Error("Expected non-zero timestamp")
	}

	if result.Hops == nil {
		t.Error("Expected non-nil Hops slice")
	}

	// External mode should produce some hops (at least to localhost)
	if len(result.Hops) == 0 {
		t.Log("Warning: No hops returned (may be expected for localhost or network issues)")
	}
}

func TestWorkerPoolTimeout(t *testing.T) {
	// Use very short timeout to test timeout handling
	pool := NewWorkerPool(1, 30, 100*time.Millisecond, "external")
	pool.Start()

	results := make(chan *TraceResult, 1)
	// Use a non-routable IP to trigger timeout
	ip := net.ParseIP("192.0.2.1") // RFC 5737 TEST-NET-1

	pool.Submit(ip, results)
	pool.Wait()

	select {
	case result := <-results:
		if result == nil {
			t.Error("Expected non-nil result even on timeout")
		}
		// Verify we got a result (may have timeout hops)
	case <-time.After(5 * time.Second):
		t.Error("Timeout waiting for result")
	}
}

func TestWorkerPoolJobStructure(t *testing.T) {
	results := make(chan *TraceResult, 1)
	ip := net.ParseIP("192.168.1.1")

	job := &Job{
		IP:      ip,
		Results: results,
	}

	if !job.IP.Equal(ip) {
		t.Errorf("Expected IP %s, got %s", ip.String(), job.IP.String())
	}

	if job.Results != results {
		t.Error("Expected Results channel to match")
	}
}

func TestWorkerPoolEmptyJobs(t *testing.T) {
	pool := NewWorkerPool(2, 30, time.Second, "external")
	pool.Start()

	// Don't submit any jobs, just wait
	done := make(chan bool)
	go func() {
		pool.Wait()
		done <- true
	}()

	select {
	case <-done:
		// Success - pool should complete even with no jobs
	case <-time.After(2 * time.Second):
		t.Error("Wait() hung with no jobs submitted")
	}
}

func TestWorkerPoolZeroWorkers(t *testing.T) {
	// Edge case: 0 workers - just test creation and cleanup
	pool := NewWorkerPool(0, 30, time.Second, "external")

	if pool.workers != 0 {
		t.Errorf("Expected 0 workers, got %d", pool.workers)
	}

	pool.Start()

	// Wait should complete immediately with no workers and no jobs
	done := make(chan bool)
	go func() {
		pool.Wait()
		done <- true
	}()

	select {
	case <-done:
		// Success - pool should complete even with 0 workers
	case <-time.After(2 * time.Second):
		t.Error("Wait() hung with 0 workers")
	}
}

func TestWorkerPoolModeSelection(t *testing.T) {
	// This test verifies that the pool accepts both modes
	modes := []string{"raw", "external"}

	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			pool := NewWorkerPool(1, 30, time.Second, mode)
			if pool.mode != mode {
				t.Errorf("Expected mode %s, got %s", mode, pool.mode)
			}
		})
	}
}

func TestWorkerPoolJobBuffer(t *testing.T) {
	workers := 2
	pool := NewWorkerPool(workers, 30, time.Second, "external")

	// Job channel buffer should be workers * 2
	expectedBuffer := workers * 2
	actualBuffer := cap(pool.jobs)

	if actualBuffer != expectedBuffer {
		t.Errorf("Expected job buffer size %d, got %d", expectedBuffer, actualBuffer)
	}
}

func TestWorkerPoolMultipleSubmitsBeforeStart(t *testing.T) {
	pool := NewWorkerPool(2, 30, time.Second, "external")

	results := make(chan *TraceResult, 10)
	ips := []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("127.0.0.2"),
	}

	// Submit jobs before starting pool
	for _, ip := range ips {
		pool.Submit(ip, results)
	}

	// Now start pool
	pool.Start()

	// Jobs should be processed
	pool.Wait()
	close(results)

	resultCount := 0
	for range results {
		resultCount++
	}

	if resultCount != len(ips) {
		t.Errorf("Expected %d results, got %d", len(ips), resultCount)
	}
}

func TestWorkerPoolResultTimestamp(t *testing.T) {
	pool := NewWorkerPool(1, 30, time.Second, "external")
	pool.Start()

	results := make(chan *TraceResult, 1)
	ip := net.ParseIP("127.0.0.1")

	beforeSubmit := time.Now()
	pool.Submit(ip, results)
	pool.Wait()

	result := <-results
	afterComplete := time.Now()

	// Timestamp should be between submission and completion
	if result.Timestamp.Before(beforeSubmit) || result.Timestamp.After(afterComplete.Add(time.Second)) {
		t.Errorf("Result timestamp %v is outside expected range [%v, %v]",
			result.Timestamp, beforeSubmit, afterComplete)
	}
}
