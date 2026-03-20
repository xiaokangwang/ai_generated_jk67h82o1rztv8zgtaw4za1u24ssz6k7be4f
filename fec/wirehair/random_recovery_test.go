package wirehair

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestRecoverRandomPayloadWithMissingOriginalBlocks(t *testing.T) {
	t.Parallel()

	const (
		messageBytes = 96*1024 + 321
		blockBytes   = 1024
	)

	payload := randomTestMessage(t, messageBytes, 1)
	enc, err := NewEncoder(payload, blockBytes)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}

	n := uint32((len(payload) + int(blockBytes) - 1) / int(blockBytes))
	shards := encodeBlocksForIDs(t, enc, shardIDsWithMissingOriginals(n, 2))

	dec, err := NewDecoder(uint64(len(payload)), blockBytes)
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}

	decodeUntilReadyBasic(t, dec, shards)
	assertRecoveredMessage(t, dec, payload)
}

func TestRecoverRandomPayloadWithAllOriginalBlocksMissing(t *testing.T) {
	t.Parallel()

	const (
		messageBytes = 80*1024 + 777
		blockBytes   = 1024
	)

	payload := randomTestMessage(t, messageBytes, 2)
	enc, err := NewEncoder(payload, blockBytes)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}

	n := uint32((len(payload) + int(blockBytes) - 1) / int(blockBytes))
	shards := encodeBlocksForIDs(t, enc, repairOnlyShardIDs(n))

	dec, err := NewDecoder(uint64(len(payload)), blockBytes)
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}

	decodeUntilReadyBasic(t, dec, shards)
	assertRecoveredMessage(t, dec, payload)
}

func randomTestMessage(t *testing.T, size int, seed int64) []byte {
	t.Helper()

	message := make([]byte, size)
	rng := rand.New(rand.NewSource(seed))
	if _, err := rng.Read(message); err != nil {
		t.Fatalf("read random payload: %v", err)
	}
	return message
}

func shardIDsWithMissingOriginals(n uint32, seed int64) []uint32 {
	ids := make([]uint32, 0, n+16)
	for id := uint32(0); id < n; id++ {
		if id%4 == 1 {
			continue
		}
		ids = append(ids, id)
	}
	for id := n; len(ids) < int(n)+12; id++ {
		ids = append(ids, id)
	}
	shuffleShardIDs(ids, seed)
	return ids
}

func repairOnlyShardIDs(n uint32) []uint32 {
	ids := make([]uint32, 0, n+16)
	for id := n; len(ids) < int(n)+16; id++ {
		ids = append(ids, id)
	}
	return ids
}

func shuffleShardIDs(ids []uint32, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(ids), func(i, j int) {
		ids[i], ids[j] = ids[j], ids[i]
	})
}

func encodeBlocksForIDs(t *testing.T, enc *Encoder, ids []uint32) []struct {
	id   uint32
	data []byte
} {
	t.Helper()

	shards := make([]struct {
		id   uint32
		data []byte
	}, 0, len(ids))

	blockBytes := uint32(enc.codec.blockBytes)
	for _, id := range ids {
		block := make([]byte, blockBytes)
		n, err := enc.Encode(id, block)
		if err != nil {
			t.Fatalf("encode block %d: %v", id, err)
		}
		shards = append(shards, struct {
			id   uint32
			data []byte
		}{
			id:   id,
			data: append([]byte(nil), block[:n]...),
		})
	}
	return shards
}

func decodeUntilReadyBasic(t *testing.T, dec *Decoder, shards []struct {
	id   uint32
	data []byte
}) {
	t.Helper()

	for _, shard := range shards {
		state, err := dec.Decode(shard.id, shard.data)
		if err != nil {
			t.Fatalf("decode shard %d: %v", shard.id, err)
		}
		if state == StateReady {
			return
		}
	}

	t.Fatal("decoder never became ready")
}

func assertRecoveredMessage(t *testing.T, dec *Decoder, want []byte) {
	t.Helper()

	got := make([]byte, len(want))
	if err := dec.Recover(got); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("recovered payload mismatch")
	}
}
