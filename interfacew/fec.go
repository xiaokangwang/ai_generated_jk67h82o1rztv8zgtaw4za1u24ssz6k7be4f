package interfacew

import "io"

type FECEngine interface {
	GetEncoder(in io.Reader, shardLen int32) FECEngineEncoder
	GetDecoder(inLen int64, shardLen int32) FECEngineDecoder
}

type FECEngineV2 interface {
	FECEngine
	GetEncoder3(in io.Reader, inLen int64, shardLen int32) FECEngineEncoder
}

type FECEngineInputSizer interface {
	MaxInputSize(shardLen int32) uint32
}

type FECEngineEncoder interface {
	GetShard(blockid uint32) []byte
	Close()
}

type FECEngineDecoder interface {
	PutShard(blockid uint32, block []byte) bool
	GetIn() io.Reader
	Close()
}
