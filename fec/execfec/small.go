package execfec

import (
	"bytes"
	"io"
)

type smallEncoder struct {
	Buffer []byte
}

func (s smallEncoder) GetShard(blockid uint32) []byte {
	return s.Buffer
}

func (s smallEncoder) Close() {
}

type smallDecoder struct {
	Buffer []byte
}

func (s *smallDecoder) PutShard(blockid uint32, block []byte) bool {
	s.Buffer = block
	return true
}

func (s *smallDecoder) GetIn() io.Reader {
	return bytes.NewReader(s.Buffer)
}

func (s *smallDecoder) Close() {
}
