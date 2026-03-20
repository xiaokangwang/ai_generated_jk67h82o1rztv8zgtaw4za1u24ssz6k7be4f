//go:build fecbench
// +build fecbench

package fecbench

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/xiaokangwang/fastTransfer/fec/raptorqfec"
	"github.com/xiaokangwang/fastTransfer/fec/wirehairfec"
	"github.com/xiaokangwang/fastTransfer/interfacew"
)

const benchShardSize = 1300

var encoderSink interfacew.FECEngineEncoder
var shardSink []byte
var decodeSink []byte

func BenchmarkEncoderInit(b *testing.B) {
	for _, sourceSymbols := range []int{1024, 2048, 4096} {
		sourceSymbols := sourceSymbols
		payload := bytes.Repeat([]byte{0x5a}, sourceSymbols*benchShardSize)

		b.Run(fmt.Sprintf("wirehair_%d_symbols", sourceSymbols), func(b *testing.B) {
			engine := wirehairfec.NewWirehairFECV2()
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for b.Loop() {
				enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), benchShardSize)
				encoderSink = enc
			}
		})

		b.Run(fmt.Sprintf("raptorq_%d_symbols", sourceSymbols), func(b *testing.B) {
			engine := raptorqfec.NewRaptorQFECV2()
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for b.Loop() {
				enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), benchShardSize)
				encoderSink = enc
			}
		})
	}
}

func BenchmarkShardGeneration(b *testing.B) {
	for _, sourceSymbols := range []int{1024, 2048, 4096} {
		sourceSymbols := sourceSymbols
		payload := bytes.Repeat([]byte{0xa5}, sourceSymbols*benchShardSize)

		b.Run(fmt.Sprintf("wirehair_%d_symbols", sourceSymbols), func(b *testing.B) {
			engine := wirehairfec.NewWirehairFECV2()
			enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), benchShardSize)
			defer enc.Close()
			b.SetBytes(benchShardSize)
			b.ReportAllocs()
			var blockID uint32
			for b.Loop() {
				shardSink = enc.GetShard(blockID)
				blockID++
			}
		})

		b.Run(fmt.Sprintf("raptorq_%d_symbols", sourceSymbols), func(b *testing.B) {
			engine := raptorqfec.NewRaptorQFECV2()
			enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), benchShardSize)
			defer enc.Close()
			b.SetBytes(benchShardSize)
			b.ReportAllocs()
			var blockID uint32
			for b.Loop() {
				shardSink = enc.GetShard(blockID)
				blockID++
			}
		})
	}
}

func BenchmarkDecodeRecover(b *testing.B) {
	for _, sourceSymbols := range []int{1024, 2048, 4096} {
		sourceSymbols := sourceSymbols
		payload := bytes.Repeat([]byte{0x3c}, sourceSymbols*benchShardSize)

		b.Run(fmt.Sprintf("wirehair_%d_symbols", sourceSymbols), func(b *testing.B) {
			engine := wirehairfec.NewWirehairFECV2()
			benchDecodeRecover(b, engine, payload)
		})

		b.Run(fmt.Sprintf("raptorq_%d_symbols", sourceSymbols), func(b *testing.B) {
			engine := raptorqfec.NewRaptorQFECV2()
			benchDecodeRecover(b, engine, payload)
		})
	}
}

func benchDecodeRecover(b *testing.B, engine interfacew.FECEngineV2, payload []byte) {
	enc := engine.GetEncoder3(bytes.NewReader(payload), int64(len(payload)), benchShardSize)
	defer enc.Close()

	sourceSymbols := uint32((len(payload) + benchShardSize - 1) / benchShardSize)
	shardCount := sourceSymbols + sourceSymbols/3 + 1
	shards := make([][]byte, 0, shardCount)
	for id := uint32(0); id < shardCount; id++ {
		shard := append([]byte(nil), enc.GetShard(id)...)
		shards = append(shards, shard)
	}

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		dec := engine.GetDecoder(int64(len(payload)), benchShardSize)
		done := false
		for id, shard := range shards {
			if dec.PutShard(uint32(id), shard) {
				done = true
				break
			}
		}
		if !done {
			b.Fatal("decoder did not complete")
		}
		out := make([]byte, len(payload))
		n, err := bytes.NewBuffer(out[:0]).ReadFrom(dec.GetIn())
		if err != nil {
			b.Fatal(err)
		}
		if int(n) != len(payload) {
			b.Fatalf("unexpected recovered length: got %d want %d", n, len(payload))
		}
		decodeSink = out
		dec.Close()
	}
}
