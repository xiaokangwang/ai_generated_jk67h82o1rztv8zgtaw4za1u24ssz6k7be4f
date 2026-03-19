package raptorqfec

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/xssnick/raptorq"

	"github.com/xiaokangwang/fastTransfer/interfacew"
)

const (
	defaultMaxSourceSymbols  uint32 = 4096
	absoluteMaxSourceSymbols uint32 = 56403
	maxSourceSymbolsEnv             = "RAPTORQ_MAX_SOURCE_SYMBOLS"
)

func NewRaptorQFECV2() interfacew.FECEngineV2 {
	return &engine{maxSourceSymbols: loadMaxSourceSymbolsFromEnv(os.LookupEnv)}
}

type engine struct {
	maxSourceSymbols uint32
}

func (e *engine) MaxInputSize(shardLen int32) uint32 {
	if shardLen <= 0 {
		return 0
	}
	return e.maxSourceSymbols * uint32(shardLen)
}

func (e *engine) GetEncoder(in io.Reader, shardLen int32) interfacew.FECEngineEncoder {
	data, err := io.ReadAll(in)
	if err != nil {
		panic(err)
	}
	return e.GetEncoder3(bytes.NewReader(data), int64(len(data)), shardLen)
}

func (e *engine) GetEncoder3(in io.Reader, inLen int64, shardLen int32) interfacew.FECEngineEncoder {
	validateArgs(inLen, shardLen)

	data, err := io.ReadAll(in)
	if err != nil {
		panic(err)
	}
	if int64(len(data)) != inLen {
		panic(fmt.Sprintf("incorrect input stream length: got %d want %d", len(data), inLen))
	}
	if inLen <= int64(shardLen) {
		return &smallEncoder{buffer: data}
	}

	enc, err := raptorq.NewRaptorQ(uint32(shardLen)).CreateEncoder(data)
	if err != nil {
		panic(err)
	}

	return &encoder{enc: enc}
}

func (e *engine) GetDecoder(inLen int64, shardLen int32) interfacew.FECEngineDecoder {
	validateArgs(inLen, shardLen)

	if inLen <= int64(shardLen) {
		return &smallDecoder{}
	}

	dec, err := raptorq.NewRaptorQ(uint32(shardLen)).CreateDecoder(uint32(inLen))
	if err != nil {
		panic(err)
	}

	return &decoder{dec: dec}
}

type encoder struct {
	enc *raptorq.Encoder
}

func (e *encoder) GetShard(blockid uint32) []byte {
	return e.enc.GenSymbol(blockid)
}

func (e *encoder) Close() {
	e.enc = nil
}

type decoder struct {
	dec       *raptorq.Decoder
	recovered []byte
	done      bool
}

func (d *decoder) PutShard(blockid uint32, block []byte) bool {
	if d.done {
		return true
	}

	canTryDecode, err := d.dec.AddSymbol(blockid, block)
	if err != nil || !canTryDecode {
		return false
	}

	ok, data, err := d.dec.Decode()
	if err != nil || !ok {
		return false
	}

	d.recovered = data
	d.dec = nil
	d.done = true
	return true
}

func (d *decoder) GetIn() io.Reader {
	return bytes.NewReader(d.recovered)
}

func (d *decoder) Close() {
	d.dec = nil
	d.recovered = nil
}

type smallEncoder struct {
	buffer []byte
}

func (s *smallEncoder) GetShard(blockid uint32) []byte {
	return s.buffer
}

func (s *smallEncoder) Close() {
	s.buffer = nil
}

type smallDecoder struct {
	buffer []byte
}

func (s *smallDecoder) PutShard(blockid uint32, block []byte) bool {
	s.buffer = append(s.buffer[:0], block...)
	return true
}

func (s *smallDecoder) GetIn() io.Reader {
	return bytes.NewReader(s.buffer)
}

func (s *smallDecoder) Close() {
	s.buffer = nil
}

func validateArgs(inLen int64, shardLen int32) {
	switch {
	case inLen < 0:
		panic("invalid negative input length")
	case inLen > math.MaxUint32:
		panic(fmt.Sprintf("input length %d exceeds raptorq limit", inLen))
	case shardLen <= 0:
		panic(fmt.Sprintf("invalid shard length %d", shardLen))
	}
}

func loadMaxSourceSymbolsFromEnv(lookupEnv func(string) (string, bool)) uint32 {
	value, ok := lookupEnv(maxSourceSymbolsEnv)
	if !ok {
		return defaultMaxSourceSymbols
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return defaultMaxSourceSymbols
	}

	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		panic(fmt.Sprintf("invalid %s value %q: %v", maxSourceSymbolsEnv, value, err))
	}

	if parsed == 0 {
		panic(fmt.Sprintf("%s must be greater than zero", maxSourceSymbolsEnv))
	}
	if parsed > uint64(absoluteMaxSourceSymbols) {
		panic(fmt.Sprintf("%s must be <= %d", maxSourceSymbolsEnv, absoluteMaxSourceSymbols))
	}

	return uint32(parsed)
}
