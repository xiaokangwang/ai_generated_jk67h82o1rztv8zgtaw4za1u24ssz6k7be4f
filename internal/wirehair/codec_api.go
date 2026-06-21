package wirehair

type Encoder struct {
	codec *Codec
}

type Decoder struct {
	codec *Codec
}

func NewEncoder(message []byte, blockBytes uint32) (*Encoder, error) {
	if err := Init(); err != nil {
		return nil, err
	}
	c := &Codec{}
	if result := c.InitializeEncoder(uint64(len(message)), int(blockBytes)); result != ResultSuccess {
		return nil, &Error{Code: result}
	}
	if result := c.EncodeFeed(message); result != ResultSuccess {
		return nil, &Error{Code: result}
	}
	return &Encoder{codec: c}, nil
}

func NewDecoder(messageBytes uint64, blockBytes uint32) (*Decoder, error) {
	if err := Init(); err != nil {
		return nil, err
	}
	c := &Codec{}
	if result := c.InitializeDecoder(messageBytes, int(blockBytes)); result != ResultSuccess {
		return nil, &Error{Code: result}
	}
	return &Decoder{codec: c}, nil
}

func (e *Encoder) Encode(blockID uint32, blockOut []byte) (int, error) {
	n := e.codec.Encode(blockID, blockOut, len(blockOut))
	if n == 0 {
		return 0, &Error{Code: ResultInvalidInput}
	}
	return n, nil
}

func (d *Decoder) Decode(blockID uint32, block []byte) (State, error) {
	result := d.codec.DecodeFeed(blockID, block, len(block))
	switch result {
	case ResultSuccess:
		return StateReady, nil
	case ResultNeedMore:
		return StateNeedMore, nil
	default:
		return StateNeedMore, &Error{Code: result}
	}
}

func (d *Decoder) Recover(message []byte) error {
	result := d.codec.ReconstructOutput(message, uint64(len(message)))
	if result != ResultSuccess {
		return &Error{Code: result}
	}
	return nil
}

func (d *Decoder) RecoverBlock(blockID uint32, block []byte) (int, error) {
	var out uint32
	result := d.codec.ReconstructBlock(uint16(blockID), block, &out)
	if result != ResultSuccess {
		return 0, &Error{Code: result}
	}
	return int(out), nil
}

func (d *Decoder) BecomeEncoder() (*Encoder, error) {
	if result := d.codec.InitializeEncoderFromDecoder(); result != ResultSuccess {
		return nil, &Error{Code: result}
	}
	d.codec.mode = modeEncoder
	return &Encoder{codec: d.codec}, nil
}

func ResultString(code ResultCode) string {
	return code.String()
}

func (c *Codec) OverrideSeeds(denseCount, pSeed, dSeed uint16) {
	c.denseCount = denseCount
	c.pSeed = uint32(pSeed)
	c.dSeed = uint32(dSeed)
	c.seedOverride = true
}

func (c *Codec) ChooseMatrix(messageBytes uint64, blockBytes int) ResultCode {
	if messageBytes < 1 || blockBytes < 1 {
		return ResultInvalidInput
	}
	c.blockBytes = blockBytes
	c.blockCount = uint16((messageBytes + uint64(c.blockBytes) - 1) / uint64(c.blockBytes))
	c.blockNextPrime = nextPrime16(c.blockCount)
	if c.blockCount < minN {
		return ResultBadInputSmallN
	}
	if c.blockCount > maxN {
		return ResultBadInputLargeN
	}
	if !c.seedOverride {
		c.denseCount = getDenseCount(unsigned(c.blockCount))
		c.dSeed = uint32(getDenseSeed(unsigned(c.blockCount), unsigned(c.denseCount)))
		c.pSeed = uint32(getPeelSeed(unsigned(c.blockCount)))
	}
	c.mixCount = c.denseCount + heavyRows
	c.mixNextPrime = nextPrime16(c.mixCount)
	c.peelHeadRows = listTerm
	c.peelTailRow = nil
	c.deferHeadRows = listTerm
	c.mode = modeUnknown
	return ResultSuccess
}

func (c *Codec) InitializeEncoder(messageBytes uint64, blockBytes int) ResultCode {
	result := c.ChooseMatrix(messageBytes, blockBytes)
	if result != ResultSuccess {
		return result
	}
	partialFinalBytes := int(messageBytes % uint64(c.blockBytes))
	if partialFinalBytes <= 0 {
		partialFinalBytes = c.blockBytes
	}
	c.inputFinalBytes = partialFinalBytes
	c.outputFinalBytes = c.blockBytes
	c.extraCount = 0
	c.originalOutOfOrder = false
	c.mode = modeEncoder
	if !c.AllocateWorkspace() {
		return ResultOOM
	}
	return ResultSuccess
}

func (c *Codec) InitializeDecoder(messageBytes uint64, blockBytes int) ResultCode {
	result := c.ChooseMatrix(messageBytes, blockBytes)
	if result != ResultSuccess {
		return result
	}
	partialFinalBytes := int(messageBytes % uint64(c.blockBytes))
	if partialFinalBytes <= 0 {
		partialFinalBytes = c.blockBytes
	}
	c.rowCount = 0
	c.outputFinalBytes = partialFinalBytes
	c.inputFinalBytes = c.blockBytes
	c.extraCount = maxExtraRows
	c.allOriginal = true
	c.originalOutOfOrder = true
	c.decoderFinal = false
	c.decoderResult = ResultNeedMore
	c.mode = modeDecoder
	if !c.AllocateInput() {
		return ResultOOM
	}
	if !c.AllocateWorkspace() {
		return ResultOOM
	}
	return ResultSuccess
}

func (c *Codec) InitializeEncoderFromDecoder() ResultCode {
	if c.mode == modeDecoder && c.decoderFinal && c.decoderResult != ResultSuccess {
		return c.decoderResult
	}
	if c.allOriginal {
		result := c.SolveMatrix()
		if result != ResultSuccess {
			if result == ResultNeedMore {
				return ResultBadPeelSeed
			}
			return result
		}
		c.GenerateRecoveryBlocks()
	}
	c.inputFinalBytes = c.outputFinalBytes
	c.mode = modeEncoder
	return ResultSuccess
}

func (c *Codec) SetInput(messageIn []byte) {
	totalBytes := int(c.blockCount) * c.blockBytes
	if len(messageIn) >= totalBytes {
		c.inputBlocks = messageIn
		c.inputAllocated = false
		return
	}

	if len(c.inputBlocks) < totalBytes || !c.inputAllocated {
		c.inputBlocks = make([]byte, totalBytes)
		c.inputAllocated = true
	} else {
		zeroBytes(c.inputBlocks[:totalBytes])
	}
	copy(c.inputBlocks, messageIn)
}

func (c *Codec) AllocateInput() bool {
	sizeBytes := int(c.blockCount+c.extraCount) * c.blockBytes
	if len(c.inputBlocks) < sizeBytes || !c.inputAllocated {
		c.inputBlocks = make([]byte, sizeBytes)
		c.inputAllocated = true
	}
	return true
}

func (c *Codec) FreeInput() {
	if c.inputAllocated {
		c.inputBlocks = nil
	}
	c.inputAllocated = false
}

func (c *Codec) AllocateWorkspace() bool {
	recoveryRows := int(c.blockCount+c.mixCount) + 1
	recoverySizeBytes := recoveryRows * c.blockBytes
	rowCount := int(c.blockCount + c.extraCount)
	columnCount := int(c.blockCount)
	c.recoveryRows = recoveryRows
	if len(c.recoveryBlocks) < recoverySizeBytes {
		c.recoveryBlocks = make([]byte, recoverySizeBytes)
	}
	c.peelRows = make([]peelRow, rowCount)
	c.peelCols = make([]peelColumn, columnCount)
	c.peelColRefs = make([]peelRefs, columnCount)
	c.copiedOriginal = make([]byte, rowCount)
	for i := 0; i < columnCount; i++ {
		c.peelColRefs[i].RowCount = 0
		c.peelCols[i].Weight2Refs = 0
		c.peelCols[i].Mark = markTodo
	}
	return true
}

func (c *Codec) FreeWorkspace() {
	c.recoveryBlocks = nil
	c.peelRows = nil
	c.peelCols = nil
	c.peelColRefs = nil
	c.copiedOriginal = nil
	c.decoderFinal = false
	c.decoderResult = ResultNeedMore
}

func (c *Codec) AllocateMatrix() bool {
	geCols := int(c.deferCount + c.mixCount)
	geRows := int(c.deferCount + c.denseCount + c.extraCount + 1)
	gePitch := (geCols + 63) / 64
	compressRows := int(c.blockCount)
	heavyRowsCount := heavyRows + int(c.extraCount)
	heavyColsCount := min(int(c.mixCount), heavyCols)
	heavyPitch := (heavyColsCount + 3 + 3) &^ 3
	c.compressMatrix = make([]uint64, compressRows*gePitch)
	c.geMatrix = make([]uint64, geRows*gePitch)
	c.heavyMatrix = make([]byte, heavyPitch*heavyRowsCount)
	pivotCount := geCols + int(c.extraCount)
	c.pivots = make([]uint16, pivotCount+heavyRows)
	c.geRowMap = make([]uint16, pivotCount)
	c.geColMap = make([]uint16, geCols)
	c.gePitch = gePitch
	c.geRows = geRows
	c.geCols = geCols
	c.heavyPitch = heavyPitch
	c.heavyRows = heavyRowsCount
	c.heavyColumns = heavyColsCount
	c.firstHeavyColumn = int(c.deferCount+c.mixCount) - heavyColsCount
	return true
}

func (c *Codec) FreeMatrix() {
	c.compressMatrix = nil
	c.geMatrix = nil
	c.heavyMatrix = nil
	c.pivots = nil
	c.geRowMap = nil
	c.geColMap = nil
}
