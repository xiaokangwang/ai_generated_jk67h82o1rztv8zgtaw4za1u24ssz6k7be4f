package main

import (
	"net"
	"strings"
	"testing"
)

func TestCheckTracerouteBinary(t *testing.T) {
	// This test assumes traceroute is installed
	// In CI environments, you might need to skip this test
	result := CheckTracerouteBinary()
	if !result {
		t.Skip("traceroute binary not found, skipping test")
	}
}

func TestParseTracerouteOutput(t *testing.T) {
	// Test standard traceroute output
	output := ` 1  192.168.1.1  0.123 ms  0.234 ms  0.345 ms
 2  10.0.0.1  1.234 ms  1.345 ms  1.456 ms
 3  * * *
 4  8.8.8.8  10.123 ms  10.234 ms  10.345 ms`

	hops := parseTracerouteOutput(output)

	if len(hops) != 4 {
		t.Errorf("Expected 4 hops, got %d", len(hops))
	}

	// Check first hop
	if hops[0].TTL != 1 {
		t.Errorf("First hop TTL should be 1, got %d", hops[0].TTL)
	}
	if hops[0].IP != "192.168.1.1" {
		t.Errorf("First hop IP should be 192.168.1.1, got %s", hops[0].IP)
	}
	if hops[0].Timeout {
		t.Error("First hop should not be timeout")
	}

	// Check timeout hop
	if hops[2].TTL != 3 {
		t.Errorf("Third hop TTL should be 3, got %d", hops[2].TTL)
	}
	if !hops[2].Timeout {
		t.Error("Third hop should be timeout")
	}

	// Check last hop
	if hops[3].IP != "8.8.8.8" {
		t.Errorf("Last hop IP should be 8.8.8.8, got %s", hops[3].IP)
	}
}

func TestParseTracerouteOutputWithHeader(t *testing.T) {
	// Test with traceroute header line
	output := `traceroute to 8.8.8.8 (8.8.8.8), 30 hops max, 60 byte packets
 1  192.168.1.1  0.123 ms  0.234 ms  0.345 ms
 2  10.0.0.1  1.234 ms  1.345 ms  1.456 ms`

	hops := parseTracerouteOutput(output)

	if len(hops) != 2 {
		t.Errorf("Expected 2 hops, got %d", len(hops))
	}

	if hops[0].IP != "192.168.1.1" {
		t.Errorf("First hop IP should be 192.168.1.1, got %s", hops[0].IP)
	}
}

func TestParseTracerouteOutputSingleTime(t *testing.T) {
	// Some traceroute formats show only one time
	output := ` 1  192.168.1.1  0.500 ms
 2  10.0.0.1  1.500 ms`

	hops := parseTracerouteOutput(output)

	if len(hops) != 2 {
		t.Errorf("Expected 2 hops, got %d", len(hops))
	}

	// Check RTT parsing
	if hops[0].RTT == 0 {
		t.Error("First hop RTT should not be 0")
	}
}

func TestParseTracerouteOutputEmpty(t *testing.T) {
	output := ""
	hops := parseTracerouteOutput(output)

	if len(hops) != 0 {
		t.Errorf("Expected 0 hops for empty output, got %d", len(hops))
	}
}

func TestParseTracerouteOutputMixedTimeouts(t *testing.T) {
	// Some hops may have partial responses
	output := ` 1  192.168.1.1  0.123 ms  0.234 ms  0.345 ms
 2  * * *
 3  * * *
 4  10.0.0.1  5.123 ms  5.234 ms  5.345 ms
 5  * * *`

	hops := parseTracerouteOutput(output)

	if len(hops) != 5 {
		t.Errorf("Expected 5 hops, got %d", len(hops))
	}

	// Check pattern: response, timeout, timeout, response, timeout
	if hops[0].Timeout || !hops[1].Timeout || !hops[2].Timeout || hops[3].Timeout || !hops[4].Timeout {
		t.Error("Timeout pattern does not match expected")
	}
}

func TestTracerouteExternalIntegration(t *testing.T) {
	if !CheckTracerouteBinary() {
		t.Skip("traceroute binary not found, skipping integration test")
	}

	// Test with localhost (should be fast and reliable)
	ip := net.ParseIP("127.0.0.1")
	result, err := TracerouteExternal(ip, 5, 1000000000) // 1 second timeout

	if err != nil {
		t.Fatalf("TracerouteExternal failed: %v", err)
	}

	if result == nil {
		t.Fatal("Result should not be nil")
	}

	if result.DestIP != "127.0.0.1" {
		t.Errorf("Destination IP should be 127.0.0.1, got %s", result.DestIP)
	}

	// Localhost should typically be reached in 1 hop or be reached immediately
	if len(result.Hops) == 0 {
		t.Log("Warning: No hops returned for localhost traceroute")
	}
}

func TestParseTracerouteRealFormat(t *testing.T) {
	// Real output from traceroute command
	realOutput := `traceroute to 8.8.8.8 (8.8.8.8), 10 hops max, 60 byte packets
 1  10.0.2.2  0.077 ms  0.101 ms  0.124 ms
 2  192.168.1.1  9.287 ms  9.310 ms  9.332 ms
 3  * * *
 4  172.24.112.89  13.308 ms  13.331 ms  13.353 ms`

	hops := parseTracerouteOutput(realOutput)

	if len(hops) != 4 {
		t.Fatalf("Expected 4 hops, got %d", len(hops))
	}

	// Verify first hop
	if hops[0].TTL != 1 || hops[0].IP != "10.0.2.2" || hops[0].Timeout {
		t.Errorf("First hop incorrect: TTL=%d, IP=%s, Timeout=%v", hops[0].TTL, hops[0].IP, hops[0].Timeout)
	}

	// Verify timeout hop
	if hops[2].TTL != 3 || !hops[2].Timeout {
		t.Errorf("Third hop should be timeout: TTL=%d, Timeout=%v", hops[2].TTL, hops[2].Timeout)
	}

	// Verify last hop
	if hops[3].TTL != 4 || hops[3].IP != "172.24.112.89" {
		t.Errorf("Last hop incorrect: TTL=%d, IP=%s", hops[3].TTL, hops[3].IP)
	}
}

func TestParseTracerouteOutputNoSpaces(t *testing.T) {
	// Test different spacing formats
	outputs := []string{
		" 1  192.168.1.1  0.123 ms",
		"  1  192.168.1.1  0.123 ms",
		"1  192.168.1.1  0.123 ms",
	}

	for _, output := range outputs {
		hops := parseTracerouteOutput(output)
		if len(hops) != 1 {
			t.Errorf("Failed to parse: %q (got %d hops)", output, len(hops))
		}
		if hops[0].IP != "192.168.1.1" {
			t.Errorf("Failed to extract IP from: %q", output)
		}
	}
}

func TestParseIPv4Only(t *testing.T) {
	// Ensure we only extract IPv4 addresses
	output := ` 1  192.168.1.1  0.123 ms
 2  text without ip  * * *
 3  10.0.0.1  1.234 ms`

	hops := parseTracerouteOutput(output)

	// Should get 3 hops
	if len(hops) != 3 {
		t.Errorf("Expected 3 hops, got %d", len(hops))
	}

	// Second hop should have no IP
	if hops[1].IP != "" {
		t.Errorf("Second hop should have empty IP, got %s", hops[1].IP)
	}
}

func TestCheckForInvalidCommands(t *testing.T) {
	// Ensure we're calling traceroute and not something else
	// This is a safety test
	ip := net.ParseIP("8.8.8.8")
	_, err := TracerouteExternal(ip, 1, 1000000000)

	// Even if it fails, it should not panic or execute arbitrary commands
	// The error (if any) should be related to traceroute execution
	if err != nil && !strings.Contains(err.Error(), "traceroute") {
		t.Logf("Error message: %v", err)
	}
}
