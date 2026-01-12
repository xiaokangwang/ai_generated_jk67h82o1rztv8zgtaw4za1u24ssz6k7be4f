package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
)

// CompletedRange represents a range of completed IPs
type CompletedRange struct {
	Start uint32 `json:"start"`
	End   uint32 `json:"end"`
}

// RangeCheckpoint manages scan progress using IP ranges (memory efficient)
type RangeCheckpoint struct {
	filename         string
	completedRanges  []CompletedRange
	pendingIPs       map[uint32]bool
	mutex            sync.RWMutex
	file             *os.File
	outputFile       string
	totalIPs         uint64
	scannedCount     uint64
	saveCounter      int
	rangeStart       uint32
	rangeEnd         uint32
}

// RangeCheckpointData represents the checkpoint file format
type RangeCheckpointData struct {
	OutputFile      string           `json:"output_file"`
	CompletedRanges []CompletedRange `json:"completed_ranges"`
	TotalIPs        uint64           `json:"total_ips"`
	ScannedCount    uint64           `json:"scanned_count"`
	RangeStart      uint32           `json:"range_start"`
	RangeEnd        uint32           `json:"range_end"`
}

// NewRangeCheckpoint creates a new range-based checkpoint manager
func NewRangeCheckpoint(outputFile string, rangeStart, rangeEnd uint32) (*RangeCheckpoint, error) {
	checkpointFile := outputFile + ".progress"

	cp := &RangeCheckpoint{
		filename:        checkpointFile,
		completedRanges: make([]CompletedRange, 0),
		pendingIPs:      make(map[uint32]bool),
		outputFile:      outputFile,
		rangeStart:      rangeStart,
		rangeEnd:        rangeEnd,
	}

	return cp, nil
}

// LoadProgress loads existing progress from checkpoint file
func (cp *RangeCheckpoint) LoadProgress() error {
	file, err := os.Open(cp.filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to open checkpoint: %v", err)
	}
	defer file.Close()

	var data RangeCheckpointData
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&data); err != nil {
		return fmt.Errorf("failed to decode checkpoint: %v", err)
	}

	// Verify output file matches
	if data.OutputFile != cp.outputFile {
		return fmt.Errorf("checkpoint output file mismatch: expected %s, got %s", cp.outputFile, data.OutputFile)
	}

	cp.totalIPs = data.TotalIPs
	cp.scannedCount = data.ScannedCount
	cp.completedRanges = data.CompletedRanges
	cp.rangeStart = data.RangeStart
	cp.rangeEnd = data.RangeEnd

	// Sort ranges by start IP
	sort.Slice(cp.completedRanges, func(i, j int) bool {
		return cp.completedRanges[i].Start < cp.completedRanges[j].Start
	})

	fmt.Printf("Loaded checkpoint: %d/%d IPs already scanned (%.1f%%), %d ranges\n",
		cp.scannedCount, cp.totalIPs, float64(cp.scannedCount)/float64(cp.totalIPs)*100, len(cp.completedRanges))

	return nil
}

// InitProgress initializes the checkpoint file for a new scan
func (cp *RangeCheckpoint) InitProgress(totalIPs uint64) error {
	cp.totalIPs = totalIPs

	file, err := os.Create(cp.filename)
	if err != nil {
		return fmt.Errorf("failed to create checkpoint: %v", err)
	}

	cp.file = file

	// Write initial checkpoint
	return cp.saveCheckpoint()
}

// MarkCompleted marks an IP as completed and updates checkpoint
func (cp *RangeCheckpoint) MarkCompleted(ipInt uint32) error {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()

	// Add to pending IPs
	cp.pendingIPs[ipInt] = true
	cp.scannedCount++
	cp.saveCounter++

	// Consolidate into ranges every 100 IPs or when we have 1000 pending
	if cp.saveCounter >= 100 || len(cp.pendingIPs) >= 1000 {
		cp.consolidateRanges()
		cp.saveCounter = 0
		if err := cp.saveCheckpoint(); err != nil {
			return err
		}
	}

	return nil
}

// consolidateRanges merges pending IPs into contiguous ranges
func (cp *RangeCheckpoint) consolidateRanges() {
	if len(cp.pendingIPs) == 0 {
		return
	}

	// Convert pending IPs to sorted slice
	pending := make([]uint32, 0, len(cp.pendingIPs))
	for ip := range cp.pendingIPs {
		pending = append(pending, ip)
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i] < pending[j]
	})

	// Convert to ranges
	newRanges := make([]CompletedRange, 0)
	rangeStart := pending[0]
	rangeEnd := pending[0]

	for i := 1; i < len(pending); i++ {
		if pending[i] == rangeEnd+1 {
			// Contiguous, extend range
			rangeEnd = pending[i]
		} else {
			// Gap, save current range and start new one
			newRanges = append(newRanges, CompletedRange{Start: rangeStart, End: rangeEnd})
			rangeStart = pending[i]
			rangeEnd = pending[i]
		}
	}
	// Save last range
	newRanges = append(newRanges, CompletedRange{Start: rangeStart, End: rangeEnd})

	// Merge with existing ranges
	cp.completedRanges = append(cp.completedRanges, newRanges...)
	cp.mergeOverlappingRanges()

	// Clear pending
	cp.pendingIPs = make(map[uint32]bool)
}

// mergeOverlappingRanges combines overlapping or adjacent ranges
func (cp *RangeCheckpoint) mergeOverlappingRanges() {
	if len(cp.completedRanges) <= 1 {
		return
	}

	// Sort by start
	sort.Slice(cp.completedRanges, func(i, j int) bool {
		return cp.completedRanges[i].Start < cp.completedRanges[j].Start
	})

	merged := make([]CompletedRange, 0)
	current := cp.completedRanges[0]

	for i := 1; i < len(cp.completedRanges); i++ {
		next := cp.completedRanges[i]

		if next.Start <= current.End+1 {
			// Overlapping or adjacent, merge
			if next.End > current.End {
				current.End = next.End
			}
		} else {
			// No overlap, save current and move to next
			merged = append(merged, current)
			current = next
		}
	}
	merged = append(merged, current)

	cp.completedRanges = merged
}

// saveCheckpoint writes current progress to disk
func (cp *RangeCheckpoint) saveCheckpoint() error {
	if cp.file == nil {
		return nil
	}

	data := RangeCheckpointData{
		OutputFile:      cp.outputFile,
		CompletedRanges: cp.completedRanges,
		TotalIPs:        cp.totalIPs,
		ScannedCount:    cp.scannedCount,
		RangeStart:      cp.rangeStart,
		RangeEnd:        cp.rangeEnd,
	}

	// Seek to beginning and truncate
	if _, err := cp.file.Seek(0, 0); err != nil {
		return err
	}
	if err := cp.file.Truncate(0); err != nil {
		return err
	}

	// Write JSON
	encoder := json.NewEncoder(cp.file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return err
	}

	// Flush to disk
	return cp.file.Sync()
}

// Close finalizes the checkpoint (saves final state)
func (cp *RangeCheckpoint) Close() error {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()

	// Final consolidation
	cp.consolidateRanges()

	if cp.file != nil {
		// Final save
		if err := cp.saveCheckpoint(); err != nil {
			return err
		}

		if err := cp.file.Close(); err != nil {
			return err
		}
		cp.file = nil
	}

	return nil
}

// DeleteCheckpoint removes the checkpoint file
func (cp *RangeCheckpoint) DeleteCheckpoint() error {
	if err := os.Remove(cp.filename); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// GetProgress returns current progress stats
func (cp *RangeCheckpoint) GetProgress() (scanned, total uint64, percentage float64) {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()

	scanned = cp.scannedCount
	total = cp.totalIPs

	if total > 0 {
		percentage = float64(scanned) / float64(total) * 100
	}

	return
}

// GetCompletedRanges returns the list of completed IP ranges
func (cp *RangeCheckpoint) GetCompletedRanges() []CompletedRange {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()

	// Return a copy
	ranges := make([]CompletedRange, len(cp.completedRanges))
	copy(ranges, cp.completedRanges)
	return ranges
}

// GetRemainingCount calculates remaining IPs (approximate if pending IPs exist)
func (cp *RangeCheckpoint) GetRemainingCount() uint64 {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()

	if cp.scannedCount >= cp.totalIPs {
		return 0
	}
	return cp.totalIPs - cp.scannedCount
}
