package wirehair

import "math/bits"

const listTerm = 0xffff

var heavyMatrix = [heavyRows][heavyCols]uint8{
	{0x85, 0xd3, 0x66, 0xf3, 0x38, 0x95, 0x56, 0xad, 0x57, 0xaf, 0x58, 0x48, 0xbc, 0xfa, 0x02, 0xc5, 0x43, 0xe8},
	{0xd3, 0x85, 0xf3, 0x66, 0x95, 0x38, 0xad, 0x56, 0xaf, 0x57, 0x48, 0x58, 0xfa, 0xbc, 0xc5, 0x02, 0xe8, 0x43},
	{0x82, 0x22, 0x57, 0xaf, 0x56, 0xad, 0x38, 0x95, 0x66, 0xf3, 0x43, 0xe8, 0x02, 0xc5, 0xbc, 0xfa, 0x58, 0x48},
	{0x22, 0x82, 0xaf, 0x57, 0xad, 0x56, 0x95, 0x38, 0xf3, 0x66, 0xe8, 0x43, 0xc5, 0x02, 0xfa, 0xbc, 0x48, 0x58},
	{0x51, 0x34, 0x56, 0xad, 0x57, 0xaf, 0x66, 0xf3, 0x38, 0x95, 0x02, 0xc5, 0x43, 0xe8, 0x58, 0x48, 0xbc, 0xfa},
	{0x34, 0x51, 0xad, 0x56, 0xaf, 0x57, 0xf3, 0x66, 0x95, 0x38, 0xc5, 0x02, 0xe8, 0x43, 0x48, 0x58, 0xfa, 0xbc},
}

type Codec struct {
	blockBytes         int
	blockCount         uint16
	blockNextPrime     uint16
	seedOverride       bool
	denseCount         uint16
	pSeed              uint32
	dSeed              uint32
	extraCount         uint16
	rowCount           uint16
	mixCount           uint16
	mixNextPrime       uint16
	recoveryBlocks     []byte
	recoveryRows       int
	inputBlocks        []byte
	inputFinalBytes    int
	outputFinalBytes   int
	inputAllocated     bool
	allOriginal        bool
	copiedOriginal     []byte
	originalOutOfOrder bool
	decoderFinal       bool
	decoderResult      ResultCode

	peelRows    []peelRow
	peelCols    []peelColumn
	peelColRefs []peelRefs
	peelTailRow *peelRow

	compressMatrix []uint64
	geMatrix       []uint64
	gePitch        int
	geRows         int
	geCols         int
	pivots         []uint16
	pivotCount     int
	geColMap       []uint16
	geRowMap       []uint16
	nextPivot      int

	heavyMatrix      []byte
	heavyPitch       int
	heavyColumns     int
	heavyRows        int
	firstHeavyColumn int
	firstHeavyPivot  int

	peelHeadRows     uint16
	deferHeadColumns uint16
	deferHeadRows    uint16
	deferCount       uint16

	mode mode
}

func (c *Codec) block(blocks []byte, index int) []byte {
	start := index * c.blockBytes
	return blocks[start : start+c.blockBytes]
}

func (c *Codec) blockPartial(blocks []byte, index int, n int) []byte {
	start := index * c.blockBytes
	return blocks[start : start+n]
}

func (c *Codec) recoveryBlock(index int) []byte {
	return c.block(c.recoveryBlocks, index)
}

func (c *Codec) inputBlock(index int) []byte {
	return c.block(c.inputBlocks, index)
}

func (c *Codec) compressRow(index int) []uint64 {
	start := index * c.gePitch
	return c.compressMatrix[start : start+c.gePitch]
}

func (c *Codec) geRow(index int) []uint64 {
	start := index * c.gePitch
	return c.geMatrix[start : start+c.gePitch]
}

func (c *Codec) heavyRow(index int) []byte {
	start := index * c.heavyPitch
	return c.heavyMatrix[start : start+c.heavyPitch]
}

func rol64(x uint64, r int) uint64 {
	return bits.RotateLeft64(x, r)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func zeroUint64s(buf []uint64) {
	for i := range buf {
		buf[i] = 0
	}
}

func copyUint64s(dst, src []uint64) {
	copy(dst, src)
}
