package main

import (
	"fmt"
	"net"
	"testing"
)

func TestNewIPRangeIterator(t *testing.T) {
	start := net.ParseIP("192.168.1.1")
	end := net.ParseIP("192.168.1.10")

	iter, err := NewIPRangeIterator(start, end)
	if err != nil {
		t.Fatalf("NewIPRangeIterator failed: %v", err)
	}

	expectedTotal := uint64(10)
	if iter.Total != expectedTotal {
		t.Errorf("Expected total %d, got %d", expectedTotal, iter.Total)
	}

	// Verify start and end
	if iter.Start == 0 || iter.End == 0 {
		t.Error("Start and End should be non-zero")
	}

	if iter.Start > iter.End {
		t.Error("Start should be <= End")
	}
}

func TestNewIPRangeIteratorSingleIP(t *testing.T) {
	ip := net.ParseIP("8.8.8.8")

	iter, err := NewIPRangeIterator(ip, ip)
	if err != nil {
		t.Fatalf("NewIPRangeIterator failed: %v", err)
	}

	if iter.Total != 1 {
		t.Errorf("Expected total 1 for single IP, got %d", iter.Total)
	}
}

func TestNewIPRangeIteratorInvalidOrder(t *testing.T) {
	start := net.ParseIP("192.168.1.10")
	end := net.ParseIP("192.168.1.1")

	_, err := NewIPRangeIterator(start, end)
	if err == nil {
		t.Error("Expected error when start > end")
	}
}

func TestNewIPRangeIteratorIPv6(t *testing.T) {
	start := net.ParseIP("2001:db8::1")
	end := net.ParseIP("2001:db8::10")

	_, err := NewIPRangeIterator(start, end)
	if err == nil {
		t.Error("Expected error for IPv6 addresses")
	}
}

func TestNewIPRangeIteratorFromString(t *testing.T) {
	tests := []struct {
		name          string
		rangeStr      string
		expectedTotal uint64
		expectError   bool
	}{
		{
			name:          "CIDR /24",
			rangeStr:      "192.168.1.0/24",
			expectedTotal: 256,
			expectError:   false,
		},
		{
			name:          "CIDR /30",
			rangeStr:      "10.0.0.0/30",
			expectedTotal: 4,
			expectError:   false,
		},
		{
			name:          "IP Range",
			rangeStr:      "192.168.1.1-192.168.1.10",
			expectedTotal: 10,
			expectError:   false,
		},
		{
			name:          "Single IP",
			rangeStr:      "8.8.8.8",
			expectedTotal: 1,
			expectError:   false,
		},
		{
			name:        "Invalid format",
			rangeStr:    "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iter, err := NewIPRangeIteratorFromString(tt.rangeStr)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if iter.Total != tt.expectedTotal {
				t.Errorf("Expected total %d, got %d", tt.expectedTotal, iter.Total)
			}
		})
	}
}

func TestIPRangeIteratorGenerate(t *testing.T) {
	start := net.ParseIP("192.168.1.1")
	end := net.ParseIP("192.168.1.5")

	iter, _ := NewIPRangeIterator(start, end)
	ch := iter.Generate(nil)

	count := 0
	ips := make([]string, 0)

	for ip := range ch {
		count++
		ips = append(ips, ip.String())
	}

	if count != 5 {
		t.Errorf("Expected 5 IPs, got %d", count)
	}

	// Verify sequential order
	expectedIPs := []string{
		"192.168.1.1",
		"192.168.1.2",
		"192.168.1.3",
		"192.168.1.4",
		"192.168.1.5",
	}

	for i, expected := range expectedIPs {
		if ips[i] != expected {
			t.Errorf("Expected IP %s at position %d, got %s", expected, i, ips[i])
		}
	}
}

func TestIPRangeIteratorGenerateWithExclusion(t *testing.T) {
	start := net.ParseIP("192.168.1.1")
	end := net.ParseIP("192.168.1.10")

	iter, _ := NewIPRangeIterator(start, end)

	// Exclude IPs 3-5
	excludeRanges := []CompletedRange{
		{
			Start: IPToUint32(net.ParseIP("192.168.1.3")),
			End:   IPToUint32(net.ParseIP("192.168.1.5")),
		},
	}

	ch := iter.Generate(excludeRanges)

	ips := make([]string, 0)
	for ip := range ch {
		ips = append(ips, ip.String())
	}

	// Should have 7 IPs (10 total - 3 excluded)
	expectedCount := 7
	if len(ips) != expectedCount {
		t.Errorf("Expected %d IPs, got %d", expectedCount, len(ips))
	}

	// Verify excluded IPs are not present
	excludedIPs := map[string]bool{
		"192.168.1.3": true,
		"192.168.1.4": true,
		"192.168.1.5": true,
	}

	for _, ip := range ips {
		if excludedIPs[ip] {
			t.Errorf("Excluded IP %s should not be in output", ip)
		}
	}

	// Verify expected IPs are present
	expectedIPs := []string{
		"192.168.1.1",
		"192.168.1.2",
		"192.168.1.6",
		"192.168.1.7",
		"192.168.1.8",
		"192.168.1.9",
		"192.168.1.10",
	}

	for i, expected := range expectedIPs {
		if ips[i] != expected {
			t.Errorf("Expected IP %s at position %d, got %s", expected, i, ips[i])
		}
	}
}

func TestIPRangeIteratorGenerateMultipleExclusions(t *testing.T) {
	start := net.ParseIP("10.0.0.1")
	end := net.ParseIP("10.0.0.20")

	iter, _ := NewIPRangeIterator(start, end)

	excludeRanges := []CompletedRange{
		{Start: IPToUint32(net.ParseIP("10.0.0.3")), End: IPToUint32(net.ParseIP("10.0.0.5"))},
		{Start: IPToUint32(net.ParseIP("10.0.0.10")), End: IPToUint32(net.ParseIP("10.0.0.12"))},
		{Start: IPToUint32(net.ParseIP("10.0.0.18")), End: IPToUint32(net.ParseIP("10.0.0.19"))},
	}

	ch := iter.Generate(excludeRanges)

	count := 0
	for range ch {
		count++
	}

	// 20 total - 3 (3-5) - 3 (10-12) - 2 (18-19) = 12
	expectedCount := 12
	if count != expectedCount {
		t.Errorf("Expected %d IPs after exclusions, got %d", expectedCount, count)
	}
}

func TestIPRangeIteratorCount(t *testing.T) {
	tests := []struct {
		name     string
		start    string
		end      string
		expected uint64
	}{
		{"Single IP", "192.168.1.1", "192.168.1.1", 1},
		{"10 IPs", "192.168.1.1", "192.168.1.10", 10},
		{"256 IPs", "192.168.1.0", "192.168.1.255", 256},
		{"Full /16", "192.168.0.0", "192.168.255.255", 65536},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := net.ParseIP(tt.start)
			end := net.ParseIP(tt.end)

			iter, err := NewIPRangeIterator(start, end)
			if err != nil {
				t.Fatalf("NewIPRangeIterator failed: %v", err)
			}

			if iter.Count() != tt.expected {
				t.Errorf("Expected count %d, got %d", tt.expected, iter.Count())
			}
		})
	}
}

func TestIPToUint32(t *testing.T) {
	tests := []struct {
		ip       string
		expected uint32
	}{
		{"0.0.0.0", 0},
		{"0.0.0.1", 1},
		{"0.0.1.0", 256},
		{"1.0.0.0", 16777216},
		{"192.168.1.1", 3232235777},
		{"255.255.255.255", 4294967295},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			result := IPToUint32(ip)

			if result != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestIPToUint32IPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	result := IPToUint32(ip)

	if result != 0 {
		t.Errorf("Expected 0 for IPv6, got %d", result)
	}
}

func TestUint32ToIP(t *testing.T) {
	tests := []struct {
		value    uint32
		expected string
	}{
		{0, "0.0.0.0"},
		{1, "0.0.0.1"},
		{256, "0.0.1.0"},
		{16777216, "1.0.0.0"},
		{3232235777, "192.168.1.1"},
		{4294967295, "255.255.255.255"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			ip := Uint32ToIP(tt.value)

			if ip.String() != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, ip.String())
			}
		})
	}
}

func TestIPConversionRoundTrip(t *testing.T) {
	tests := []string{
		"0.0.0.0",
		"8.8.8.8",
		"192.168.1.1",
		"10.0.0.1",
		"255.255.255.255",
	}

	for _, ipStr := range tests {
		t.Run(ipStr, func(t *testing.T) {
			ip := net.ParseIP(ipStr)
			ipInt := IPToUint32(ip)
			ipBack := Uint32ToIP(ipInt)

			if ipBack.String() != ipStr {
				t.Errorf("Round trip failed: %s -> %d -> %s", ipStr, ipInt, ipBack.String())
			}
		})
	}
}

func TestIPRangeIteratorGenerateLargeRange(t *testing.T) {
	// Test with a /24 network (256 IPs)
	start := net.ParseIP("10.0.0.0")
	end := net.ParseIP("10.0.0.255")

	iter, _ := NewIPRangeIterator(start, end)
	ch := iter.Generate(nil)

	count := 0
	for range ch {
		count++
	}

	if count != 256 {
		t.Errorf("Expected 256 IPs, got %d", count)
	}
}

func TestIPRangeIteratorGenerateEmptyAfterExclusion(t *testing.T) {
	start := net.ParseIP("192.168.1.1")
	end := net.ParseIP("192.168.1.5")

	iter, _ := NewIPRangeIterator(start, end)

	// Exclude entire range
	excludeRanges := []CompletedRange{
		{
			Start: IPToUint32(net.ParseIP("192.168.1.1")),
			End:   IPToUint32(net.ParseIP("192.168.1.5")),
		},
	}

	ch := iter.Generate(excludeRanges)

	count := 0
	for range ch {
		count++
	}

	if count != 0 {
		t.Errorf("Expected 0 IPs after full exclusion, got %d", count)
	}
}

func TestIPRangeIteratorGenerateShuffled(t *testing.T) {
	start := net.ParseIP("192.168.1.1")
	end := net.ParseIP("192.168.1.10")

	iter, _ := NewIPRangeIterator(start, end)
	seed := uint64(42)

	ch := iter.GenerateShuffled(nil, seed)

	ips := make([]string, 0)
	for ip := range ch {
		ips = append(ips, ip.String())
	}

	// Should have all 10 IPs
	if len(ips) != 10 {
		t.Errorf("Expected 10 IPs, got %d", len(ips))
	}

	// Check all IPs are in range
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		ipInt := IPToUint32(ip)
		if ipInt < IPToUint32(start) || ipInt > IPToUint32(end) {
			t.Errorf("IP %s is out of range", ipStr)
		}
	}

	// Should not be sequential (with high probability)
	sequential := true
	for i := 1; i < len(ips); i++ {
		prev := IPToUint32(net.ParseIP(ips[i-1]))
		curr := IPToUint32(net.ParseIP(ips[i]))
		if curr != prev+1 {
			sequential = false
			break
		}
	}

	if sequential {
		t.Error("Shuffled IPs appear to be sequential")
	}
}

func TestIPRangeIteratorGenerateShuffledBijection(t *testing.T) {
	// Test that shuffled generation produces all IPs exactly once
	start := net.ParseIP("10.0.0.1")
	end := net.ParseIP("10.0.0.100")

	iter, _ := NewIPRangeIterator(start, end)
	seed := uint64(123)

	ch := iter.GenerateShuffled(nil, seed)

	seen := make(map[string]bool)
	count := 0

	for ip := range ch {
		ipStr := ip.String()
		if seen[ipStr] {
			t.Errorf("Duplicate IP in shuffled output: %s", ipStr)
		}
		seen[ipStr] = true
		count++
	}

	if count != 100 {
		t.Errorf("Expected 100 unique IPs, got %d", count)
	}
}

func TestIPRangeIteratorGenerateShuffledWithExclusion(t *testing.T) {
	start := net.ParseIP("192.168.1.1")
	end := net.ParseIP("192.168.1.20")

	iter, _ := NewIPRangeIterator(start, end)

	// Exclude IPs 5-10
	excludeRanges := []CompletedRange{
		{
			Start: IPToUint32(net.ParseIP("192.168.1.5")),
			End:   IPToUint32(net.ParseIP("192.168.1.10")),
		},
	}

	seed := uint64(999)
	ch := iter.GenerateShuffled(excludeRanges, seed)

	ips := make([]string, 0)
	for ip := range ch {
		ips = append(ips, ip.String())
	}

	// Should have 14 IPs (20 - 6 excluded)
	expectedCount := 14
	if len(ips) != expectedCount {
		t.Errorf("Expected %d IPs after exclusion, got %d", expectedCount, len(ips))
	}

	// Verify excluded IPs are not present
	for i := 5; i <= 10; i++ {
		excludedIP := fmt.Sprintf("192.168.1.%d", i)
		for _, ip := range ips {
			if ip == excludedIP {
				t.Errorf("Excluded IP %s found in output", excludedIP)
			}
		}
	}
}

func TestIPRangeIteratorGenerateShuffledSameSeed(t *testing.T) {
	// Test that same seed produces same order
	start := net.ParseIP("10.0.0.1")
	end := net.ParseIP("10.0.0.50")

	iter1, _ := NewIPRangeIterator(start, end)
	iter2, _ := NewIPRangeIterator(start, end)

	seed := uint64(777)

	ch1 := iter1.GenerateShuffled(nil, seed)
	ch2 := iter2.GenerateShuffled(nil, seed)

	ips1 := make([]string, 0)
	ips2 := make([]string, 0)

	for ip := range ch1 {
		ips1 = append(ips1, ip.String())
	}

	for ip := range ch2 {
		ips2 = append(ips2, ip.String())
	}

	// Should produce identical sequences
	if len(ips1) != len(ips2) {
		t.Errorf("Different lengths: %d vs %d", len(ips1), len(ips2))
	}

	for i := range ips1 {
		if ips1[i] != ips2[i] {
			t.Errorf("Different IP at position %d: %s vs %s", i, ips1[i], ips2[i])
		}
	}
}

func TestIPRangeIteratorGenerateShuffledDifferentSeeds(t *testing.T) {
	// Test that different seeds produce different orders
	start := net.ParseIP("10.0.0.1")
	end := net.ParseIP("10.0.0.50")

	iter1, _ := NewIPRangeIterator(start, end)
	iter2, _ := NewIPRangeIterator(start, end)

	seed1 := uint64(111)
	seed2 := uint64(222)

	ch1 := iter1.GenerateShuffled(nil, seed1)
	ch2 := iter2.GenerateShuffled(nil, seed2)

	ips1 := make([]string, 0)
	ips2 := make([]string, 0)

	for ip := range ch1 {
		ips1 = append(ips1, ip.String())
	}

	for ip := range ch2 {
		ips2 = append(ips2, ip.String())
	}

	// Should differ on most positions
	differences := 0
	for i := range ips1 {
		if ips1[i] != ips2[i] {
			differences++
		}
	}

	// At least 80% should be different
	minDifferences := int(float64(len(ips1)) * 0.8)
	if differences < minDifferences {
		t.Errorf("Expected at least %d differences, got %d", minDifferences, differences)
	}
}

func TestIPRangeIteratorGenerateShuffledLargeRange(t *testing.T) {
	// Test with /24 network (256 IPs)
	start := net.ParseIP("10.0.1.0")
	end := net.ParseIP("10.0.1.255")

	iter, _ := NewIPRangeIterator(start, end)
	seed := uint64(12345)

	ch := iter.GenerateShuffled(nil, seed)

	count := 0
	seen := make(map[string]bool)

	for ip := range ch {
		if seen[ip.String()] {
			t.Errorf("Duplicate IP: %s", ip.String())
		}
		seen[ip.String()] = true
		count++
	}

	if count != 256 {
		t.Errorf("Expected 256 IPs, got %d", count)
	}
}

// TestIPRangeIteratorOnePer24 tests the one-per-24 sampling functionality
func TestIPRangeIteratorOnePer24(t *testing.T) {
	start := net.ParseIP("10.0.0.0")
	end := net.ParseIP("10.0.255.255")

	iter, err := NewIPRangeIterator(start, end)
	if err != nil {
		t.Fatalf("Failed to create iterator: %v", err)
	}

	// Original should have 65536 IPs (/16)
	if iter.Total != 65536 {
		t.Errorf("Expected 65536 IPs in /16, got %d", iter.Total)
	}

	// Sample one per /24
	sampled := iter.SampleOnePer24()

	// Should have 256 IPs (one per /24 block)
	if sampled.Total != 256 {
		t.Errorf("Expected 256 IPs (one per /24), got %d", sampled.Total)
	}

	// Generate IPs and verify they're all .0 addresses (first of each /24)
	ch := sampled.Generate(nil)
	count := 0
	for ip := range ch {
		ipInt := IPToUint32(ip)
		// Check that last 8 bits are 0 (first IP of /24 block)
		if ipInt&0xFF != 0 {
			t.Errorf("IP %s is not first of /24 block (last byte should be 0)", ip.String())
		}
		count++
	}

	if count != 256 {
		t.Errorf("Expected to generate 256 IPs, got %d", count)
	}
}

// TestIPRangeIteratorOnePer24Shuffle tests shuffling with one-per-24 sampling
func TestIPRangeIteratorOnePer24Shuffle(t *testing.T) {
	start := net.ParseIP("10.0.0.0")
	end := net.ParseIP("10.0.1.255")

	iter, err := NewIPRangeIterator(start, end)
	if err != nil {
		t.Fatalf("Failed to create iterator: %v", err)
	}

	// Original should have 512 IPs
	if iter.Total != 512 {
		t.Errorf("Expected 512 IPs, got %d", iter.Total)
	}

	// Sample one per /24 (should give us 2 /24 blocks)
	sampled := iter.SampleOnePer24()

	if sampled.Total != 2 {
		t.Errorf("Expected 2 /24 blocks, got %d", sampled.Total)
	}

	// Generate with shuffle
	ch := sampled.GenerateShuffled(nil, 42)
	ips := []string{}
	for ip := range ch {
		ips = append(ips, ip.String())
	}

	// Should have exactly 2 IPs
	if len(ips) != 2 {
		t.Errorf("Expected 2 IPs, got %d", len(ips))
	}

	// Both should be .0 addresses
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		ipInt := IPToUint32(ip)
		if ipInt&0xFF != 0 {
			t.Errorf("IP %s is not first of /24 block", ipStr)
		}
	}

	// Verify determinism - same seed should give same order
	ch2 := sampled.GenerateShuffled(nil, 42)
	ips2 := []string{}
	for ip := range ch2 {
		ips2 = append(ips2, ip.String())
	}

	if len(ips) != len(ips2) {
		t.Errorf("Different number of IPs with same seed")
	}

	for i := range ips {
		if ips[i] != ips2[i] {
			t.Errorf("Different order with same seed at index %d: %s vs %s", i, ips[i], ips2[i])
		}
	}
}

// TestIPRangeIteratorOnePer24FullSpace tests one-per-24 on full IPv4 space
func TestIPRangeIteratorOnePer24FullSpace(t *testing.T) {
	start := net.ParseIP("0.0.0.0")
	end := net.ParseIP("255.255.255.255")

	iter, err := NewIPRangeIterator(start, end)
	if err != nil {
		t.Fatalf("Failed to create iterator: %v", err)
	}

	// Full IPv4 space: 2^32 = 4,294,967,296
	expectedFull := uint64(4294967296)
	if iter.Total != expectedFull {
		t.Errorf("Expected %d IPs in full space, got %d", expectedFull, iter.Total)
	}

	// Sample one per /24
	sampled := iter.SampleOnePer24()

	// Should have 2^24 = 16,777,216 blocks (256x reduction)
	expectedSampled := uint64(16777216)
	if sampled.Total != expectedSampled {
		t.Errorf("Expected %d /24 blocks, got %d", expectedSampled, sampled.Total)
	}

	// Verify reduction factor
	reduction := float64(iter.Total) / float64(sampled.Total)
	if reduction < 255.9 || reduction > 256.1 {
		t.Errorf("Expected ~256x reduction, got %.2fx", reduction)
	}
}
