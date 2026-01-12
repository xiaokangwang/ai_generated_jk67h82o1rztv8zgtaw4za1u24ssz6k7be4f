package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
)

func TestNewCheckpoint(t *testing.T) {
	outputFile := "test_checkpoint_output.jsonl"
	cp, err := NewCheckpoint(outputFile)
	if err != nil {
		t.Fatalf("NewCheckpoint failed: %v", err)
	}

	expectedFilename := outputFile + ".progress"
	if cp.filename != expectedFilename {
		t.Errorf("Expected filename %s, got %s", expectedFilename, cp.filename)
	}

	if cp.outputFile != outputFile {
		t.Errorf("Expected outputFile %s, got %s", outputFile, cp.outputFile)
	}

	if cp.completed == nil {
		t.Error("Expected completed map to be initialized")
	}
}

func TestCheckpointInitProgress(t *testing.T) {
	outputFile := "test_init_progress.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	cp, _ := NewCheckpoint(outputFile)
	totalIPs := 100

	err := cp.InitProgress(totalIPs)
	if err != nil {
		t.Fatalf("InitProgress failed: %v", err)
	}
	defer cp.Close()

	if cp.totalIPs != totalIPs {
		t.Errorf("Expected totalIPs %d, got %d", totalIPs, cp.totalIPs)
	}

	// Verify checkpoint file was created
	if _, err := os.Stat(checkpointFile); os.IsNotExist(err) {
		t.Error("Checkpoint file was not created")
	}

	// Verify initial checkpoint content
	data, err := os.ReadFile(checkpointFile)
	if err != nil {
		t.Fatalf("Failed to read checkpoint file: %v", err)
	}

	var checkpointData CheckpointData
	if err := json.Unmarshal(data, &checkpointData); err != nil {
		t.Fatalf("Failed to unmarshal checkpoint: %v", err)
	}

	if checkpointData.TotalIPs != totalIPs {
		t.Errorf("Expected checkpoint totalIPs %d, got %d", totalIPs, checkpointData.TotalIPs)
	}

	if len(checkpointData.Completed) != 0 {
		t.Errorf("Expected empty completed list, got %d items", len(checkpointData.Completed))
	}
}

func TestCheckpointMarkCompleted(t *testing.T) {
	outputFile := "test_mark_completed.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	cp, _ := NewCheckpoint(outputFile)
	cp.InitProgress(50)
	defer cp.Close()

	ip1 := net.ParseIP("192.168.1.1")
	ip2 := net.ParseIP("192.168.1.2")

	// Mark first IP
	err := cp.MarkCompleted(ip1)
	if err != nil {
		t.Fatalf("MarkCompleted failed: %v", err)
	}

	if !cp.IsCompleted(ip1) {
		t.Error("IP1 should be marked as completed")
	}

	if cp.IsCompleted(ip2) {
		t.Error("IP2 should not be marked as completed")
	}

	// Mark second IP
	cp.MarkCompleted(ip2)
	if !cp.IsCompleted(ip2) {
		t.Error("IP2 should be marked as completed")
	}

	// Mark same IP again (should be idempotent)
	cp.MarkCompleted(ip1)
	if cp.scannedCount != 2 {
		t.Errorf("Expected scannedCount 2, got %d", cp.scannedCount)
	}
}

func TestCheckpointGetProgress(t *testing.T) {
	outputFile := "test_get_progress.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	cp, _ := NewCheckpoint(outputFile)
	cp.InitProgress(100)
	defer cp.Close()

	// Initial progress
	scanned, total, pct := cp.GetProgress()
	if scanned != 0 || total != 100 || pct != 0.0 {
		t.Errorf("Expected 0/100 (0%%), got %d/%d (%.1f%%)", scanned, total, pct)
	}

	// Mark 10 IPs
	for i := 1; i <= 10; i++ {
		ip := net.ParseIP(fmt.Sprintf("192.168.1.%d", i))
		cp.MarkCompleted(ip)
	}

	scanned, total, pct = cp.GetProgress()
	if scanned != 10 || total != 100 {
		t.Errorf("Expected 10/100, got %d/%d", scanned, total)
	}

	expectedPct := 10.0
	if pct != expectedPct {
		t.Errorf("Expected percentage %.1f%%, got %.1f%%", expectedPct, pct)
	}
}

func TestCheckpointFilterCompleted(t *testing.T) {
	outputFile := "test_filter_completed.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	cp, _ := NewCheckpoint(outputFile)
	cp.InitProgress(5)
	defer cp.Close()

	ips := []net.IP{
		net.ParseIP("192.168.1.1"),
		net.ParseIP("192.168.1.2"),
		net.ParseIP("192.168.1.3"),
		net.ParseIP("192.168.1.4"),
		net.ParseIP("192.168.1.5"),
	}

	// Mark some as completed
	cp.MarkCompleted(ips[0])
	cp.MarkCompleted(ips[2])
	cp.MarkCompleted(ips[4])

	// Filter
	remaining := cp.FilterCompleted(ips)

	if len(remaining) != 2 {
		t.Errorf("Expected 2 remaining IPs, got %d", len(remaining))
	}

	// Check that remaining are the correct ones
	expectedRemaining := map[string]bool{
		"192.168.1.2": true,
		"192.168.1.4": true,
	}

	for _, ip := range remaining {
		if !expectedRemaining[ip.String()] {
			t.Errorf("Unexpected IP in remaining list: %s", ip.String())
		}
	}
}

func TestCheckpointLoadProgress(t *testing.T) {
	outputFile := "test_load_progress.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	// Create and save checkpoint
	cp1, _ := NewCheckpoint(outputFile)
	cp1.InitProgress(20)

	ips := []net.IP{
		net.ParseIP("10.0.0.1"),
		net.ParseIP("10.0.0.2"),
		net.ParseIP("10.0.0.3"),
	}

	for _, ip := range ips {
		cp1.MarkCompleted(ip)
	}
	cp1.Close()

	// Load checkpoint
	cp2, _ := NewCheckpoint(outputFile)
	loadedIPs, err := cp2.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}

	if len(loadedIPs) != 3 {
		t.Errorf("Expected 3 loaded IPs, got %d", len(loadedIPs))
	}

	if cp2.totalIPs != 20 {
		t.Errorf("Expected totalIPs 20, got %d", cp2.totalIPs)
	}

	if cp2.scannedCount != 3 {
		t.Errorf("Expected scannedCount 3, got %d", cp2.scannedCount)
	}

	// Verify loaded IPs are marked as completed
	for _, ip := range ips {
		if !cp2.IsCompleted(ip) {
			t.Errorf("Loaded IP %s should be marked as completed", ip.String())
		}
	}
}

func TestCheckpointLoadProgressNoFile(t *testing.T) {
	outputFile := "test_no_checkpoint.jsonl"

	cp, _ := NewCheckpoint(outputFile)
	loadedIPs, err := cp.LoadProgress()

	if err != nil {
		t.Errorf("LoadProgress should not error on missing file, got: %v", err)
	}

	if loadedIPs != nil {
		t.Error("Expected nil loadedIPs when no checkpoint exists")
	}
}

func TestCheckpointLoadProgressMismatch(t *testing.T) {
	outputFile1 := "test_output1.jsonl"
	outputFile2 := "test_output2.jsonl"
	checkpointFile1 := outputFile1 + ".progress"
	defer os.Remove(checkpointFile1)

	// Create checkpoint for outputFile1
	cp1, _ := NewCheckpoint(outputFile1)
	cp1.InitProgress(10)
	cp1.Close()

	// Try to load checkpoint from outputFile1 but with outputFile2 context
	// Manually rename the checkpoint to the second output file's progress name
	checkpointFile2 := outputFile2 + ".progress"
	data, _ := os.ReadFile(checkpointFile1)
	os.WriteFile(checkpointFile2, data, 0644)
	defer os.Remove(checkpointFile2)

	// Now try to load with different output file (will have mismatch in JSON)
	cp2, _ := NewCheckpoint(outputFile2)
	_, err := cp2.LoadProgress()

	if err == nil {
		t.Error("Expected error when output file mismatches in checkpoint JSON")
	}
}

func TestCheckpointDeleteCheckpoint(t *testing.T) {
	outputFile := "test_delete.jsonl"
	checkpointFile := outputFile + ".progress"

	cp, _ := NewCheckpoint(outputFile)
	cp.InitProgress(10)
	cp.Close()

	// Verify file exists
	if _, err := os.Stat(checkpointFile); os.IsNotExist(err) {
		t.Fatal("Checkpoint file should exist")
	}

	// Delete
	err := cp.DeleteCheckpoint()
	if err != nil {
		t.Fatalf("DeleteCheckpoint failed: %v", err)
	}

	// Verify file is gone
	if _, err := os.Stat(checkpointFile); !os.IsNotExist(err) {
		t.Error("Checkpoint file should be deleted")
	}

	// Delete again (should not error)
	err = cp.DeleteCheckpoint()
	if err != nil {
		t.Errorf("DeleteCheckpoint should not error on already deleted file: %v", err)
	}
}

func TestCheckpointPeriodicSave(t *testing.T) {
	outputFile := "test_periodic_save.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	cp, _ := NewCheckpoint(outputFile)
	cp.InitProgress(100)
	defer cp.Close()

	// Mark 15 IPs (checkpoint saved every 10)
	for i := 1; i <= 15; i++ {
		ip := net.ParseIP(fmt.Sprintf("10.0.0.%d", i))
		cp.MarkCompleted(ip)
	}

	// Read checkpoint file
	data, err := os.ReadFile(checkpointFile)
	if err != nil {
		t.Fatalf("Failed to read checkpoint file: %v", err)
	}

	var checkpointData CheckpointData
	if err := json.Unmarshal(data, &checkpointData); err != nil {
		t.Fatalf("Failed to unmarshal checkpoint: %v", err)
	}

	// Should have saved at IP 10
	if len(checkpointData.Completed) < 10 {
		t.Errorf("Expected at least 10 completed IPs in checkpoint, got %d", len(checkpointData.Completed))
	}
}

func TestCheckpointConcurrency(t *testing.T) {
	outputFile := "test_concurrency.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	cp, _ := NewCheckpoint(outputFile)
	cp.InitProgress(100)
	defer cp.Close()

	// Concurrent marking
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(base int) {
			for j := 0; j < 10; j++ {
				ip := net.ParseIP(fmt.Sprintf("10.0.%d.%d", base, j))
				cp.MarkCompleted(ip)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	scanned, _, _ := cp.GetProgress()
	if scanned != 100 {
		t.Errorf("Expected 100 scanned IPs, got %d", scanned)
	}
}
