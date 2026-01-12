package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CheckTracerouteBinary checks if traceroute command is available
func CheckTracerouteBinary() bool {
	cmd := exec.Command("which", "traceroute")
	err := cmd.Run()
	return err == nil
}

// TracerouteExternal performs traceroute using the external traceroute command
func TracerouteExternal(destIP net.IP, maxHops int, timeout time.Duration) (*TraceResult, error) {
	startTime := time.Now()
	result := &TraceResult{
		DestIP:    destIP.String(),
		Hops:      make([]Hop, 0),
		Reached:   false,
		Timestamp: startTime,
	}

	// Build traceroute command
	// -n: don't resolve hostnames (faster)
	// -m: max hops
	// -w: timeout in seconds
	timeoutSecs := int(timeout.Seconds())
	if timeoutSecs < 1 {
		timeoutSecs = 1
	}

	cmd := exec.Command("traceroute", "-n", "-m", strconv.Itoa(maxHops), "-w", strconv.Itoa(timeoutSecs), destIP.String())

	// Capture output
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// Run with overall timeout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start traceroute: %v", err)
	}

	// Wait for completion with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	overallTimeout := time.Duration(maxHops) * timeout * 2
	select {
	case err := <-done:
		if err != nil {
			// traceroute may return non-zero even on successful runs
			// Continue parsing output anyway
		}
	case <-time.After(overallTimeout):
		cmd.Process.Kill()
		return nil, fmt.Errorf("traceroute timed out")
	}

	// Parse output
	output := stdout.String()
	result.Hops = parseTracerouteOutput(output)

	// Check if destination was reached
	if len(result.Hops) > 0 {
		lastHop := result.Hops[len(result.Hops)-1]
		if !lastHop.Timeout && lastHop.IP == destIP.String() {
			result.Reached = true
		}
	}

	result.Duration = time.Since(startTime)
	return result, nil
}

// parseTracerouteOutput parses the output of the traceroute command
func parseTracerouteOutput(output string) []Hop {
	var hops []Hop

	scanner := bufio.NewScanner(strings.NewReader(output))

	// Regex patterns for different traceroute output formats
	// Standard format: " 1  192.168.1.1  0.123 ms  0.234 ms  0.345 ms"
	// Timeout format: " 1  * * *"
	// Single response: " 1  192.168.1.1  0.123 ms"
	hopRegex := regexp.MustCompile(`^\s*(\d+)\s+(.+)$`)
	ipRegex := regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)`)
	timeRegex := regexp.MustCompile(`([\d.]+)\s*ms`)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip header line
		if strings.Contains(line, "traceroute to") {
			continue
		}

		match := hopRegex.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		ttlStr := match[1]
		hopData := match[2]

		ttl, err := strconv.Atoi(ttlStr)
		if err != nil {
			continue
		}

		hop := Hop{
			TTL:     ttl,
			Timeout: false,
		}

		// Check if this hop timed out (contains only asterisks)
		if strings.Contains(hopData, "* * *") || strings.TrimSpace(hopData) == "*" {
			hop.Timeout = true
			hops = append(hops, hop)
			continue
		}

		// Extract IP address
		ipMatch := ipRegex.FindString(hopData)
		if ipMatch != "" {
			hop.IP = ipMatch
		}

		// Extract RTT (use first time value)
		timeMatches := timeRegex.FindAllStringSubmatch(hopData, -1)
		if len(timeMatches) > 0 {
			rttStr := timeMatches[0][1]
			rttMs, err := strconv.ParseFloat(rttStr, 64)
			if err == nil {
				hop.RTT = time.Duration(rttMs * float64(time.Millisecond))
			}
		}

		hops = append(hops, hop)
	}

	return hops
}
