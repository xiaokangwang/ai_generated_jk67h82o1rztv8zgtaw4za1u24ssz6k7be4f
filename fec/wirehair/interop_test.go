//go:build wirehairnativeoracle
// +build wirehairnativeoracle

package wirehair

import (
	"bytes"
	"testing"

	"codextest2/internal/nativeoracle"
)

func TestEncodeMatchesNative(t *testing.T) {
	cases := []struct {
		messageBytes int
		blockBytes   uint32
	}{
		{128, 16},
		{511, 50},
		{4096, 256},
		{12345, 140},
	}

	for _, tc := range cases {
		message := testMessage(tc.messageBytes)
		enc, err := NewEncoder(message, tc.blockBytes)
		if err != nil {
			t.Fatalf("new encoder %+v: %v", tc, err)
		}
		n := uint32((tc.messageBytes + int(tc.blockBytes) - 1) / int(tc.blockBytes))
		for blockID := uint32(0); blockID < n+12; blockID++ {
			got := make([]byte, tc.blockBytes)
			gotN, err := enc.Encode(blockID, got)
			if err != nil {
				t.Fatalf("go encode %+v block %d: %v", tc, blockID, err)
			}

			code, want, err := nativeoracle.NativeEncode(message, tc.blockBytes, blockID)
			if err != nil {
				t.Fatalf("native encode %+v block %d: %v", tc, blockID, err)
			}
			if ResultCode(code) != ResultSuccess {
				t.Fatalf("native encode %+v block %d returned %v", tc, blockID, code)
			}
			if !bytes.Equal(got[:gotN], want) {
				t.Fatalf("encode mismatch %+v block %d", tc, blockID)
			}
		}
	}
}

func TestDecodeRecoverMatchesNative(t *testing.T) {
	cases := []struct {
		messageBytes int
		blockBytes   uint32
	}{
		{128, 16},
		{511, 50},
		{4096, 256},
		{7777, 111},
	}

	for _, tc := range cases {
		message := testMessage(tc.messageBytes)
		enc, err := NewEncoder(message, tc.blockBytes)
		if err != nil {
			t.Fatalf("new encoder %+v: %v", tc, err)
		}

		n := uint32((tc.messageBytes + int(tc.blockBytes) - 1) / int(tc.blockBytes))
		shards := encodeShardsForIDs(t, enc, selectShardIDs(n), tc.blockBytes)

		dec, err := NewDecoder(uint64(len(message)), tc.blockBytes)
		if err != nil {
			t.Fatalf("new decoder %+v: %v", tc, err)
		}
		used := decodeUntilReady(t, dec, shards)

		recovered := make([]byte, len(message))
		if err := dec.Recover(recovered); err != nil {
			t.Fatalf("go recover %+v: %v", tc, err)
		}
		if !bytes.Equal(recovered, message) {
			t.Fatalf("go recover mismatch %+v", tc)
		}

		code, nativeRecovered, err := nativeoracle.NativeRecover(uint64(len(message)), tc.blockBytes, used)
		if err != nil {
			t.Fatalf("native recover %+v: %v", tc, err)
		}
		if ResultCode(code) != ResultSuccess {
			t.Fatalf("native recover %+v returned %v", tc, code)
		}
		if !bytes.Equal(nativeRecovered, message) {
			t.Fatalf("native recover mismatch %+v", tc)
		}

		blockIDs := []uint32{0, n / 2, n - 1}
		for _, blockID := range blockIDs {
			block := make([]byte, tc.blockBytes)
			gotN, err := dec.RecoverBlock(blockID, block)
			if err != nil {
				t.Fatalf("go recover block %+v block %d: %v", tc, blockID, err)
			}

			code, want, err := nativeoracle.NativeRecoverBlock(uint64(len(message)), tc.blockBytes, blockID, used)
			if err != nil {
				t.Fatalf("native recover block %+v block %d: %v", tc, blockID, err)
			}
			if ResultCode(code) != ResultSuccess {
				t.Fatalf("native recover block %+v block %d returned %v", tc, blockID, code)
			}
			if !bytes.Equal(block[:gotN], want) {
				t.Fatalf("recover block mismatch %+v block %d", tc, blockID)
			}
		}

		goEncoder, err := dec.BecomeEncoder()
		if err != nil {
			t.Fatalf("go become encoder %+v: %v", tc, err)
		}
		reencoded := make([]byte, tc.blockBytes)
		blockID := n + 7
		gotN, err := goEncoder.Encode(blockID, reencoded)
		if err != nil {
			t.Fatalf("go encode after become %+v: %v", tc, err)
		}

		code, want, err := nativeoracle.NativeBecomeEncode(uint64(len(message)), tc.blockBytes, blockID, used)
		if err != nil {
			t.Fatalf("native become encode %+v: %v", tc, err)
		}
		if ResultCode(code) != ResultSuccess {
			t.Fatalf("native become encode %+v returned %v", tc, code)
		}
		if !bytes.Equal(reencoded[:gotN], want) {
			t.Fatalf("become-encoder output mismatch %+v", tc)
		}
	}
}

func TestInvalidInputsAndCompatAPI(t *testing.T) {
	if _, err := NewEncoder([]byte{1}, 4); err == nil {
		t.Fatalf("expected small-N encoder error")
	}
	if _, err := NewDecoder(1, 1); err == nil {
		t.Fatalf("expected small-N decoder error")
	}

	decoder, err := NewDecoder(505, 50)
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}
	if _, err := decoder.Decode(0, make([]byte, 49)); err == nil {
		t.Fatalf("expected invalid input for short non-final block")
	}
	if _, err := decoder.Decode(10, make([]byte, 4)); err == nil {
		t.Fatalf("expected invalid input for short final block")
	}
	if _, err := decoder.RecoverBlock(9999, make([]byte, 50)); err == nil {
		t.Fatalf("expected invalid input for out-of-range recover block")
	}

	message := testMessage(300)
	enc, err := NewEncoder(message, 50)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	compatEnc, err := NewWirehairEncoder(message, uint64(len(message)), 50)
	if err != nil {
		t.Fatalf("new compat encoder: %v", err)
	}
	compatDec, err := NewWirehairDecoder(uint64(len(message)), 50)
	if err != nil {
		t.Fatalf("new compat decoder: %v", err)
	}

	for _, id := range []uint32{0, 1, 1, 2, 3, 4} {
		block := make([]byte, 50)
		n, err := enc.Encode(id, block)
		if err != nil {
			t.Fatalf("encode %d: %v", id, err)
		}
		result, err := compatDec.Decode(uint64(id), block[:n], uint32(n))
		if id == 4 {
			if err == nil {
				t.Fatalf("expected duplicate-original decode error")
			}
		} else if err != nil {
			t.Fatalf("compat decode %d: %v", id, err)
		} else if result != WirehairResultNeedMore {
			t.Fatalf("unexpected compat decode result %v", result)
		}
	}

	block := make([]byte, 50)
	var outBytes uint32
	if result, err := compatEnc.Encode(7, block, 50, &outBytes); err != nil || result != WirehairResultSuccess || outBytes == 0 {
		t.Fatalf("compat encode result=%v err=%v out=%d", result, err, outBytes)
	}
}
