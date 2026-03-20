//go:build wirehairrepro
// +build wirehairrepro

package wirehair

import (
	"bytes"
	"testing"
)

func TestReproduceDuplicateOriginalBlockPanic(t *testing.T) {
	payload := bytes.Repeat([]byte("abc"), 100)
	const blockBytes uint32 = 50

	enc, err := NewEncoder(payload, blockBytes)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}

	dec, err := NewDecoder(uint64(len(payload)), blockBytes)
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}

	original := make([]byte, blockBytes)
	n, err := enc.Encode(0, original)
	if err != nil {
		t.Fatalf("encode block 0: %v", err)
	}
	original = original[:n]

	if _, err := dec.Decode(0, original); err != nil {
		t.Fatalf("first original decode: %v", err)
	}
	if _, err := dec.Decode(0, original); err != nil {
		t.Fatalf("duplicate original decode returned error instead of reproducing panic: %v", err)
	}

	for id := uint32(1); id < 64; id++ {
		block := make([]byte, blockBytes)
		n, err := enc.Encode(id, block)
		if err != nil {
			t.Fatalf("encode block %d: %v", id, err)
		}
		// The translated decoder eventually panics after this duplicate original
		// block corrupts internal state. This test intentionally does not recover.
		_, _ = dec.Decode(id, block[:n])
	}

	t.Fatal("expected decoder panic after duplicate original block, but it did not occur")
}
