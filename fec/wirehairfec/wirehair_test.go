package wirehairfec

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

	engine := NewWirehairFECV2()
	enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), shardLen)
	defer enc.Close()

	dec := engine.GetDecoder(int64(len(payload)), shardLen)
	defer dec.Close()

	blockCount := uint32((len(payload) + shardLen - 1) / shardLen)
	done := false
	for id := uint32(0); id < blockCount+64; id++ {
		shard := enc.GetShard(id)
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

	engine := NewWirehairFECV2()
	enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), shardLen)
	defer enc.Close()

	dec := engine.GetDecoder(int64(len(payload)), shardLen)
	defer dec.Close()

	shard := enc.GetShard(7)
	if !dec.PutShard(7, shard) {
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

func TestDecoderIgnoresDuplicateOriginalShard(t *testing.T) {
	t.Parallel()

	const shardLen = 50
	payload := bytes.Repeat([]byte("abc"), 100)

	engine := NewWirehairFECV2()
	enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), shardLen)
	defer enc.Close()

	dec := engine.GetDecoder(int64(len(payload)), shardLen)
	defer dec.Close()

	original := enc.GetShard(0)
	if dec.PutShard(0, original) {
		t.Fatal("decoder should not be complete after first original shard")
	}
	if dec.PutShard(0, original) {
		t.Fatal("duplicate original shard should not complete decoding")
	}

	done := false
	for id := uint32(1); id < 64; id++ {
		shard := enc.GetShard(id)
		if dec.PutShard(id, shard) {
			done = true
			break
		}
	}
	if !done {
		t.Fatal("decoder did not recover payload after duplicate shard")
	}
}

func TestMaxInputSize(t *testing.T) {
	t.Parallel()

	engine := &engine{maxSourceBlocks: defaultMaxSourceBlocks}
	if got, want := engine.MaxInputSize(1300), uint32(defaultMaxSourceBlocks*1300); got != want {
		t.Fatalf("unexpected max input size: got %d want %d", got, want)
	}
}

func TestLoadMaxSourceBlocksFromEnv(t *testing.T) {
	t.Parallel()

	if got := loadMaxSourceBlocksFromEnv(func(string) (string, bool) { return "", false }); got != defaultMaxSourceBlocks {
		t.Fatalf("unexpected default max source blocks: got %d want %d", got, defaultMaxSourceBlocks)
	}
	if got := loadMaxSourceBlocksFromEnv(func(string) (string, bool) { return "8192", true }); got != 8192 {
		t.Fatalf("unexpected overridden max source blocks: got %d want 8192", got)
	}
}

func TestLoadMaxSourceBlocksFromEnvInvalid(t *testing.T) {
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
				if !strings.Contains(r.(string), maxSourceBlocksEnv) {
					t.Fatalf("unexpected panic message: %v", r)
				}
			}()

			_ = loadMaxSourceBlocksFromEnv(func(string) (string, bool) { return value, true })
		})
	}
}
