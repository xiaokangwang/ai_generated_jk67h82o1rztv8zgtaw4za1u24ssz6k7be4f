//go:build wirehairnativeoracle
// +build wirehairnativeoracle

package wirehair

import (
	"bytes"
	"errors"
	"testing"

	"codextest2/internal/nativeoracle"
)

func normalizeFuzzInput(data []byte, blockHint uint8) ([]byte, uint32, bool) {
	if len(data) < 2 {
		return nil, 0, false
	}
	if len(data) > 4096 {
		data = data[:4096]
	}
	blockBytes := int(blockHint)%64 + 2
	if blockBytes >= len(data) {
		blockBytes = len(data) - 1
	}
	if blockBytes < 1 {
		return nil, 0, false
	}
	if (len(data)+blockBytes-1)/blockBytes < 2 {
		return nil, 0, false
	}
	return append([]byte(nil), data...), uint32(blockBytes), true
}

func fuzzShardIDs(n uint32, pattern uint8) []uint32 {
	target := int(n) - 1 + int(pattern&7)
	if target < 1 {
		target = 1
	}
	limit := n + 8 + uint32(pattern&7)
	ids := make([]uint32, 0, target+2)
	for i := uint32(0); len(ids) < target && i < limit*3; i++ {
		if i < n && ((i+uint32(pattern>>2))%5 == 2) {
			continue
		}
		id := (i*5 + uint32(pattern)*3 + uint32(i>>1)) % limit
		ids = append(ids, id)
		if pattern&0x40 != 0 && len(ids) < target && len(ids)%3 == 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		ids = append(ids, 0)
	}
	for i := range ids {
		j := int((uint32(i)*uint32(pattern) + uint32(pattern>>1) + 1) % uint32(len(ids)))
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids
}

func fuzzShardsForPattern(t *testing.T, enc *Encoder, message []byte, blockBytes uint32, pattern uint8) []nativeoracle.Shard {
	t.Helper()
	n := uint32((len(message) + int(blockBytes) - 1) / int(blockBytes))
	shards := encodeShardsForIDs(t, enc, fuzzShardIDs(n, pattern), blockBytes)
	if len(shards) == 0 {
		return shards
	}
	if pattern&0x80 != 0 {
		i := int(pattern>>1) % len(shards)
		if len(shards[i].Data) > 0 {
			shards[i].Data = append([]byte(nil), shards[i].Data[:len(shards[i].Data)-1]...)
		}
	}
	return shards
}

func fuzzResultCode(err error) (ResultCode, bool) {
	if err == nil {
		return ResultSuccess, true
	}
	var wireErr *Error
	if errors.As(err, &wireErr) {
		return wireErr.Code, true
	}
	return ResultError, false
}

func runGoDecodeOracle(message []byte, blockBytes uint32, shards []nativeoracle.Shard) (ResultCode, []byte, *Decoder, bool, error) {
	dec, err := NewDecoder(uint64(len(message)), blockBytes)
	if err != nil {
		code, ok := fuzzResultCode(err)
		if ok {
			return code, nil, nil, false, nil
		}
		return ResultError, nil, nil, false, err
	}

	for _, shard := range shards {
		state, err := dec.Decode(shard.ID, shard.Data)
		if err != nil {
			code, ok := fuzzResultCode(err)
			if ok {
				return code, nil, nil, false, nil
			}
			return ResultError, nil, nil, false, err
		}
		if state != StateReady {
			continue
		}

		recovered := make([]byte, len(message))
		if err := dec.Recover(recovered); err != nil {
			code, ok := fuzzResultCode(err)
			if ok {
				return code, nil, nil, false, nil
			}
			return ResultError, nil, nil, false, err
		}
		return ResultSuccess, recovered, dec, true, nil
	}

	recovered := make([]byte, len(message))
	if err := dec.Recover(recovered); err != nil {
		code, ok := fuzzResultCode(err)
		if ok {
			return code, nil, dec, false, nil
		}
		return ResultError, nil, nil, false, err
	}
	return ResultSuccess, recovered, dec, false, nil
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("wirehair-go-port"), uint8(7), uint8(3))
	f.Add(testMessage(257), uint8(31), uint8(11))
	f.Add(testMessage(1025), uint8(17), uint8(29))

	f.Fuzz(func(t *testing.T, data []byte, blockHint uint8, pattern uint8) {
		message, blockBytes, ok := normalizeFuzzInput(data, blockHint)
		if !ok {
			t.Skip()
		}

		enc, err := NewEncoder(message, blockBytes)
		if err != nil {
			t.Skip()
		}

		n := uint32((len(message) + int(blockBytes) - 1) / int(blockBytes))
		var ids []uint32
		for i := uint32(0); i < n+8; i++ {
			if i < n && ((i+uint32(pattern))%3 == 1) {
				continue
			}
			ids = append(ids, i)
			if len(ids) >= int(n)+2 {
				break
			}
		}
		if len(ids) < int(n) {
			t.Skip()
		}
		for i := range ids {
			j := int((uint32(i)*uint32(pattern) + uint32(pattern>>1)) % uint32(len(ids)))
			ids[i], ids[j] = ids[j], ids[i]
		}

		shards := encodeShardsForIDs(t, enc, ids, blockBytes)
		dec, err := NewDecoder(uint64(len(message)), blockBytes)
		if err != nil {
			t.Fatalf("new decoder: %v", err)
		}
		decodeUntilReady(t, dec, shards)

		recovered := make([]byte, len(message))
		if err := dec.Recover(recovered); err != nil {
			t.Fatalf("recover: %v", err)
		}
		if !bytes.Equal(recovered, message) {
			t.Fatalf("round-trip mismatch")
		}
	})
}

func FuzzNativeEncodeCompatibility(f *testing.F) {
	f.Add([]byte("wirehair-diff"), uint8(9))
	f.Add(testMessage(333), uint8(23))
	f.Add(testMessage(777), uint8(42))

	f.Fuzz(func(t *testing.T, data []byte, blockHint uint8) {
		message, blockBytes, ok := normalizeFuzzInput(data, blockHint)
		if !ok {
			t.Skip()
		}

		enc, err := NewEncoder(message, blockBytes)
		if err != nil {
			t.Skip()
		}

		n := uint32((len(message) + int(blockBytes) - 1) / int(blockBytes))
		blockIDs := []uint32{0, n / 2, n + 3}
		for _, blockID := range blockIDs {
			got := make([]byte, blockBytes)
			gotN, err := enc.Encode(blockID, got)
			if err != nil {
				t.Fatalf("go encode block %d: %v", blockID, err)
			}

			code, want, err := nativeoracle.NativeEncode(message, blockBytes, blockID)
			if err != nil {
				t.Fatalf("native encode block %d: %v", blockID, err)
			}
			if ResultCode(code) != ResultSuccess {
				t.Fatalf("native encode returned %v", code)
			}
			if !bytes.Equal(got[:gotN], want) {
				t.Fatalf("encode mismatch for block %d", blockID)
			}
		}
	})
}

func FuzzNativeDecodeCompatibility(f *testing.F) {
	f.Add([]byte("wirehair-decode-diff"), uint8(13), uint8(5))
	f.Add(testMessage(513), uint8(21), uint8(47))
	f.Add(testMessage(1501), uint8(37), uint8(129))

	f.Fuzz(func(t *testing.T, data []byte, blockHint uint8, pattern uint8) {
		message, blockBytes, ok := normalizeFuzzInput(data, blockHint)
		if !ok {
			t.Skip()
		}

		enc, err := NewEncoder(message, blockBytes)
		if err != nil {
			t.Skip()
		}

		n := uint32((len(message) + int(blockBytes) - 1) / int(blockBytes))
		shards := fuzzShardsForPattern(t, enc, message, blockBytes, pattern)

		goCode, goRecovered, dec, decodeReady, err := runGoDecodeOracle(message, blockBytes, shards)
		if err != nil {
			t.Fatalf("go decode oracle: %v", err)
		}

		nativeCodeRaw, nativeRecovered, err := nativeoracle.NativeRecover(uint64(len(message)), blockBytes, shards)
		if err != nil {
			t.Fatalf("native recover: %v", err)
		}
		nativeCode := ResultCode(nativeCodeRaw)
		if goCode != nativeCode {
			t.Fatalf("decode result mismatch: go=%v native=%v", goCode, nativeCode)
		}
		if goCode != ResultSuccess {
			return
		}
		if !bytes.Equal(goRecovered, nativeRecovered) {
			t.Fatalf("recovered message mismatch")
		}

		blockID := uint32(pattern) % n
		goBlock := make([]byte, blockBytes)
		goBlockN, err := dec.RecoverBlock(blockID, goBlock)
		if err != nil {
			t.Fatalf("go recover block %d: %v", blockID, err)
		}
		nativeBlockCodeRaw, nativeBlock, err := nativeoracle.NativeRecoverBlock(uint64(len(message)), blockBytes, blockID, shards)
		if err != nil {
			t.Fatalf("native recover block %d: %v", blockID, err)
		}
		if ResultCode(nativeBlockCodeRaw) != ResultSuccess {
			t.Fatalf("native recover block %d returned %v", blockID, nativeBlockCodeRaw)
		}
		if !bytes.Equal(goBlock[:goBlockN], nativeBlock) {
			t.Fatalf("recovered block mismatch for block %d", blockID)
		}

		if decodeReady {
			goEncoder, err := dec.BecomeEncoder()
			if err != nil {
				t.Fatalf("go become encoder: %v", err)
			}
			parityID := n + uint32(pattern&7) + 1
			goParity := make([]byte, blockBytes)
			goParityN, err := goEncoder.Encode(parityID, goParity)
			if err != nil {
				t.Fatalf("go encode parity %d: %v", parityID, err)
			}
			nativeParityCodeRaw, nativeParity, err := nativeoracle.NativeBecomeEncode(uint64(len(message)), blockBytes, parityID, shards)
			if err != nil {
				t.Fatalf("native become encode %d: %v", parityID, err)
			}
			if ResultCode(nativeParityCodeRaw) != ResultSuccess {
				t.Fatalf("native become encode %d returned %v", parityID, nativeParityCodeRaw)
			}
			if !bytes.Equal(goParity[:goParityN], nativeParity) {
				t.Fatalf("parity block mismatch for block %d", parityID)
			}
		}
	})
}
