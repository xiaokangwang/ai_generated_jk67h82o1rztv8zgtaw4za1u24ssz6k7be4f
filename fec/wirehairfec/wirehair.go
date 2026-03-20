package wirehairfec

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/xiaokangwang/fastTransfer/fec/wirehair"
	"github.com/xiaokangwang/fastTransfer/interfacew"
)

const (
	defaultMaxSourceBlocks  uint32 = 64000
	absoluteMaxSourceBlocks uint32 = 64000
	maxSourceBlocksEnv             = "WIREHAIR_MAX_SOURCE_BLOCKS"
)

func NewWirehairFECV2() interfacew.FECEngineV2 {
	return &engine{maxSourceBlocks: loadMaxSourceBlocksFromEnv(os.LookupEnv)}
}

type engine struct {
	maxSourceBlocks uint32
}

func (e *engine) MaxInputSize(shardLen int32) uint32 {
	if shardLen <= 0 {
		return 0
	}
	maxInputSize := uint64(e.maxSourceBlocks) * uint64(shardLen)
	if maxInputSize > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(maxInputSize)
}

func (e *engine) GetEncoder(in io.Reader, shardLen int32) interfacew.FECEngineEncoder {
	data, err := io.ReadAll(in)
	if err != nil {
		panic(err)
	}
	return e.GetEncoder3(bytes.NewReader(data), int64(len(data)), shardLen)
}

func (e *engine) GetEncoder3(in io.Reader, inLen int64, shardLen int32) interfacew.FECEngineEncoder {
	e.validateArgs(inLen, shardLen)

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

	enc, err := wirehair.NewEncoder(data, uint32(shardLen))
	if err != nil {
		panic(err)
	}

	return &encoder{enc: enc, shardLen: int(shardLen)}
}

func (e *engine) GetDecoder(inLen int64, shardLen int32) interfacew.FECEngineDecoder {
	e.validateArgs(inLen, shardLen)

	if inLen <= int64(shardLen) {
		return &smallDecoder{}
	}

	dec, err := wirehair.NewDecoder(uint64(inLen), uint32(shardLen))
	if err != nil {
		panic(err)
	}

	return &decoder{
		dec:         dec,
		messageSize: int(inLen),
		seen:        make(map[uint32]struct{}),
	}
}

type encoder struct {
	enc      *wirehair.Encoder
	shardLen int
}

func (e *encoder) GetShard(blockid uint32) []byte {
	buffer := make([]byte, e.shardLen)
	n, err := e.enc.Encode(blockid, buffer)
	if err != nil {
		panic(err)
	}
	return buffer[:n]
}

func (e *encoder) Close() {
	e.enc = nil
}

type decoder struct {
	dec         *wirehair.Decoder
	messageSize int
	recovered   []byte
	done        bool
	seen        map[uint32]struct{}
}

func (d *decoder) PutShard(blockid uint32, block []byte) bool {
	if d.done {
		return true
	}
	if _, ok := d.seen[blockid]; ok {
		return false
	}
	d.seen[blockid] = struct{}{}

	state, err := d.decodeSafe(blockid, block)
	if err != nil || state != wirehair.StateReady {
		return false
	}

	recovered := make([]byte, d.messageSize)
	if err := d.recoverSafe(recovered); err != nil {
		return false
	}

	d.recovered = recovered
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
	d.seen = nil
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

func (e *engine) validateArgs(inLen int64, shardLen int32) {
	switch {
	case inLen < 0:
		panic("invalid negative input length")
	case shardLen <= 0:
		panic(fmt.Sprintf("invalid shard length %d", shardLen))
	}

	maxInputSize := int64(e.maxSourceBlocks) * int64(shardLen)
	if inLen > maxInputSize {
		panic(fmt.Sprintf("input length %d exceeds wirehair limit %d", inLen, maxInputSize))
	}
}

func loadMaxSourceBlocksFromEnv(lookupEnv func(string) (string, bool)) uint32 {
	value, ok := lookupEnv(maxSourceBlocksEnv)
	if !ok {
		return defaultMaxSourceBlocks
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return defaultMaxSourceBlocks
	}

	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		panic(fmt.Sprintf("invalid %s value %q: %v", maxSourceBlocksEnv, value, err))
	}

	if parsed == 0 {
		panic(fmt.Sprintf("%s must be greater than zero", maxSourceBlocksEnv))
	}
	if parsed > uint64(absoluteMaxSourceBlocks) {
		panic(fmt.Sprintf("%s must be <= %d", maxSourceBlocksEnv, absoluteMaxSourceBlocks))
	}

	return uint32(parsed)
}

func (d *decoder) decodeSafe(blockid uint32, block []byte) (state wirehair.State, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("wirehair decode panic: %v", r)
		}
	}()
	return d.dec.Decode(blockid, block)
}

func (d *decoder) recoverSafe(out []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("wirehair recover panic: %v", r)
		}
	}()
	return d.dec.Recover(out)
}
