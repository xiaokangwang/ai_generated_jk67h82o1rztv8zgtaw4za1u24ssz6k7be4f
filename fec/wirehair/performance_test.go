//go:build wirehairnativeoracle
// +build wirehairnativeoracle

package wirehair

import (
	"runtime"
	"testing"
	"time"

	"codextest2/internal/nativeoracle"
)

type goBenchResult struct {
	NS         uint64
	TotalAlloc uint64
	HeapInuse  uint64
}

func benchmarkGoEncode(message []byte, blockBytes uint32, iterations uint32, maxBlockID uint32) (goBenchResult, error) {
	enc, err := NewEncoder(message, blockBytes)
	if err != nil {
		return goBenchResult{}, err
	}

	block := make([]byte, blockBytes)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for i := uint32(0); i < iterations; i++ {
		if _, err := enc.Encode(i%maxBlockID, block); err != nil {
			return goBenchResult{}, err
		}
	}
	elapsed := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	return goBenchResult{
		NS:         uint64(elapsed.Nanoseconds()),
		TotalAlloc: after.TotalAlloc - before.TotalAlloc,
		HeapInuse:  after.HeapInuse,
	}, nil
}

func benchmarkGoRecover(message []byte, blockBytes uint32, iterations uint32, shards []nativeoracle.Shard) (goBenchResult, error) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for i := uint32(0); i < iterations; i++ {
		dec, err := NewDecoder(uint64(len(message)), blockBytes)
		if err != nil {
			return goBenchResult{}, err
		}
		for _, shard := range shards {
			state, err := dec.Decode(shard.ID, shard.Data)
			if err != nil {
				return goBenchResult{}, err
			}
			if state == StateReady {
				break
			}
		}
		recovered := make([]byte, len(message))
		if err := dec.Recover(recovered); err != nil {
			return goBenchResult{}, err
		}
	}
	elapsed := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	return goBenchResult{
		NS:         uint64(elapsed.Nanoseconds()),
		TotalAlloc: after.TotalAlloc - before.TotalAlloc,
		HeapInuse:  after.HeapInuse,
	}, nil
}

func TestPerformanceEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("skip performance checks in short mode")
	}

	message := testMessage(64 * 1024)
	const blockBytes = uint32(1024)
	n := uint32((len(message) + int(blockBytes) - 1) / int(blockBytes))
	enc, err := NewEncoder(message, blockBytes)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	shards := encodeShardsForIDs(t, enc, selectShardIDs(n), blockBytes)

	goEncode, err := benchmarkGoEncode(message, blockBytes, 400, n+32)
	if err != nil {
		t.Fatalf("benchmark go encode: %v", err)
	}
	nativeEncode, err := nativeoracle.NativeBenchEncode(message, blockBytes, 400, n+32)
	if err != nil {
		t.Fatalf("benchmark native encode: %v", err)
	}
	if ResultCode(nativeEncode.Code) != ResultSuccess {
		t.Fatalf("native encode bench returned %v", nativeEncode.Code)
	}
	if goEncode.NS > nativeEncode.NS*80+10_000_000 {
		t.Fatalf("encode slowdown too high: go=%dns native=%dns", goEncode.NS, nativeEncode.NS)
	}
	if nativeEncode.PeakKiB > 0 && goEncode.HeapInuse/1024 > nativeEncode.PeakKiB*128 {
		t.Fatalf("encode heap growth too high: go=%dKiB native=%dKiB", goEncode.HeapInuse/1024, nativeEncode.PeakKiB)
	}

	goRecover, err := benchmarkGoRecover(message, blockBytes, 60, shards)
	if err != nil {
		t.Fatalf("benchmark go recover: %v", err)
	}
	nativeRecover, err := nativeoracle.NativeBenchRecover(uint64(len(message)), blockBytes, 60, shards)
	if err != nil {
		t.Fatalf("benchmark native recover: %v", err)
	}
	if ResultCode(nativeRecover.Code) != ResultSuccess {
		t.Fatalf("native recover bench returned %v", nativeRecover.Code)
	}
	if goRecover.NS > nativeRecover.NS*120+20_000_000 {
		t.Fatalf("recover slowdown too high: go=%dns native=%dns", goRecover.NS, nativeRecover.NS)
	}
	if nativeRecover.PeakKiB > 0 && goRecover.HeapInuse/1024 > nativeRecover.PeakKiB*192 {
		t.Fatalf("recover heap growth too high: go=%dKiB native=%dKiB", goRecover.HeapInuse/1024, nativeRecover.PeakKiB)
	}

	t.Logf("encode: go=%dns alloc=%dB heap=%dKiB native=%dns peak=%dKiB", goEncode.NS, goEncode.TotalAlloc, goEncode.HeapInuse/1024, nativeEncode.NS, nativeEncode.PeakKiB)
	t.Logf("recover: go=%dns alloc=%dB heap=%dKiB native=%dns peak=%dKiB", goRecover.NS, goRecover.TotalAlloc, goRecover.HeapInuse/1024, nativeRecover.NS, nativeRecover.PeakKiB)
}
