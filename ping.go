package main

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PingResult represents the result of a ping operation
type PingResult struct {
	DestIP    string        `json:"dest_ip"`
	Reachable bool          `json:"reachable"`
	TTL       int           `json:"ttl"`
	RTT       time.Duration `json:"rtt_ns"`
	Timestamp time.Time     `json:"timestamp"`
	Duration  time.Duration `json:"duration_ns"`
}

// CheckPingBinary checks if ping command is available
func CheckPingBinary() bool {
	cmd := exec.Command("which", "ping")
	err := cmd.Run()
	return err == nil
}

// Ping performs a simple ICMP ping to check host reachability
// This is much faster than full traceroute (1 packet vs 30+)
func Ping(destIP net.IP, timeout time.Duration) (*PingResult, error) {
	startTime := time.Now()

	// TTL for ping packets (set high to ensure reachability)
	const pingTTL = 64

	result := &PingResult{
		DestIP:    destIP.String(),
		Reachable: false,
		Timestamp: startTime,
		TTL:       pingTTL,
	}

	// Build ping command
	// -c 1: send only 1 packet
	// -W: timeout in seconds
	// -n: numeric output only (no DNS)
	// -t: TTL value for outgoing packets
	timeoutSecs := int(timeout.Seconds())
	if timeoutSecs < 1 {
		timeoutSecs = 1
	}

	cmd := exec.Command("ping", "-c", "1", "-t", strconv.Itoa(pingTTL), "-W", strconv.Itoa(timeoutSecs), "-n", destIP.String())

	// Capture output
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// Run with timeout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ping: %v", err)
	}

	// Wait for completion with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			// Ping failed (host unreachable or timeout)
			result.Reachable = false
			result.Duration = time.Since(startTime)
			return result, nil
		}
	case <-time.After(timeout + time.Second):
		cmd.Process.Kill()
		result.Reachable = false
		result.Duration = time.Since(startTime)
		return result, nil
	}

	// Parse output for RTT
	output := stdout.String()
	rtt := parsePingOutput(output)

	result.Reachable = true
	result.RTT = rtt
	result.Duration = time.Since(startTime)
	return result, nil
}

// parsePingOutput extracts RTT from ping command output
func parsePingOutput(output string) time.Duration {
	var rtt time.Duration

	// Parse line like: "64 bytes from 8.8.8.8: icmp_seq=1 ttl=117 time=1.23 ms"
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "bytes from") {
			// Extract time
			timeRegex := regexp.MustCompile(`time[=\s]+([\d.]+)\s*ms`)
			timeMatches := timeRegex.FindStringSubmatch(line)
			if len(timeMatches) >= 2 {
				rttMs, err := strconv.ParseFloat(timeMatches[1], 64)
				if err == nil {
					rtt = time.Duration(rttMs * float64(time.Millisecond))
				}
			}
			break
		}
	}

	return rtt
}
