package raptorqfec

import (
	"bytes"
	"io"
	"math/rand"
	"strings"
	"testing"
)

func TestRoundTripLargePayload(t *testing.T) {
	t.Parallel()

	const shardLen = 1024
	payload := make([]byte, 32*1024+137)
	rng := rand.New(rand.NewSource(1))
	if _, err := rng.Read(payload); err != nil {
		t.Fatal(err)
	}

	engine := NewRaptorQFECV2()
	enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), shardLen)
	defer enc.Close()

	dec := engine.GetDecoder(int64(len(payload)), shardLen)
	defer dec.Close()

	done := false
	for i := 0; i < 256; i++ {
		id := rng.Uint32()
		shard := enc.GetShard(id)
		if len(shard) != shardLen {
			t.Fatalf("unexpected shard size %d", len(shard))
		}
		if dec.PutShard(id, shard) {
			done = true
			break
		}
	}
	if !done {
		t.Fatal("decoder did not recover payload")
	}

	got, err := io.ReadAll(dec.GetIn())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("decoded payload mismatch")
	}
}

func TestRoundTripSmallPayload(t *testing.T) {
	t.Parallel()

	const shardLen = 1024
	payload := []byte("small payload")

	engine := NewRaptorQFECV2()
	enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), shardLen)
	defer enc.Close()

	dec := engine.GetDecoder(int64(len(payload)), shardLen)
	defer dec.Close()

	shard := enc.GetShard(42)
	if !dec.PutShard(42, shard) {
		t.Fatal("small decoder should complete on first shard")
	}

	got, err := io.ReadAll(dec.GetIn())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("decoded payload mismatch")
	}
}

func TestMaxInputSize(t *testing.T) {
	t.Parallel()

	engine := &engine{maxSourceSymbols: defaultMaxSourceSymbols}
	if got, want := engine.MaxInputSize(1300), uint32(defaultMaxSourceSymbols*1300); got != want {
		t.Fatalf("unexpected max input size: got %d want %d", got, want)
	}
}

func TestLoadMaxSourceSymbolsFromEnv(t *testing.T) {
	t.Parallel()

	if got := loadMaxSourceSymbolsFromEnv(func(string) (string, bool) { return "", false }); got != defaultMaxSourceSymbols {
		t.Fatalf("unexpected default max source symbols: got %d want %d", got, defaultMaxSourceSymbols)
	}
	if got := loadMaxSourceSymbolsFromEnv(func(string) (string, bool) { return "2048", true }); got != 2048 {
		t.Fatalf("unexpected overridden max source symbols: got %d want 2048", got)
	}
}

func TestLoadMaxSourceSymbolsFromEnvInvalid(t *testing.T) {
	t.Parallel()

	cases := []string{
		"0",
		"abc",
		"999999",
	}

	for _, value := range cases {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic")
				}
				if !strings.Contains(r.(string), maxSourceSymbolsEnv) {
					t.Fatalf("unexpected panic message: %v", r)
				}
			}()

			_ = loadMaxSourceSymbolsFromEnv(func(string) (string, bool) { return value, true })
		})
	}
}
