package transfer

import "github.com/xiaokangwang/fastTransfer/interfacew"

const (
	DefaultShardSize  uint32 = 1300
	LegacyMaxPartSize uint32 = 1024 * 1024 * 78
)

func MaxPartSizeForEngine(engine interfacew.FECEngineV2, shardSize uint32) uint32 {
	if shardSize == 0 {
		return 0
	}
	if sizer, ok := engine.(interfacew.FECEngineInputSizer); ok {
		if maxInputSize := sizer.MaxInputSize(int32(shardSize)); maxInputSize > 0 {
			return maxInputSize
		}
	}
	return LegacyMaxPartSize
}
