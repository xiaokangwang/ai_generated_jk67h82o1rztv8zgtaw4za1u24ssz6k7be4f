package wirehair

func zeroBytes(buf []byte) {
	for i := range buf {
		buf[i] = 0
	}
}

func (c *Codec) SetupTriangle() {
	pivotCount := int(c.deferCount + c.denseCount)
	for pivotI := 0; pivotI < pivotCount; pivotI++ {
		c.pivots[pivotI] = uint16(pivotI)
	}
	c.nextPivot = 0
	c.pivotCount = pivotCount
	if c.firstHeavyColumn <= 0 {
		c.InsertHeavyRows()
	}
}

func (c *Codec) InsertHeavyRows() {
	firstHeavyPivot := c.pivotCount
	columnCount := int(c.deferCount + c.mixCount)
	firstHeavyRow := int(c.deferCount + c.denseCount)

	for pivotJ := c.pivotCount - 1; pivotJ >= 0; pivotJ-- {
		geRowJ := int(c.pivots[pivotJ])
		if geRowJ < firstHeavyRow {
			continue
		}

		if pivotJ >= c.nextPivot {
			firstHeavyPivot--
			c.pivots[pivotJ] = c.pivots[firstHeavyPivot]
			c.pivots[firstHeavyPivot] = uint16(geRowJ)
		}

		extraRow := c.heavyRow(geRowJ - firstHeavyRow)
		geExtraRow := c.geRow(geRowJ)
		for geColumnJ := c.firstHeavyColumn; geColumnJ < columnCount; geColumnJ++ {
			columnBit := uint8((geExtraRow[geColumnJ>>6] >> (geColumnJ & 63)) & 1)
			extraRow[geColumnJ-c.firstHeavyColumn] = columnBit
		}
	}

	c.firstHeavyPivot = firstHeavyPivot

	for heavyI := 0; heavyI < heavyRows; heavyI++ {
		c.pivots[c.pivotCount+heavyI] = uint16(firstHeavyRow + int(c.extraCount) + heavyI)
	}
	c.pivotCount += heavyRows
}

func (c *Codec) TriangleNonHeavy() bool {
	pivotCount := c.pivotCount
	firstHeavyColumn := c.firstHeavyColumn

	for pivotI := c.nextPivot; pivotI < firstHeavyColumn; pivotI++ {
		wordOffset := pivotI >> 6
		geMask := uint64(1) << (pivotI & 63)
		found := false

		for pivotJ := pivotI; pivotJ < pivotCount; pivotJ++ {
			geRowJ := int(c.pivots[pivotJ])
			geRow := c.geRow(geRowJ)[wordOffset:]
			if geRow[0]&geMask == 0 {
				continue
			}

			found = true
			c.pivots[pivotJ] = c.pivots[pivotI]
			c.pivots[pivotI] = uint16(geRowJ)

			row0 := (geRow[0] & ^(geMask - 1)) ^ geMask
			for pivotK := pivotJ + 1; pivotK < pivotCount; pivotK++ {
				geRowK := int(c.pivots[pivotK])
				remRow := c.geRow(geRowK)[wordOffset:]
				if remRow[0]&geMask == 0 {
					continue
				}
				remRow[0] ^= row0
				for ii := 1; ii < c.gePitch-wordOffset; ii++ {
					remRow[ii] ^= geRow[ii]
				}
			}
			break
		}

		if !found {
			c.nextPivot = pivotI
			return false
		}
	}

	c.nextPivot = firstHeavyColumn
	c.InsertHeavyRows()
	return true
}

func (c *Codec) Triangle() bool {
	if c.nextPivot < c.firstHeavyColumn && !c.TriangleNonHeavy() {
		return false
	}

	pivotCount := c.pivotCount
	columnCount := int(c.deferCount + c.mixCount)
	firstHeavyRow := int(c.deferCount + c.denseCount)
	firstHeavyPivot := c.firstHeavyPivot

	for pivotI := c.nextPivot; pivotI < columnCount; pivotI++ {
		heavyColI := pivotI - c.firstHeavyColumn
		wordOffset := pivotI >> 6
		geMask := uint64(1) << (pivotI & 63)
		found := false
		pivotJ := pivotI

		for ; pivotJ < firstHeavyPivot; pivotJ++ {
			geRowJ := int(c.pivots[pivotJ])
			geRow := c.geRow(geRowJ)[wordOffset:]
			if geRow[0]&geMask == 0 {
				continue
			}

			found = true
			c.pivots[pivotJ] = c.pivots[pivotI]
			c.pivots[pivotI] = uint16(geRowJ)

			row0 := (geRow[0] & ^(geMask - 1)) ^ geMask
			pivotK := pivotJ + 1

			for ; pivotK < firstHeavyPivot; pivotK++ {
				geRowK := int(c.pivots[pivotK])
				remRow := c.geRow(geRowK)[wordOffset:]
				if remRow[0]&geMask == 0 {
					continue
				}
				remRow[0] ^= row0
				for ii := 1; ii < c.gePitch-wordOffset; ii++ {
					remRow[ii] ^= geRow[ii]
				}
			}

			pivotRow := c.geRow(geRowJ)
			for ; pivotK < pivotCount; pivotK++ {
				geRowK := int(c.pivots[pivotK])
				heavyRowK := c.heavyRow(geRowK - firstHeavyRow)
				codeValue := heavyRowK[heavyColI]
				if codeValue == 0 {
					continue
				}
				for geColumnI := pivotI + 1; geColumnI < columnCount; geColumnI++ {
					if (pivotRow[geColumnI>>6]>>(geColumnI&63))&1 != 0 {
						heavyRowK[geColumnI-c.firstHeavyColumn] ^= codeValue
					}
				}
			}

			break
		}

		if !found {
			for ; pivotJ < c.pivotCount; pivotJ++ {
				geRowJ := int(c.pivots[pivotJ])
				heavyRowJ := c.heavyRow(geRowJ - firstHeavyRow)
				codeValue := heavyRowJ[heavyColI]
				if codeValue == 0 {
					continue
				}

				found = true
				c.pivots[pivotJ] = c.pivots[pivotI]
				c.pivots[pivotI] = uint16(geRowJ)

				if pivotI < firstHeavyPivot {
					temp := c.pivots[firstHeavyPivot]
					c.pivots[firstHeavyPivot] = c.pivots[pivotJ]
					c.pivots[pivotJ] = temp
					firstHeavyPivot++
				}

				for pivotK := pivotJ + 1; pivotK < pivotCount; pivotK++ {
					geRowK := int(c.pivots[pivotK])
					heavyRowK := c.heavyRow(geRowK - firstHeavyRow)
					remValue := heavyRowK[heavyColI]
					if remValue == 0 {
						continue
					}
					x := gf256Div(remValue, codeValue)
					heavyRowK[heavyColI] = x
					offset := heavyColI + 1
					gf256MulAddMem(heavyRowK[offset:c.heavyColumns], x, heavyRowJ[offset:c.heavyColumns])
				}

				break
			}
		}

		if !found {
			c.nextPivot = pivotI
			c.firstHeavyPivot = firstHeavyPivot
			return false
		}
	}

	return true
}

func (c *Codec) InitializeColumnValues() {
	firstHeavyRow := int(c.deferCount + c.denseCount)
	columnCount := int(c.deferCount + c.mixCount)
	pivotI := 0

	for ; pivotI < columnCount; pivotI++ {
		destColumnI := int(c.geColMap[pivotI])
		geRowI := int(c.pivots[pivotI])
		dest := c.recoveryBlock(destColumnI)

		if geRowI < int(c.denseCount) || geRowI >= firstHeavyRow+int(c.extraCount) {
			zeroBytes(dest)
			c.geRowMap[geRowI] = uint16(destColumnI)
			continue
		}

		rowI := int(c.geRowMap[geRowI])
		combo := c.inputBlock(rowI)
		row := &c.peelRows[rowI]

		if rowI == int(c.blockCount)-1 {
			copy(dest[:c.inputFinalBytes], combo[:c.inputFinalBytes])
			zeroBytes(dest[c.inputFinalBytes:])
			combo = nil
		}

		iter := NewPeelRowIterator(row.Params, c.blockCount, c.blockNextPrime)
		for {
			columnI := int(iter.GetColumn())
			column := &c.peelCols[columnI]
			if column.Mark == markPeel {
				src := c.recoveryBlock(columnI)
				if combo == nil {
					gf256AddMem(dest, src)
				} else {
					gf256AddSetMem(dest, combo, src)
					combo = nil
				}
			}
			if !iter.Iterate() {
				break
			}
		}

		if combo != nil {
			copy(dest, combo)
		}
	}

	for ; pivotI < c.pivotCount; pivotI++ {
		geRowI := int(c.pivots[pivotI])
		if geRowI < int(c.denseCount) || (geRowI >= firstHeavyRow && geRowI < columnCount) {
			c.geRowMap[geRowI] = listTerm
		}
	}
}

func (c *Codec) MultiplyDenseValues() {
	var prng PCGRandom
	prng.Seed(uint64(c.dSeed), 0)

	denseCount := int(c.denseCount)
	tempBlock := c.recoveryBlock(int(c.blockCount + c.mixCount))
	rows := make([]uint16, denseCount)
	bits := make([]uint16, denseCount)
	blockCount := int(c.blockCount)

	for columnI := 0; columnI < blockCount; columnI += denseCount {
		maxX := denseCount
		if columnI+denseCount > blockCount {
			maxX = blockCount - columnI
		}

		shuffleDeck16(&prng, rows, uint32(c.denseCount))
		shuffleDeck16(&prng, bits, uint32(c.denseCount))

		setCount := int((c.denseCount + 1) >> 1)
		setBits := bits[:setCount]
		clrBits := bits[setCount:]
		rowIndex := 0

		var combo []byte
		comboInTemp := false
		for ii := 0; ii < setCount; ii++ {
			bitI := int(setBits[ii])
			if bitI >= maxX || c.peelCols[columnI+bitI].Mark != markPeel {
				continue
			}
			src := c.recoveryBlock(columnI + bitI)
			if combo == nil {
				combo = src
			} else if comboInTemp {
				gf256AddMem(tempBlock, src)
			} else {
				gf256AddSetMem(tempBlock, combo, src)
				combo = tempBlock
				comboInTemp = true
			}
		}

		if combo == nil {
			zeroBytes(tempBlock)
		} else if !comboInTemp {
			copy(tempBlock, combo)
		}

		destColumnI := c.geRowMap[rows[rowIndex]]
		if destColumnI != listTerm {
			gf256AddMem(c.recoveryBlock(int(destColumnI)), tempBlock)
		}
		rowIndex++

		shuffleDeck16(&prng, bits, uint32(c.denseCount))
		setBits = bits[:setCount]
		clrBits = bits[setCount:]
		loopCount := denseCount >> 1

		for ii := 0; ii < loopCount; ii++ {
			bit0 := int(setBits[ii])
			bit1 := int(clrBits[ii])
			if bit0 < maxX && c.peelCols[columnI+bit0].Mark == markPeel {
				if bit1 < maxX && c.peelCols[columnI+bit1].Mark == markPeel {
					gf256Add2Mem(tempBlock, c.recoveryBlock(columnI+bit0), c.recoveryBlock(columnI+bit1))
				} else {
					gf256AddMem(tempBlock, c.recoveryBlock(columnI+bit0))
				}
			} else if bit1 < maxX && c.peelCols[columnI+bit1].Mark == markPeel {
				gf256AddMem(tempBlock, c.recoveryBlock(columnI+bit1))
			}

			destColumnI = c.geRowMap[rows[rowIndex]]
			if destColumnI != listTerm {
				gf256AddMem(c.recoveryBlock(int(destColumnI)), tempBlock)
			}
			rowIndex++
		}

		shuffleDeck16(&prng, bits, uint32(c.denseCount))
		setBits = bits[:setCount]
		clrBits = bits[setCount:]
		secondLoopCount := loopCount - 1 + denseCount&1

		for ii := 0; ii < secondLoopCount; ii++ {
			bit0 := int(setBits[ii])
			bit1 := int(clrBits[ii])
			if bit0 < maxX && c.peelCols[columnI+bit0].Mark == markPeel {
				if bit1 < maxX && c.peelCols[columnI+bit1].Mark == markPeel {
					gf256Add2Mem(tempBlock, c.recoveryBlock(columnI+bit0), c.recoveryBlock(columnI+bit1))
				} else {
					gf256AddMem(tempBlock, c.recoveryBlock(columnI+bit0))
				}
			} else if bit1 < maxX && c.peelCols[columnI+bit1].Mark == markPeel {
				gf256AddMem(tempBlock, c.recoveryBlock(columnI+bit1))
			}

			destColumnI = c.geRowMap[rows[rowIndex]]
			if destColumnI != listTerm {
				gf256AddMem(c.recoveryBlock(int(destColumnI)), tempBlock)
			}
			rowIndex++
		}
	}
}

func (c *Codec) AddSubdiagonalValues() {
	columnCount := int(c.deferCount + c.mixCount)
	firstHeavyRow := int(c.deferCount + c.denseCount)

	for geColumnI := 1; geColumnI < columnCount; geColumnI++ {
		columnI := int(c.geColMap[geColumnI])
		geRowI := int(c.pivots[geColumnI])
		dest := c.recoveryBlock(columnI)
		geLimit := geColumnI

		if geRowI >= firstHeavyRow {
			heavyRowI := geRowI - firstHeavyRow
			heavyRow := c.heavyRow(heavyRowI)
			for subI := c.firstHeavyColumn; subI < geLimit; subI++ {
				codeValue := heavyRow[subI-c.firstHeavyColumn]
				if codeValue == 0 {
					continue
				}
				src := c.recoveryBlock(int(c.geColMap[subI]))
				gf256MulAddMem(dest, codeValue, src)
			}

			if heavyRowI >= int(c.extraCount) {
				continue
			}
			if geLimit > c.firstHeavyColumn {
				geLimit = c.firstHeavyColumn
			}
		}

		geRow := c.geRow(geRowI)
		for bitJ := 0; bitJ < geLimit; bitJ++ {
			if (geRow[bitJ>>6]>>(bitJ&63))&1 == 0 {
				continue
			}
			src := c.recoveryBlock(int(c.geColMap[bitJ]))
			gf256AddMem(dest, src)
		}
	}
}

func (c *Codec) BackSubstituteAboveDiagonal() {
	pivotCount := int(c.deferCount + c.mixCount)
	if pivotCount == 0 {
		return
	}

	firstHeavyRow := int(c.deferCount + c.denseCount)
	for pivotI := pivotCount - 1; ; pivotI-- {
		src := c.recoveryBlock(int(c.geColMap[pivotI]))
		geRowI := int(c.pivots[pivotI])

		if geRowI >= firstHeavyRow && pivotI >= c.firstHeavyColumn {
			heavyRowI := geRowI - firstHeavyRow
			heavyColI := pivotI - c.firstHeavyColumn
			codeValue := c.heavyRow(heavyRowI)[heavyColI]
			if codeValue != 1 {
				gf256DivMem(src, src, codeValue)
			}
		}

		geMask := uint64(1) << (pivotI & 63)
		for geUpI := 0; geUpI < pivotI; geUpI++ {
			upRowI := int(c.pivots[geUpI])
			dest := c.recoveryBlock(int(c.geColMap[geUpI]))

			if upRowI >= firstHeavyRow && pivotI >= c.firstHeavyColumn {
				heavyRowI := upRowI - firstHeavyRow
				heavyColI := pivotI - c.firstHeavyColumn
				codeValue := c.heavyRow(heavyRowI)[heavyColI]
				if codeValue == 0 {
					continue
				}
				gf256MulAddMem(dest, codeValue, src)
				continue
			}

			if c.geRow(upRowI)[pivotI>>6]&geMask != 0 {
				gf256AddMem(dest, src)
			}
		}

		if pivotI == 0 {
			break
		}
	}
}

func (c *Codec) Substitute() {
	for rowI := c.peelHeadRows; rowI != listTerm; rowI = c.peelRows[rowI].NextRow {
		row := &c.peelRows[rowI]
		destColumnI := int(row.Marks.Result.PeelColumn)
		dest := c.recoveryBlock(destColumnI)
		inputSrc := c.inputBlock(int(rowI))
		mix := NewRowMixIterator(row.Params, c.mixCount, c.mixNextPrime)
		src := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[0]))

		if int(rowI) != int(c.blockCount)-1 {
			gf256AddSetMem(dest, src, inputSrc)
		} else {
			gf256AddSetMem(dest[:c.inputFinalBytes], src[:c.inputFinalBytes], inputSrc[:c.inputFinalBytes])
			copy(dest[c.inputFinalBytes:], src[c.inputFinalBytes:])
		}

		src0 := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[1]))
		src1 := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[2]))
		gf256Add2Mem(dest, src0, src1)

		if row.Params.PeelCount >= 2 {
			iter := NewPeelRowIterator(row.Params, c.blockCount, c.blockNextPrime)
			column0 := iter.GetColumn()
			iter.Iterate()
			column1 := iter.GetColumn()

			if column0 != row.Marks.Result.PeelColumn {
				peel0 := c.recoveryBlock(int(column0))
				if column1 != row.Marks.Result.PeelColumn {
					gf256Add2Mem(dest, peel0, c.recoveryBlock(int(column1)))
				} else {
					gf256AddMem(dest, peel0)
				}
			} else {
				gf256AddMem(dest, c.recoveryBlock(int(column1)))
			}

			for iter.Iterate() {
				columnI := iter.GetColumn()
				if columnI != row.Marks.Result.PeelColumn {
					gf256AddMem(dest, c.recoveryBlock(int(columnI)))
				}
			}
		}
	}
}

func (c *Codec) SolveMatrix() ResultCode {
	c.GreedyPeeling()

	if !c.AllocateMatrix() {
		return ResultOOM
	}

	c.SetDeferredColumns()
	c.SetMixingColumnsForDeferredRows()
	c.PeelDiagonal()
	c.CopyDeferredRows()
	c.MultiplyDenseRows()
	c.SetHeavyRows()
	addInvertibleGF2Matrix(c.geMatrix, uint(c.deferCount), uint(c.gePitch), uint(c.denseCount))

	c.SetupTriangle()
	if !c.Triangle() {
		return ResultNeedMore
	}

	return ResultSuccess
}

func (c *Codec) GenerateRecoveryBlocks() {
	c.InitializeColumnValues()
	c.MultiplyDenseValues()
	c.AddSubdiagonalValues()
	c.BackSubstituteAboveDiagonal()
	c.Substitute()
}

func (c *Codec) ResumeSolveMatrix(id uint32, data []byte) ResultCode {
	if data == nil {
		return ResultInvalidInput
	}

	var rowI, geRowI, newPivotI int

	if int(c.rowCount) >= int(c.blockCount+c.extraCount) {
		firstHeavyRow := int(c.deferCount + c.denseCount)
		newPivotI = -1
		for pivotI := c.nextPivot; pivotI < c.pivotCount; pivotI++ {
			geRowK := int(c.pivots[pivotI])
			if geRowK >= firstHeavyRow && geRowK < firstHeavyRow+int(c.extraCount) {
				newPivotI = pivotI
				break
			}
		}
		if newPivotI < 0 {
			return ResultExtraInsufficient
		}
		geRowI = int(c.pivots[newPivotI])
		rowI = int(c.geRowMap[geRowI])
	} else {
		newPivotI = c.pivotCount
		c.pivotCount++
		rowI = int(c.rowCount)
		c.rowCount++
		geRowI = int(c.deferCount+c.denseCount) + rowI - int(c.blockCount)
		c.geRowMap[geRowI] = uint16(rowI)
		c.pivots[newPivotI] = uint16(geRowI)
	}

	row := &c.peelRows[rowI]
	row.RecoveryID = id
	blockStoreDest := c.inputBlock(rowI)

	if id != uint32(c.blockCount)-1 {
		copy(blockStoreDest, data[:c.blockBytes])
	} else {
		copy(blockStoreDest[:c.outputFinalBytes], data[:c.outputFinalBytes])
		zeroBytes(blockStoreDest[c.outputFinalBytes:])
	}

	geNewRow := c.geRow(geRowI)
	zeroUint64s(geNewRow)

	row.Params.Initialize(id, c.pSeed, c.blockCount, c.mixCount)
	iter := NewPeelRowIterator(row.Params, c.blockCount, c.blockNextPrime)
	mix := NewRowMixIterator(row.Params, c.mixCount, c.mixNextPrime)

	geColumnI := int(mix.Columns[0]) + int(c.deferCount)
	geNewRow[geColumnI>>6] ^= uint64(1) << (geColumnI & 63)
	geColumnI = int(mix.Columns[1]) + int(c.deferCount)
	geNewRow[geColumnI>>6] ^= uint64(1) << (geColumnI & 63)
	geColumnI = int(mix.Columns[2]) + int(c.deferCount)
	geNewRow[geColumnI>>6] ^= uint64(1) << (geColumnI & 63)

	for {
		column := iter.GetColumn()
		refCol := &c.peelCols[column]
		if refCol.Mark == markPeel {
			geSrcRow := c.compressRow(int(refCol.PeelRow))
			for ii := 0; ii < c.gePitch; ii++ {
				geNewRow[ii] ^= geSrcRow[ii]
			}
		} else {
			geColumnK := int(refCol.GEColumn)
			geNewRow[geColumnK>>6] ^= uint64(1) << (geColumnK & 63)
		}
		if !iter.Iterate() {
			break
		}
	}

	for pivotJ := 0; pivotJ < c.nextPivot && pivotJ < c.firstHeavyColumn; pivotJ++ {
		wordOffset := pivotJ >> 6
		geMask := uint64(1) << (pivotJ & 63)
		remRow := geNewRow[wordOffset:]
		if remRow[0]&geMask == 0 {
			continue
		}
		geRowJ := int(c.pivots[pivotJ])
		gePivotRow := c.geRow(geRowJ)[wordOffset:]
		row0 := (gePivotRow[0] & ^(geMask - 1)) ^ geMask
		remRow[0] ^= row0
		for ii := 1; ii < c.gePitch-wordOffset; ii++ {
			remRow[ii] ^= gePivotRow[ii]
		}
	}

	if c.nextPivot < c.firstHeavyColumn {
		bit := geNewRow[c.nextPivot>>6] & (uint64(1) << (c.nextPivot & 63))
		if bit == 0 {
			return ResultNeedMore
		}
		c.pivots[newPivotI] = c.pivots[c.nextPivot]
		c.pivots[c.nextPivot] = uint16(geRowI)
	} else {
		columnCount := int(c.deferCount + c.mixCount)
		firstHeavyRow := int(c.deferCount + c.denseCount)
		heavyRowI := geRowI - firstHeavyRow
		heavyRow := c.heavyRow(heavyRowI)

		for geColumnJ := c.firstHeavyColumn; geColumnJ < columnCount; geColumnJ++ {
			heavyColJ := geColumnJ - c.firstHeavyColumn
			bitJ := uint8((geNewRow[geColumnJ>>6] >> (geColumnJ & 63)) & 1)
			heavyRow[heavyColJ] = bitJ
		}

		for pivotJ := c.firstHeavyColumn; pivotJ < c.nextPivot; pivotJ++ {
			heavyColJ := pivotJ - c.firstHeavyColumn
			codeValue := heavyRow[heavyColJ]
			if codeValue == 0 {
				continue
			}

			geRowJ := int(c.pivots[pivotJ])
			if geRowJ >= firstHeavyRow {
				heavyRowJ := geRowJ - firstHeavyRow
				heavyPivotRow := c.heavyRow(heavyRowJ)
				pivotCode := heavyPivotRow[heavyColJ]
				startColumn := heavyColJ + 1
				if pivotCode == 1 {
					gf256MulAddMem(heavyRow[startColumn:c.heavyColumns], codeValue, heavyPivotRow[startColumn:c.heavyColumns])
				} else {
					eliminator := gf256Div(codeValue, pivotCode)
					heavyRow[heavyColJ] = eliminator
					gf256MulAddMem(heavyRow[startColumn:c.heavyColumns], eliminator, heavyPivotRow[startColumn:c.heavyColumns])
				}
			} else {
				otherRow := c.geRow(geRowJ)
				for geColumnK := pivotJ + 1; geColumnK < columnCount; geColumnK++ {
					if (otherRow[geColumnK>>6]>>(geColumnK&63))&1 != 0 {
						heavyRow[geColumnK-c.firstHeavyColumn] ^= codeValue
					}
				}
			}
		}

		nextHeavyCol := c.nextPivot - c.firstHeavyColumn
		if heavyRow[nextHeavyCol] == 0 {
			return ResultNeedMore
		}

		if c.nextPivot < c.firstHeavyPivot {
			c.pivots[newPivotI] = c.pivots[c.firstHeavyPivot]
			c.pivots[c.firstHeavyPivot] = c.pivots[c.nextPivot]
			c.firstHeavyPivot++
		} else {
			c.pivots[newPivotI] = c.pivots[c.nextPivot]
		}
		c.pivots[c.nextPivot] = uint16(geRowI)
	}

	c.nextPivot++
	if c.nextPivot == c.firstHeavyColumn {
		c.InsertHeavyRows()
	}

	if c.Triangle() {
		return ResultSuccess
	}
	return ResultNeedMore
}

func (c *Codec) setDecoderFinal(result ResultCode) ResultCode {
	c.decoderFinal = true
	c.decoderResult = result
	return result
}

func (c *Codec) IsAllOriginalData() bool {
	for i := 0; i < int(c.blockCount); i++ {
		c.copiedOriginal[i] = 0
	}

	seenRows := 0
	for rowI := 0; rowI < int(c.rowCount); rowI++ {
		id := c.peelRows[rowI].RecoveryID
		if id < uint32(c.blockCount) && c.copiedOriginal[id] == 0 {
			c.copiedOriginal[id] = 1
			seenRows++
		}
	}
	return seenRows >= int(c.blockCount)
}

func (c *Codec) ReconstructBlock(blockID uint16, blockOut []byte, bytesOut *uint32) ResultCode {
	if bytesOut != nil {
		*bytesOut = 0
	}
	if c.mode == modeDecoder && c.decoderFinal && c.decoderResult != ResultSuccess {
		return c.decoderResult
	}
	if blockOut == nil || int(blockID) >= int(c.blockCount) {
		return ResultInvalidInput
	}

	if c.allOriginal {
		for rowI := 0; rowI < int(c.rowCount); rowI++ {
			id := c.peelRows[rowI].RecoveryID
			if id != uint32(blockID) {
				continue
			}

			bytes := c.blockBytes
			if id == uint32(c.blockCount)-1 {
				bytes = c.outputFinalBytes
			}
			if len(blockOut) < bytes {
				return ResultInvalidInput
			}
			copy(blockOut[:bytes], c.inputBlocks[rowI*c.blockBytes:rowI*c.blockBytes+bytes])
			if bytesOut != nil {
				*bytesOut = uint32(bytes)
			}
			return ResultSuccess
		}
		return ResultError
	}

	blockBytes := c.blockBytes
	if int(blockID) == int(c.blockCount)-1 {
		blockBytes = c.outputFinalBytes
	}
	if len(blockOut) < blockBytes {
		return ResultInvalidInput
	}

	params := PeelRowParameters{}
	params.Initialize(uint32(blockID), c.pSeed, c.blockCount, c.mixCount)
	iter := NewPeelRowIterator(params, c.blockCount, c.blockNextPrime)
	mix := NewRowMixIterator(params, c.mixCount, c.mixNextPrime)

	peel0 := int(iter.GetColumn())
	first := c.recoveryBlock(peel0)

	if iter.Iterate() {
		peel1 := int(iter.GetColumn())
		gf256AddSetMem(blockOut[:blockBytes], first[:blockBytes], c.recoveryBlock(peel1)[:blockBytes])
		for iter.Iterate() {
			peelX := int(iter.GetColumn())
			gf256AddMem(blockOut[:blockBytes], c.recoveryBlock(peelX)[:blockBytes])
		}
		gf256AddMem(blockOut[:blockBytes], c.recoveryBlock(int(c.blockCount) + int(mix.Columns[0]))[:blockBytes])
	} else {
		gf256AddSetMem(blockOut[:blockBytes], first[:blockBytes], c.recoveryBlock(int(c.blockCount) + int(mix.Columns[0]))[:blockBytes])
	}

	mix0Src := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[1]))
	mix1Src := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[2]))
	gf256Add2Mem(blockOut[:blockBytes], mix0Src[:blockBytes], mix1Src[:blockBytes])

	if bytesOut != nil {
		*bytesOut = uint32(blockBytes)
	}
	return ResultSuccess
}

func (c *Codec) ReconstructOutput(messageOut []byte, messageBytes uint64) ResultCode {
	if c.mode == modeDecoder && c.decoderFinal && c.decoderResult != ResultSuccess {
		return c.decoderResult
	}
	if messageOut == nil {
		return ResultInvalidInput
	}
	expectedBytes := uint64(c.blockBytes)*uint64(c.blockCount-1) + uint64(c.outputFinalBytes)
	if messageBytes != expectedBytes || uint64(len(messageOut)) < messageBytes {
		return ResultInvalidInput
	}

	for i := 0; i < int(c.blockCount); i++ {
		c.copiedOriginal[i] = 0
	}

	for rowI := 0; rowI < int(c.rowCount); rowI++ {
		blockID := c.peelRows[rowI].RecoveryID
		if blockID >= uint32(c.blockCount) {
			continue
		}
		dest := messageOut[int(blockID)*c.blockBytes:]
		bytes := c.blockBytes
		if blockID == uint32(c.blockCount)-1 {
			bytes = c.outputFinalBytes
		}
		copy(dest[:bytes], c.inputBlocks[rowI*c.blockBytes:rowI*c.blockBytes+bytes])
		c.copiedOriginal[blockID] = 1
	}

	for blockID := 0; blockID < int(c.blockCount); blockID++ {
		if c.copiedOriginal[blockID] != 0 {
			continue
		}

		blockBytes := c.blockBytes
		if blockID+1 == int(c.blockCount) {
			blockBytes = c.outputFinalBytes
		}
		dest := messageOut[blockID*c.blockBytes : blockID*c.blockBytes+blockBytes]

		params := PeelRowParameters{}
		params.Initialize(uint32(blockID), c.pSeed, c.blockCount, c.mixCount)
		iter := NewPeelRowIterator(params, c.blockCount, c.blockNextPrime)
		mix := NewRowMixIterator(params, c.mixCount, c.mixNextPrime)

		peel0 := int(iter.GetColumn())
		first := c.recoveryBlock(peel0)

		if iter.Iterate() {
			peel1 := int(iter.GetColumn())
			gf256AddSetMem(dest, first[:blockBytes], c.recoveryBlock(peel1)[:blockBytes])
			for iter.Iterate() {
				peelX := int(iter.GetColumn())
				gf256AddMem(dest, c.recoveryBlock(peelX)[:blockBytes])
			}
			gf256AddMem(dest, c.recoveryBlock(int(c.blockCount) + int(mix.Columns[0]))[:blockBytes])
		} else {
			gf256AddSetMem(dest, first[:blockBytes], c.recoveryBlock(int(c.blockCount) + int(mix.Columns[0]))[:blockBytes])
		}

		mix0Src := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[1]))
		mix1Src := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[2]))
		gf256Add2Mem(dest, mix0Src[:blockBytes], mix1Src[:blockBytes])
	}

	return ResultSuccess
}

func (c *Codec) EncodeFeed(messageIn []byte) ResultCode {
	if messageIn == nil {
		return ResultInvalidInput
	}

	c.SetInput(messageIn)
	for id := uint16(0); id < c.blockCount; id++ {
		if !c.OpportunisticPeeling(id, uint32(id)) {
			return ResultBadPeelSeed
		}
	}

	result := c.SolveMatrix()
	if result == ResultSuccess {
		c.GenerateRecoveryBlocks()
		return ResultSuccess
	}
	if result == ResultNeedMore {
		return ResultBadPeelSeed
	}
	return result
}

func (c *Codec) Encode(blockID uint32, blockOut []byte, outBufferBytes int) int {
	if blockOut == nil {
		return 0
	}

	copyBytes := c.blockBytes
	if uint16(blockID) == c.blockCount-1 {
		copyBytes = c.inputFinalBytes
	}
	if outBufferBytes < copyBytes || len(blockOut) < copyBytes {
		return 0
	}

	if blockID < uint32(c.blockCount) && !c.originalOutOfOrder {
		copy(blockOut[:copyBytes], c.inputBlocks[int(blockID)*c.blockBytes:int(blockID)*c.blockBytes+copyBytes])
		return copyBytes
	}

	params := PeelRowParameters{}
	params.Initialize(blockID, c.pSeed, c.blockCount, c.mixCount)
	iter := NewPeelRowIterator(params, c.blockCount, c.blockNextPrime)
	mix := NewRowMixIterator(params, c.mixCount, c.mixNextPrime)

	peel0 := int(iter.GetColumn())
	first := c.recoveryBlock(peel0)
	mix0Src := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[0]))

	if iter.Iterate() {
		peel1 := int(iter.GetColumn())
		gf256AddSetMem(blockOut[:copyBytes], first[:copyBytes], c.recoveryBlock(peel1)[:copyBytes])
		for iter.Iterate() {
			peelX := int(iter.GetColumn())
			gf256AddMem(blockOut[:copyBytes], c.recoveryBlock(peelX)[:copyBytes])
		}
		gf256AddMem(blockOut[:copyBytes], mix0Src[:copyBytes])
	} else {
		gf256AddSetMem(blockOut[:copyBytes], first[:copyBytes], mix0Src[:copyBytes])
	}

	mix1Src := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[1]))
	mix2Src := c.recoveryBlock(int(c.blockCount) + int(mix.Columns[2]))
	gf256Add2Mem(blockOut[:copyBytes], mix1Src[:copyBytes], mix2Src[:copyBytes])
	return copyBytes
}

func (c *Codec) DecodeFeed(blockID uint32, blockIn []byte, blockBytes int) ResultCode {
	if c.decoderFinal {
		return c.decoderResult
	}
	if blockIn == nil {
		return ResultInvalidInput
	}

	isFinalBlock := blockID+1 == uint32(c.blockCount)
	if isFinalBlock {
		if c.outputFinalBytes > blockBytes {
			return ResultInvalidInput
		}
	} else if c.blockBytes != blockBytes {
		return ResultInvalidInput
	}

	if blockID >= uint32(c.blockCount) {
		c.allOriginal = false
	}

	rowI := c.rowCount
	if rowI >= c.blockCount {
		result := c.ResumeSolveMatrix(blockID, blockIn)
		if result == ResultSuccess {
			c.GenerateRecoveryBlocks()
			return c.setDecoderFinal(ResultSuccess)
		}
		if result != ResultNeedMore {
			return c.setDecoderFinal(result)
		}
		return result
	}

	if !c.OpportunisticPeeling(rowI, blockID) {
		return ResultNeedMore
	}

	dest := c.inputBlock(int(rowI))
	if isFinalBlock {
		copy(dest[:c.outputFinalBytes], blockIn[:c.outputFinalBytes])
		zeroBytes(dest[c.outputFinalBytes:])
	} else {
		copy(dest, blockIn[:c.blockBytes])
	}

	c.rowCount++
	if c.rowCount != c.blockCount {
		return ResultNeedMore
	}

	if c.allOriginal {
		if !c.IsAllOriginalData() {
			c.allOriginal = false
			return c.setDecoderFinal(ResultInvalidInput)
		}
		return c.setDecoderFinal(ResultSuccess)
	}

	result := c.SolveMatrix()
	if result == ResultSuccess {
		c.GenerateRecoveryBlocks()
		return c.setDecoderFinal(ResultSuccess)
	}
	if result != ResultNeedMore {
		return c.setDecoderFinal(result)
	}
	return result
}
