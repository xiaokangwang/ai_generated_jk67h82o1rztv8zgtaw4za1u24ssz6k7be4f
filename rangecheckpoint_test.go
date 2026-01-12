package main

import (
	"encoding/json"
	"net"
	"os"
	"testing"
)

func TestNewRangeCheckpoint(t *testing.T) {
	outputFile := "test_range_checkpoint.jsonl"
	rangeStart := IPToUint32(net.ParseIP("192.168.1.0"))
	rangeEnd := IPToUint32(net.ParseIP("192.168.1.255"))

	cp, err := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	if err != nil {
		t.Fatalf("NewRangeCheckpoint failed: %v", err)
	}

	expectedFilename := outputFile + ".progress"
	if cp.filename != expectedFilename {
		t.Errorf("Expected filename %s, got %s", expectedFilename, cp.filename)
	}

	if cp.rangeStart != rangeStart {
		t.Errorf("Expected rangeStart %d, got %d", rangeStart, cp.rangeStart)
	}

	if cp.rangeEnd != rangeEnd {
		t.Errorf("Expected rangeEnd %d, got %d", rangeEnd, cp.rangeEnd)
	}

	if cp.completedRanges == nil {
		t.Error("Expected completedRanges to be initialized")
	}

	if cp.pendingIPs == nil {
		t.Error("Expected pendingIPs to be initialized")
	}
}

func TestRangeCheckpointInitProgress(t *testing.T) {
	outputFile := "test_range_init.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	totalIPs := uint64(256)

	err := cp.InitProgress(totalIPs)
	if err != nil {
		t.Fatalf("InitProgress failed: %v", err)
	}
	defer cp.Close()

	if cp.totalIPs != totalIPs {
		t.Errorf("Expected totalIPs %d, got %d", totalIPs, cp.totalIPs)
	}

	// Verify checkpoint file created
	if _, err := os.Stat(checkpointFile); os.IsNotExist(err) {
		t.Error("Checkpoint file was not created")
	}

	// Verify checkpoint content
	data, _ := os.ReadFile(checkpointFile)
	var checkpointData RangeCheckpointData
	json.Unmarshal(data, &checkpointData)

	if checkpointData.TotalIPs != totalIPs {
		t.Errorf("Expected checkpoint totalIPs %d, got %d", totalIPs, checkpointData.TotalIPs)
	}

	if len(checkpointData.CompletedRanges) != 0 {
		t.Errorf("Expected empty completed ranges, got %d", len(checkpointData.CompletedRanges))
	}
}

func TestRangeCheckpointMarkCompleted(t *testing.T) {
	outputFile := "test_range_mark.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("192.168.1.0"))
	rangeEnd := IPToUint32(net.ParseIP("192.168.1.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	ip1 := IPToUint32(net.ParseIP("192.168.1.1"))
	ip2 := IPToUint32(net.ParseIP("192.168.1.2"))

	// Mark IPs
	err := cp.MarkCompleted(ip1)
	if err != nil {
		t.Fatalf("MarkCompleted failed: %v", err)
	}

	if cp.scannedCount != 1 {
		t.Errorf("Expected scannedCount 1, got %d", cp.scannedCount)
	}

	cp.MarkCompleted(ip2)
	if cp.scannedCount != 2 {
		t.Errorf("Expected scannedCount 2, got %d", cp.scannedCount)
	}
}

func TestRangeCheckpointConsolidateRanges(t *testing.T) {
	outputFile := "test_consolidate.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	// Mark contiguous IPs
	for i := uint32(1); i <= 5; i++ {
		cp.pendingIPs[rangeStart+i] = true
	}

	cp.consolidateRanges()

	if len(cp.completedRanges) != 1 {
		t.Errorf("Expected 1 consolidated range, got %d", len(cp.completedRanges))
	}

	if len(cp.pendingIPs) != 0 {
		t.Error("Expected pending IPs to be cleared after consolidation")
	}

	// Verify range bounds
	expectedStart := rangeStart + 1
	expectedEnd := rangeStart + 5

	if cp.completedRanges[0].Start != expectedStart {
		t.Errorf("Expected range start %d, got %d", expectedStart, cp.completedRanges[0].Start)
	}

	if cp.completedRanges[0].End != expectedEnd {
		t.Errorf("Expected range end %d, got %d", expectedEnd, cp.completedRanges[0].End)
	}
}

func TestRangeCheckpointConsolidateDiscontiguousRanges(t *testing.T) {
	outputFile := "test_discontiguous.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	// Mark non-contiguous IPs: 1-3, 5-7, 10
	for i := uint32(1); i <= 3; i++ {
		cp.pendingIPs[rangeStart+i] = true
	}
	for i := uint32(5); i <= 7; i++ {
		cp.pendingIPs[rangeStart+i] = true
	}
	cp.pendingIPs[rangeStart+10] = true

	cp.consolidateRanges()

	// Should create 3 ranges: [1-3], [5-7], [10-10]
	if len(cp.completedRanges) != 3 {
		t.Errorf("Expected 3 ranges, got %d", len(cp.completedRanges))
	}
}

func TestRangeCheckpointMergeOverlappingRanges(t *testing.T) {
	outputFile := "test_merge.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	// Create overlapping ranges
	cp.completedRanges = []CompletedRange{
		{Start: 100, End: 110},
		{Start: 105, End: 115},
		{Start: 120, End: 130},
	}

	cp.mergeOverlappingRanges()

	// Should merge first two into [100-115], keep [120-130]
	if len(cp.completedRanges) != 2 {
		t.Errorf("Expected 2 merged ranges, got %d", len(cp.completedRanges))
	}

	if cp.completedRanges[0].Start != 100 || cp.completedRanges[0].End != 115 {
		t.Errorf("Expected merged range [100-115], got [%d-%d]",
			cp.completedRanges[0].Start, cp.completedRanges[0].End)
	}
}

func TestRangeCheckpointMergeAdjacentRanges(t *testing.T) {
	outputFile := "test_adjacent.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	// Create adjacent ranges: [100-110], [111-120]
	cp.completedRanges = []CompletedRange{
		{Start: 100, End: 110},
		{Start: 111, End: 120},
	}

	cp.mergeOverlappingRanges()

	// Should merge into single range [100-120]
	if len(cp.completedRanges) != 1 {
		t.Errorf("Expected 1 merged range, got %d", len(cp.completedRanges))
	}

	if cp.completedRanges[0].Start != 100 || cp.completedRanges[0].End != 120 {
		t.Errorf("Expected merged range [100-120], got [%d-%d]",
			cp.completedRanges[0].Start, cp.completedRanges[0].End)
	}
}

func TestRangeCheckpointGetProgress(t *testing.T) {
	outputFile := "test_range_progress.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("192.168.1.0"))
	rangeEnd := IPToUint32(net.ParseIP("192.168.1.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	// Initial progress
	scanned, total, pct := cp.GetProgress()
	if scanned != 0 || total != 256 || pct != 0.0 {
		t.Errorf("Expected 0/256 (0%%), got %d/%d (%.2f%%)", scanned, total, pct)
	}

	// Mark 25 IPs (25/256 = 9.765625%)
	for i := uint32(0); i < 25; i++ {
		cp.MarkCompleted(rangeStart + i)
	}

	scanned, total, pct = cp.GetProgress()
	if scanned != 25 || total != 256 {
		t.Errorf("Expected 25/256, got %d/%d", scanned, total)
	}

	expectedPct := float64(25) / float64(256) * 100
	if pct < expectedPct-0.1 || pct > expectedPct+0.1 {
		t.Errorf("Expected percentage ~%.2f%%, got %.2f%%", expectedPct, pct)
	}
}

func TestRangeCheckpointGetCompletedRanges(t *testing.T) {
	outputFile := "test_get_ranges.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	// Add some ranges
	cp.completedRanges = []CompletedRange{
		{Start: 100, End: 110},
		{Start: 200, End: 210},
	}

	ranges := cp.GetCompletedRanges()

	if len(ranges) != 2 {
		t.Errorf("Expected 2 ranges, got %d", len(ranges))
	}

	// Verify it's a copy (modifying returned slice shouldn't affect internal state)
	ranges[0].Start = 999
	if cp.completedRanges[0].Start == 999 {
		t.Error("GetCompletedRanges should return a copy, not reference")
	}
}

func TestRangeCheckpointGetRemainingCount(t *testing.T) {
	outputFile := "test_remaining.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("192.168.1.0"))
	rangeEnd := IPToUint32(net.ParseIP("192.168.1.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	// Initially all remaining
	remaining := cp.GetRemainingCount()
	if remaining != 256 {
		t.Errorf("Expected 256 remaining, got %d", remaining)
	}

	// Scan 50 IPs
	for i := uint32(0); i < 50; i++ {
		cp.MarkCompleted(rangeStart + i)
	}

	remaining = cp.GetRemainingCount()
	if remaining != 206 {
		t.Errorf("Expected 206 remaining, got %d", remaining)
	}

	// Scan all remaining
	for i := uint32(50); i < 256; i++ {
		cp.MarkCompleted(rangeStart + i)
	}

	remaining = cp.GetRemainingCount()
	if remaining != 0 {
		t.Errorf("Expected 0 remaining, got %d", remaining)
	}
}

func TestRangeCheckpointLoadProgress(t *testing.T) {
	outputFile := "test_range_load.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	// Create and save checkpoint
	cp1, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp1.InitProgress(256)

	// Mark some IPs
	for i := uint32(1); i <= 100; i++ {
		cp1.MarkCompleted(rangeStart + i)
	}
	cp1.Close()

	// Load checkpoint
	cp2, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	err := cp2.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}

	if cp2.totalIPs != 256 {
		t.Errorf("Expected totalIPs 256, got %d", cp2.totalIPs)
	}

	if cp2.scannedCount != 100 {
		t.Errorf("Expected scannedCount 100, got %d", cp2.scannedCount)
	}

	if len(cp2.completedRanges) == 0 {
		t.Error("Expected some completed ranges after load")
	}
}

func TestRangeCheckpointLoadProgressNoFile(t *testing.T) {
	outputFile := "test_no_range_checkpoint.jsonl"

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	err := cp.LoadProgress()

	if err != nil {
		t.Errorf("LoadProgress should not error on missing file, got: %v", err)
	}
}

func TestRangeCheckpointDeleteCheckpoint(t *testing.T) {
	outputFile := "test_range_delete.jsonl"
	checkpointFile := outputFile + ".progress"

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(100)
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
}

func TestRangeCheckpointPeriodicSave(t *testing.T) {
	outputFile := "test_range_periodic.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.0.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(256)
	defer cp.Close()

	// Mark 150 IPs (saves every 100)
	for i := uint32(0); i < 150; i++ {
		cp.MarkCompleted(rangeStart + i)
	}

	// Read checkpoint file
	data, err := os.ReadFile(checkpointFile)
	if err != nil {
		t.Fatalf("Failed to read checkpoint file: %v", err)
	}

	var checkpointData RangeCheckpointData
	json.Unmarshal(data, &checkpointData)

	// Should have saved at 100 IPs
	if checkpointData.ScannedCount < 100 {
		t.Errorf("Expected at least 100 scanned in checkpoint, got %d", checkpointData.ScannedCount)
	}

	if len(checkpointData.CompletedRanges) == 0 {
		t.Error("Expected some completed ranges in checkpoint")
	}
}

func TestRangeCheckpointConcurrency(t *testing.T) {
	outputFile := "test_range_concurrency.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.3.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(1024)
	defer cp.Close()

	// Concurrent marking from multiple goroutines
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(base int) {
			for j := 0; j < 100; j++ {
				ipInt := rangeStart + uint32(base*100+j)
				cp.MarkCompleted(ipInt)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	scanned, _, _ := cp.GetProgress()
	if scanned != 1000 {
		t.Errorf("Expected 1000 scanned IPs, got %d", scanned)
	}
}

func TestRangeCheckpointLargeRangeConsolidation(t *testing.T) {
	outputFile := "test_large_consolidation.jsonl"
	checkpointFile := outputFile + ".progress"
	defer os.Remove(checkpointFile)

	rangeStart := IPToUint32(net.ParseIP("10.0.0.0"))
	rangeEnd := IPToUint32(net.ParseIP("10.0.255.255"))

	cp, _ := NewRangeCheckpoint(outputFile, rangeStart, rangeEnd)
	cp.InitProgress(65536)
	defer cp.Close()

	// Mark 1000 contiguous IPs
	for i := uint32(0); i < 1000; i++ {
		cp.pendingIPs[rangeStart+i] = true
	}

	cp.consolidateRanges()

	// Should consolidate into 1 range
	if len(cp.completedRanges) != 1 {
		t.Errorf("Expected 1 consolidated range for contiguous IPs, got %d", len(cp.completedRanges))
	}

	expectedRange := CompletedRange{
		Start: rangeStart,
		End:   rangeStart + 999,
	}

	if cp.completedRanges[0] != expectedRange {
		t.Errorf("Expected range [%d-%d], got [%d-%d]",
			expectedRange.Start, expectedRange.End,
			cp.completedRanges[0].Start, cp.completedRanges[0].End)
	}
}
