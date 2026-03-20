package wirehair

import "testing"

func BenchmarkEncode(b *testing.B) {
	message := testMessage(256 * 1024)
	const blockBytes = 1024

	enc, err := NewEncoder(message, blockBytes)
	if err != nil {
		b.Fatalf("new encoder: %v", err)
	}
	n := uint32((len(message) + blockBytes - 1) / blockBytes)
	block := make([]byte, blockBytes)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode(uint32(i)%(n+32), block); err != nil {
			b.Fatalf("encode: %v", err)
		}
	}
}

func BenchmarkDecodeRecover(b *testing.B) {
	message := testMessage(64 * 1024)
	const blockBytes = 1024

	enc, err := NewEncoder(message, blockBytes)
	if err != nil {
		b.Fatalf("new encoder: %v", err)
	}
	n := uint32((len(message) + blockBytes - 1) / blockBytes)
	ids := selectShardIDs(n)
	shards := make([]struct {
		id   uint32
		data []byte
	}, 0, len(ids))
	for _, id := range ids {
		block := make([]byte, blockBytes)
		size, err := enc.Encode(id, block)
		if err != nil {
			b.Fatalf("encode %d: %v", id, err)
		}
		shards = append(shards, struct {
			id   uint32
			data []byte
		}{id: id, data: append([]byte(nil), block[:size]...)})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec, err := NewDecoder(uint64(len(message)), blockBytes)
		if err != nil {
			b.Fatalf("new decoder: %v", err)
		}
		ready := false
		for _, shard := range shards {
			state, err := dec.Decode(shard.id, shard.data)
			if err != nil {
				b.Fatalf("decode: %v", err)
			}
			if state == StateReady {
				ready = true
				break
			}
		}
		if !ready {
			b.Fatalf("decoder never became ready")
		}
		recovered := make([]byte, len(message))
		if err := dec.Recover(recovered); err != nil {
			b.Fatalf("recover: %v", err)
		}
	}
}
