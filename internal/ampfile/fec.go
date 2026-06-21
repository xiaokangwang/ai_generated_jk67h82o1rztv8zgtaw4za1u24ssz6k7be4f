package ampfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gomodstore/internal/wirehair"
)

func sourceSymbolCount(length, symbolSize uint64) uint64 {
	if length == 0 {
		return 0
	}
	return (length + symbolSize - 1) / symbolSize
}

func totalSymbolCount(source, minRecovery, totalMillis uint64) uint64 {
	if source == 0 {
		return 0
	}
	byMin := source + minRecovery
	byRatio := (source*totalMillis + 999) / 1000
	if byRatio < source {
		byRatio = source
	}
	if byMin > byRatio {
		return byMin
	}
	return byRatio
}

func encodeSymbol(chunk []byte, symbolSize int, symbolID uint64) ([]byte, error) {
	if symbolSize <= 0 {
		return nil, errors.New("symbol size must be greater than zero")
	}
	if len(chunk) <= symbolSize {
		return append([]byte(nil), chunk...), nil
	}
	if symbolID > uint64(^uint32(0)) {
		return nil, fmt.Errorf("symbol id %d exceeds wirehair limit", symbolID)
	}
	enc, err := wirehair.NewEncoder(chunk, uint32(symbolSize))
	if err != nil {
		return nil, err
	}
	buf := make([]byte, symbolSize)
	n, err := enc.Encode(uint32(symbolID), buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

type chunkDecoder struct {
	length     uint64
	symbolSize int
	decoder    *wirehair.Decoder
	seen       map[uint64]struct{}
	recovered  []byte
	done       bool
}

func newChunkDecoder(length uint64, symbolSize int) (*chunkDecoder, error) {
	if symbolSize <= 0 {
		return nil, errors.New("symbol size must be greater than zero")
	}
	if length <= uint64(symbolSize) {
		return &chunkDecoder{length: length, symbolSize: symbolSize, seen: make(map[uint64]struct{})}, nil
	}
	if length > uint64(^uint(0)) {
		return nil, errors.New("chunk too large for this platform")
	}
	dec, err := wirehair.NewDecoder(length, uint32(symbolSize))
	if err != nil {
		return nil, err
	}
	return &chunkDecoder{length: length, symbolSize: symbolSize, decoder: dec, seen: make(map[uint64]struct{})}, nil
}

func (d *chunkDecoder) put(symbolID uint64, data []byte) (bool, error) {
	if d.done {
		return true, nil
	}
	if _, ok := d.seen[symbolID]; ok {
		return false, nil
	}
	d.seen[symbolID] = struct{}{}
	if d.length <= uint64(d.symbolSize) {
		if uint64(len(data)) != d.length {
			return false, fmt.Errorf("small chunk symbol length = %d, want %d", len(data), d.length)
		}
		d.recovered = append([]byte(nil), data...)
		d.done = true
		return true, nil
	}
	if symbolID > uint64(^uint32(0)) {
		return false, fmt.Errorf("symbol id %d exceeds wirehair limit", symbolID)
	}
	state, err := d.decoder.Decode(uint32(symbolID), data)
	if err != nil {
		return false, err
	}
	if state != wirehair.StateReady {
		return false, nil
	}
	out := make([]byte, int(d.length))
	if err := d.decoder.Recover(out); err != nil {
		return false, err
	}
	d.recovered = out
	d.done = true
	return true, nil
}

func (d *chunkDecoder) reader() io.Reader {
	return bytes.NewReader(d.recovered)
}
