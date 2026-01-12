package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
)

// Checkpoint manages scan progress for resumable scans
type Checkpoint struct {
	filename     string
	completed    map[string]bool
	mutex        sync.RWMutex
	file         *os.File
	writer       *bufio.Writer
	outputFile   string
	totalIPs     int
	scannedCount int
}

// CheckpointData represents the checkpoint file format
type CheckpointData struct {
	OutputFile string   `json:"output_file"`
	Completed  []string `json:"completed"`
	TotalIPs   int      `json:"total_ips"`
}

// NewCheckpoint creates a new checkpoint manager
func NewCheckpoint(outputFile string) (*Checkpoint, error) {
	checkpointFile := outputFile + ".progress"

	cp := &Checkpoint{
		filename:   checkpointFile,
		completed:  make(map[string]bool),
		outputFile: outputFile,
	}

	return cp, nil
}

// LoadProgress loads existing progress from checkpoint file
func (cp *Checkpoint) LoadProgress() ([]net.IP, error) {
	file, err := os.Open(cp.filename)
	if err != nil {
		if os.IsNotExist(err) {
			// No checkpoint file, starting fresh
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open checkpoint: %v", err)
	}
	defer file.Close()

	var data CheckpointData
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode checkpoint: %v", err)
	}

	// Verify output file matches
	if data.OutputFile != cp.outputFile {
		return nil, fmt.Errorf("checkpoint output file mismatch: expected %s, got %s", cp.outputFile, data.OutputFile)
	}

	cp.totalIPs = data.TotalIPs
	cp.scannedCount = len(data.Completed)

	// Build completed IP set
	completedIPs := make([]net.IP, 0, len(data.Completed))
	for _, ipStr := range data.Completed {
		ip := net.ParseIP(ipStr)
		if ip != nil {
			cp.completed[ipStr] = true
			completedIPs = append(completedIPs, ip)
		}
	}

	fmt.Printf("Loaded checkpoint: %d/%d IPs already scanned (%.1f%%)\n",
		len(cp.completed), cp.totalIPs, float64(len(cp.completed))/float64(cp.totalIPs)*100)

	return completedIPs, nil
}

// InitProgress initializes the checkpoint file for a new scan
func (cp *Checkpoint) InitProgress(totalIPs int) error {
	cp.totalIPs = totalIPs

	file, err := os.Create(cp.filename)
	if err != nil {
		return fmt.Errorf("failed to create checkpoint: %v", err)
	}

	cp.file = file
	cp.writer = bufio.NewWriter(file)

	// Write initial checkpoint
	return cp.saveCheckpoint()
}

// MarkCompleted marks an IP as completed and updates checkpoint
func (cp *Checkpoint) MarkCompleted(ip net.IP) error {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()

	ipStr := ip.String()
	if !cp.completed[ipStr] {
		cp.completed[ipStr] = true
		cp.scannedCount++

		// Save checkpoint periodically (every 10 IPs)
		if cp.scannedCount%10 == 0 {
			if err := cp.saveCheckpoint(); err != nil {
				return err
			}
		}
	}

	return nil
}

// saveCheckpoint writes current progress to disk
func (cp *Checkpoint) saveCheckpoint() error {
	if cp.file == nil {
		return nil
	}

	// Build completed list
	completed := make([]string, 0, len(cp.completed))
	for ipStr := range cp.completed {
		completed = append(completed, ipStr)
	}

	data := CheckpointData{
		OutputFile: cp.outputFile,
		Completed:  completed,
		TotalIPs:   cp.totalIPs,
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
func (cp *Checkpoint) Close() error {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()

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

// DeleteCheckpoint removes the checkpoint file (after successful completion)
func (cp *Checkpoint) DeleteCheckpoint() error {
	if err := os.Remove(cp.filename); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsCompleted checks if an IP has been completed
func (cp *Checkpoint) IsCompleted(ip net.IP) bool {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()
	return cp.completed[ip.String()]
}

// GetProgress returns current progress stats
func (cp *Checkpoint) GetProgress() (scanned, total int, percentage float64) {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()

	scanned = cp.scannedCount
	total = cp.totalIPs

	if total > 0 {
		percentage = float64(scanned) / float64(total) * 100
	}

	return
}

// FilterCompleted removes already-completed IPs from the list
func (cp *Checkpoint) FilterCompleted(ips []net.IP) []net.IP {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()

	if len(cp.completed) == 0 {
		return ips
	}

	remaining := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !cp.completed[ip.String()] {
			remaining = append(remaining, ip)
		}
	}

	return remaining
}
