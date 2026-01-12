package main

import (
	"testing"
)

func TestNewBlackrock(t *testing.T) {
	rangeSize := uint64(1000)
	seed := uint64(12345)

	br, err := NewBlackrock(rangeSize, seed)
	if err != nil {
		t.Fatalf("NewBlackrock failed: %v", err)
	}

	if br == nil {
		t.Fatal("Expected non-nil Blackrock")
	}

	if br.range_ != rangeSize {
		t.Errorf("Expected range %d, got %d", rangeSize, br.range_)
	}

	if br.cipher == nil {
		t.Error("Expected cipher to be initialized")
	}
}

func TestBlackrockShuffleBijection(t *testing.T) {
	// Test that shuffle is bijective (one-to-one mapping)
	rangeSize := uint64(100)
	seed := uint64(42)

	br, err := NewBlackrock(rangeSize, seed)
	if err != nil {
		t.Fatalf("NewBlackrock failed: %v", err)
	}

	seen := make(map[uint64]bool)
	for i := uint64(0); i < rangeSize; i++ {
		shuffled := br.Shuffle(i)

		// Check output is in range
		if shuffled >= rangeSize {
			t.Errorf("Shuffled value %d is out of range [0, %d)", shuffled, rangeSize)
		}

		// Check for collisions (bijection requirement)
		if seen[shuffled] {
			t.Errorf("Collision detected: value %d appears twice", shuffled)
		}
		seen[shuffled] = true
	}

	// Verify all values were produced
	if len(seen) != int(rangeSize) {
		t.Errorf("Expected %d unique values, got %d", rangeSize, len(seen))
	}
}

func TestBlackrockShuffleUnshuffle(t *testing.T) {
	// Test that unshuffle reverses shuffle
	rangeSize := uint64(1000)
	seed := uint64(999)

	br, err := NewBlackrock(rangeSize, seed)
	if err != nil {
		t.Fatalf("NewBlackrock failed: %v", err)
	}

	// Test round-trip for various inputs
	testValues := []uint64{0, 1, 100, 500, 999}
	for _, val := range testValues {
		shuffled := br.Shuffle(val)
		unshuffled := br.Unshuffle(shuffled)

		if unshuffled != val {
			t.Errorf("Round-trip failed for %d: shuffle(%d) = %d, unshuffle(%d) = %d",
				val, val, shuffled, shuffled, unshuffled)
		}
	}
}

func TestBlackrockDifferentSeeds(t *testing.T) {
	// Test that different seeds produce different permutations
	rangeSize := uint64(100)
	seed1 := uint64(123)
	seed2 := uint64(456)

	br1, _ := NewBlackrock(rangeSize, seed1)
	br2, _ := NewBlackrock(rangeSize, seed2)

	differences := 0
	for i := uint64(0); i < rangeSize; i++ {
		val1 := br1.Shuffle(i)
		val2 := br2.Shuffle(i)

		if val1 != val2 {
			differences++
		}
	}

	// Should differ on most values (at least 80%)
	minDifferences := 80
	if differences < minDifferences {
		t.Errorf("Expected at least %d differences between seeds, got %d", minDifferences, differences)
	}
}

func TestBlackrockSameSeedConsistency(t *testing.T) {
	// Test that same seed produces same permutation
	rangeSize := uint64(100)
	seed := uint64(789)

	br1, _ := NewBlackrock(rangeSize, seed)
	br2, _ := NewBlackrock(rangeSize, seed)

	for i := uint64(0); i < rangeSize; i++ {
		val1 := br1.Shuffle(i)
		val2 := br2.Shuffle(i)

		if val1 != val2 {
			t.Errorf("Same seed produced different values at index %d: %d vs %d", i, val1, val2)
		}
	}
}

func TestBlackrockLargeRange(t *testing.T) {
	// Test with larger range (like a /16 network)
	rangeSize := uint64(65536)
	seed := uint64(12345)

	br, err := NewBlackrock(rangeSize, seed)
	if err != nil {
		t.Fatalf("NewBlackrock failed: %v", err)
	}

	// Sample test instead of full bijection (would be slow)
	testCount := 1000
	seen := make(map[uint64]bool)

	for i := 0; i < testCount; i++ {
		input := uint64(i)
		shuffled := br.Shuffle(input)

		// Check in range
		if shuffled >= rangeSize {
			t.Errorf("Shuffled value %d out of range", shuffled)
		}

		// Check no collisions in sample
		if seen[shuffled] {
			t.Errorf("Collision in sample at index %d", i)
		}
		seen[shuffled] = true
	}
}

func TestBlackrockZeroRange(t *testing.T) {
	// Edge case: zero range
	rangeSize := uint64(0)
	seed := uint64(123)

	br, err := NewBlackrock(rangeSize, seed)
	if err != nil {
		t.Fatalf("NewBlackrock failed: %v", err)
	}

	result := br.Shuffle(0)
	if result != 0 {
		t.Errorf("Expected 0 for zero range, got %d", result)
	}
}

func TestBlackrockSingleElement(t *testing.T) {
	// Edge case: range of 1
	rangeSize := uint64(1)
	seed := uint64(456)

	br, err := NewBlackrock(rangeSize, seed)
	if err != nil {
		t.Fatalf("NewBlackrock failed: %v", err)
	}

	result := br.Shuffle(0)
	if result != 0 {
		t.Errorf("Expected 0 for single element range, got %d", result)
	}
}

func TestBlackrockShuffleIP32(t *testing.T) {
	// Test the 32-bit IP-specific shuffle function
	rangeSize := uint64(256) // /24 network
	seed := uint64(777)

	br, err := NewBlackrock(rangeSize, seed)
	if err != nil {
		t.Fatalf("NewBlackrock failed: %v", err)
	}

	seen := make(map[uint32]bool)
	for i := uint32(0); i < 256; i++ {
		shuffled := br.ShuffleIP32(i)

		if seen[shuffled] {
			t.Errorf("Collision in ShuffleIP32 at %d", i)
		}
		seen[shuffled] = true
	}

	if len(seen) != 256 {
		t.Errorf("Expected 256 unique values, got %d", len(seen))
	}
}

func TestBlackrockDistribution(t *testing.T) {
	// Test that shuffle produces good distribution
	rangeSize := uint64(100)
	seed := uint64(999)

	br, _ := NewBlackrock(rangeSize, seed)

	// Count how many outputs fall in each quartile
	quartiles := make([]int, 4)
	for i := uint64(0); i < rangeSize; i++ {
		shuffled := br.Shuffle(i)
		quartile := int(shuffled * 4 / rangeSize)
		quartiles[quartile]++
	}

	// Each quartile should have roughly 25 values (±10)
	expected := 25
	tolerance := 10

	for i, count := range quartiles {
		if count < expected-tolerance || count > expected+tolerance {
			t.Errorf("Quartile %d has %d values, expected %d±%d", i, count, expected, tolerance)
		}
	}
}

func TestBlackrockCycleWalking(t *testing.T) {
	// Test cycle-walking: when range is not power of 2
	rangeSize := uint64(1000) // Not a power of 2
	seed := uint64(333)

	br, _ := NewBlackrock(rangeSize, seed)

	// All values should be < rangeSize
	for i := uint64(0); i < rangeSize; i++ {
		shuffled := br.Shuffle(i)
		if shuffled >= rangeSize {
			t.Errorf("Cycle-walking failed: shuffle(%d) = %d >= %d", i, shuffled, rangeSize)
		}
	}
}

func TestBlackrockFullIPSpace(t *testing.T) {
	// Test with full 32-bit space (like 0.0.0.0/0)
	rangeSize := uint64(1 << 32) // 4.3 billion
	seed := uint64(12345)

	br, err := NewBlackrock(rangeSize, seed)
	if err != nil {
		t.Fatalf("NewBlackrock failed: %v", err)
	}

	// Can't test full bijection (too slow), but test a sample
	testInputs := []uint64{0, 1, 1000, 1000000, 0xFFFFFFFF}
	for _, input := range testInputs {
		shuffled := br.Shuffle(input)
		unshuffled := br.Unshuffle(shuffled)

		if unshuffled != input {
			t.Errorf("Full space round-trip failed for %d", input)
		}
	}
}

func BenchmarkBlackrockShuffle(b *testing.B) {
	rangeSize := uint64(1000000)
	seed := uint64(12345)
	br, _ := NewBlackrock(rangeSize, seed)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		br.Shuffle(uint64(i % 1000000))
	}
}

func BenchmarkBlackrockShuffleIP32(b *testing.B) {
	rangeSize := uint64(1 << 32)
	seed := uint64(12345)
	br, _ := NewBlackrock(rangeSize, seed)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		br.ShuffleIP32(uint32(i))
	}
}
