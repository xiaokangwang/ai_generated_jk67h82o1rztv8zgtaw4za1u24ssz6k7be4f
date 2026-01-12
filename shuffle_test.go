package main

import (
	"net"
	"testing"
)

func TestShuffleIPs(t *testing.T) {
	// Create a list of IPs
	original := []net.IP{
		net.ParseIP("192.168.1.1"),
		net.ParseIP("192.168.1.2"),
		net.ParseIP("192.168.1.3"),
		net.ParseIP("192.168.1.4"),
		net.ParseIP("192.168.1.5"),
		net.ParseIP("192.168.1.6"),
		net.ParseIP("192.168.1.7"),
		net.ParseIP("192.168.1.8"),
		net.ParseIP("192.168.1.9"),
		net.ParseIP("192.168.1.10"),
	}

	// Make a copy
	ips := make([]net.IP, len(original))
	copy(ips, original)

	// Shuffle
	ShuffleIPs(ips)

	// Verify all IPs are still present
	if len(ips) != len(original) {
		t.Errorf("Length changed after shuffle: expected %d, got %d", len(original), len(ips))
	}

	// Check that all original IPs are still in the list
	for _, origIP := range original {
		found := false
		for _, shuffledIP := range ips {
			if origIP.Equal(shuffledIP) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("IP %s missing after shuffle", origIP)
		}
	}

	// Check that order changed (with high probability)
	// For 10 items, probability of same order after shuffle is 1/10! = 1/3628800
	sameOrder := true
	for i := range ips {
		if !ips[i].Equal(original[i]) {
			sameOrder = false
			break
		}
	}

	if sameOrder {
		t.Error("IPs are in same order after shuffle (extremely unlikely, might indicate shuffle not working)")
	}
}

func TestShuffleIPsSmall(t *testing.T) {
	// Test with small lists
	tests := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"single", 1},
		{"two", 2},
		{"three", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ips := make([]net.IP, tt.size)
			for i := 0; i < tt.size; i++ {
				ips[i] = net.ParseIP("192.168.1.1")
			}

			// Should not panic
			ShuffleIPs(ips)

			if len(ips) != tt.size {
				t.Errorf("Size changed: expected %d, got %d", tt.size, len(ips))
			}
		})
	}
}

func TestShuffleIPsUniqueness(t *testing.T) {
	// Test that shuffle produces different results on multiple runs
	original := []net.IP{
		net.ParseIP("10.0.0.1"),
		net.ParseIP("10.0.0.2"),
		net.ParseIP("10.0.0.3"),
		net.ParseIP("10.0.0.4"),
		net.ParseIP("10.0.0.5"),
	}

	results := make([]string, 5)
	for run := 0; run < 5; run++ {
		ips := make([]net.IP, len(original))
		copy(ips, original)
		ShuffleIPs(ips)

		// Create a string representation of the order
		order := ""
		for _, ip := range ips {
			order += ip.String() + ","
		}
		results[run] = order
	}

	// Check that we got at least 2 different orderings
	// (with 5 runs and 5! = 120 possible orderings, this is very likely)
	uniqueCount := 0
	for i := 0; i < len(results); i++ {
		isUnique := true
		for j := 0; j < i; j++ {
			if results[i] == results[j] {
				isUnique = false
				break
			}
		}
		if isUnique {
			uniqueCount++
		}
	}

	if uniqueCount < 2 {
		t.Errorf("Expected at least 2 different shuffle results, got %d unique orderings", uniqueCount)
	}
}

func TestShuffleIPsPreservesData(t *testing.T) {
	// Test with actual IPv4 addresses
	original := []net.IP{
		net.ParseIP("8.8.8.8"),
		net.ParseIP("1.1.1.1"),
		net.ParseIP("208.67.222.222"),
		net.ParseIP("9.9.9.9"),
	}

	ips := make([]net.IP, len(original))
	copy(ips, original)

	ShuffleIPs(ips)

	// Create maps to check presence
	originalMap := make(map[string]bool)
	for _, ip := range original {
		originalMap[ip.String()] = true
	}

	shuffledMap := make(map[string]bool)
	for _, ip := range ips {
		shuffledMap[ip.String()] = true
	}

	// Check both maps have same content
	if len(originalMap) != len(shuffledMap) {
		t.Error("Number of unique IPs changed after shuffle")
	}

	for ipStr := range originalMap {
		if !shuffledMap[ipStr] {
			t.Errorf("IP %s missing after shuffle", ipStr)
		}
	}
}

func TestCryptoRandInt(t *testing.T) {
	// Test cryptoRandInt function
	tests := []struct {
		n    int
		want string
	}{
		{0, "zero"},
		{1, "one"},
		{10, "ten"},
		{100, "hundred"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				result := cryptoRandInt(tt.n)

				if tt.n <= 0 {
					if result != 0 {
						t.Errorf("cryptoRandInt(%d) = %d, want 0", tt.n, result)
					}
				} else {
					if result < 0 || result >= tt.n {
						t.Errorf("cryptoRandInt(%d) = %d, want value in [0, %d)", tt.n, result, tt.n)
					}
				}
			}
		})
	}
}

func TestShuffleIPsLarge(t *testing.T) {
	// Test with a large list to ensure performance is acceptable
	size := 1000
	ips := make([]net.IP, size)
	for i := 0; i < size; i++ {
		// Create IPs like 10.0.x.y
		ips[i] = net.IPv4(10, 0, byte(i/256), byte(i%256))
	}

	// Shuffle should complete quickly
	ShuffleIPs(ips)

	// Verify size unchanged
	if len(ips) != size {
		t.Errorf("Size changed after shuffle: expected %d, got %d", size, len(ips))
	}

	// Verify all IPs are unique (no duplicates introduced)
	seen := make(map[string]bool)
	for _, ip := range ips {
		ipStr := ip.String()
		if seen[ipStr] {
			t.Errorf("Duplicate IP after shuffle: %s", ipStr)
		}
		seen[ipStr] = true
	}

	if len(seen) != size {
		t.Errorf("Expected %d unique IPs, got %d", size, len(seen))
	}
}
