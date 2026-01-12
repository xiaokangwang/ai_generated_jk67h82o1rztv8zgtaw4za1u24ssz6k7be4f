package main

import (
	"net"
	"testing"
	"time"
)

func TestPing(t *testing.T) {
	// Test with Google DNS (should be reachable)
	ip := net.ParseIP("8.8.8.8")
	timeout := 2 * time.Second

	result, err := Ping(ip, timeout)
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	if result.DestIP != "8.8.8.8" {
		t.Errorf("Expected dest IP 8.8.8.8, got %s", result.DestIP)
	}

	if !result.Reachable {
		t.Errorf("Expected 8.8.8.8 to be reachable")
	}

	if result.RTT == 0 {
		t.Errorf("Expected non-zero RTT for reachable host")
	}

	if result.Duration == 0 {
		t.Errorf("Expected non-zero duration")
	}
}

func TestPingUnreachable(t *testing.T) {
	// Test with unreachable IP (reserved IP in documentation range)
	ip := net.ParseIP("192.0.2.1")
	timeout := 1 * time.Second

	result, err := Ping(ip, timeout)
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	if result.DestIP != "192.0.2.1" {
		t.Errorf("Expected dest IP 192.0.2.1, got %s", result.DestIP)
	}

	// This IP is typically unreachable (documentation range)
	// But we just check that we got a result without error
	if result.Duration == 0 {
		t.Errorf("Expected non-zero duration")
	}
}

func TestPingLocalhost(t *testing.T) {
	// Test with localhost (should always be reachable)
	ip := net.ParseIP("127.0.0.1")
	timeout := 1 * time.Second

	result, err := Ping(ip, timeout)
	if err != nil {
		t.Fatalf("Ping localhost failed: %v", err)
	}

	if result.DestIP != "127.0.0.1" {
		t.Errorf("Expected dest IP 127.0.0.1, got %s", result.DestIP)
	}

	if !result.Reachable {
		t.Errorf("Expected localhost to be reachable")
	}

	// Localhost should have very low RTT
	if result.RTT > 10*time.Millisecond {
		t.Logf("Warning: Localhost RTT unexpectedly high: %v", result.RTT)
	}
}

func TestCheckPingBinary(t *testing.T) {
	// This should pass on most systems
	if !CheckPingBinary() {
		t.Skip("ping binary not found, skipping test")
	}
}

func TestPingTTLField(t *testing.T) {
	// Test that TTL field represents the outgoing packet TTL (64)
	ip := net.ParseIP("127.0.0.1")
	timeout := 1 * time.Second

	result, err := Ping(ip, timeout)
	if err != nil {
		t.Fatalf("Ping localhost failed: %v", err)
	}

	if result.TTL != 64 {
		t.Errorf("Expected TTL=64 (outgoing packet TTL), got %d", result.TTL)
	}

	if !result.Reachable {
		t.Errorf("Expected localhost to be reachable")
	}
}

func TestParsePingOutput(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		expectedRTT time.Duration
	}{
		{
			name:        "Standard format",
			output:      "64 bytes from 8.8.8.8: icmp_seq=1 ttl=117 time=1.23 ms",
			expectedRTT: 1230 * time.Microsecond,
		},
		{
			name:        "Decimal time",
			output:      "64 bytes from 1.1.1.1: icmp_seq=1 ttl=58 time=10.5 ms",
			expectedRTT: 10500 * time.Microsecond,
		},
		{
			name:        "Sub-millisecond",
			output:      "64 bytes from 127.0.0.1: icmp_seq=1 ttl=64 time=0.123 ms",
			expectedRTT: 123 * time.Microsecond,
		},
		{
			name:        "No match",
			output:      "Request timeout for icmp_seq 0",
			expectedRTT: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rtt := parsePingOutput(tt.output)

			// Check RTT
			diff := rtt - tt.expectedRTT
			if diff < 0 {
				diff = -diff
			}
			if diff > time.Microsecond {
				t.Errorf("parsePingOutput(%q) RTT = %v, want %v", tt.output, rtt, tt.expectedRTT)
			}
		})
	}
}
