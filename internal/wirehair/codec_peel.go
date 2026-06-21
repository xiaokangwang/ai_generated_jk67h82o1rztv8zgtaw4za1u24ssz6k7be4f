package wirehair

func (c *Codec) OpportunisticPeeling(rowI uint16, rowSeed uint32) bool {
	row := &c.peelRows[rowI]
	row.RecoveryID = rowSeed
	row.Params.Initialize(rowSeed, c.pSeed, c.blockCount, c.mixCount)
	iter := NewPeelRowIterator(row.Params, c.blockCount, c.blockNextPrime)
	unmarkedCount := uint16(0)
	var unmarked [2]uint16
	for {
		columnI := iter.GetColumn()
		refs := &c.peelColRefs[columnI]
		if refs.RowCount >= refListMax {
			c.FixPeelFailure(row, columnI)
			return false
		}
		refs.Rows[refs.RowCount] = rowI
		refs.RowCount++
		if c.peelCols[columnI].Mark == markTodo {
			unmarked[unmarkedCount&1] = columnI
			unmarkedCount++
		}
		if !iter.Iterate() {
			break
		}
	}
	row.UnmarkedCount = unmarkedCount
	switch unmarkedCount {
	case 0:
		row.NextRow = c.deferHeadRows
		c.deferHeadRows = rowI
	case 1:
		c.SolveWithPeel(row, rowI, unmarked[0])
	case 2:
		row.Marks.Unmarked[0] = unmarked[0]
		row.Marks.Unmarked[1] = unmarked[1]
		c.peelCols[unmarked[0]].Weight2Refs++
		c.peelCols[unmarked[1]].Weight2Refs++
	}
	return true
}

func (c *Codec) FixPeelFailure(row *peelRow, failColumnI uint16) {
	iter := NewPeelRowIterator(row.Params, c.blockCount, c.blockNextPrime)
	for {
		column := iter.GetColumn()
		if column == failColumnI {
			break
		}
		refs := &c.peelColRefs[column]
		refs.RowCount--
		if !iter.Iterate() {
			break
		}
	}
}

func (c *Codec) PeelAvalancheOnSolve(columnI uint16) {
	refs := &c.peelColRefs[columnI]
	refRowCount := refs.RowCount
	for idx := uint16(0); idx < refRowCount; idx++ {
		refRowI := refs.Rows[idx]
		refRow := &c.peelRows[refRowI]
		unmarkedCount := refRow.UnmarkedCount - 1
		refRow.UnmarkedCount = unmarkedCount
		if unmarkedCount == 1 {
			newColumnI := refRow.Marks.Unmarked[0]
			if newColumnI == columnI {
				newColumnI = refRow.Marks.Unmarked[1]
			}
			if c.peelCols[newColumnI].Mark == markTodo {
				c.SolveWithPeel(refRow, refRowI, newColumnI)
				continue
			}
			refRow.NextRow = c.deferHeadRows
			c.deferHeadRows = refRowI
		} else if unmarkedCount == 2 {
			refIter := NewPeelRowIterator(refRow.Params, c.blockCount, c.blockNextPrime)
			storeCount := 0
			for {
				refColumnI := refIter.GetColumn()
				refCol := &c.peelCols[refColumnI]
				if refCol.Mark == markTodo {
					refRow.Marks.Unmarked[storeCount] = refColumnI
					storeCount++
					refCol.Weight2Refs++
				}
				if !refIter.Iterate() {
					break
				}
			}
			if storeCount <= 1 {
				refRow.UnmarkedCount = 0
				if storeCount == 1 {
					c.SolveWithPeel(refRow, refRowI, refRow.Marks.Unmarked[0])
					continue
				}
				refRow.NextRow = c.deferHeadRows
				c.deferHeadRows = refRowI
			}
		}
	}
}

func (c *Codec) SolveWithPeel(row *peelRow, rowI, columnI uint16) {
	column := &c.peelCols[columnI]
	column.Mark = markPeel
	row.Marks.Result.PeelColumn = columnI
	if c.peelTailRow != nil {
		c.peelTailRow.NextRow = rowI
	} else {
		c.peelHeadRows = rowI
	}
	row.NextRow = listTerm
	c.peelTailRow = row
	row.Marks.Result.IsCopied = 0
	c.PeelAvalancheOnSolve(columnI)
	column.PeelRow = rowI
}

func (c *Codec) GreedyPeeling() {
	c.deferHeadColumns = listTerm
	c.deferCount = 0
	for {
		bestColumnI := uint16(listTerm)
		bestW2Refs := uint16(0)
		bestRowCount := uint16(0)
		for columnI := uint16(0); columnI < c.blockCount; columnI++ {
			column := c.peelCols[columnI]
			if column.Mark != markTodo {
				continue
			}
			w2Refs := column.Weight2Refs
			if w2Refs >= bestW2Refs {
				rowCount := c.peelColRefs[columnI].RowCount
				if w2Refs > bestW2Refs || rowCount >= bestRowCount {
					bestColumnI = columnI
					bestW2Refs = w2Refs
					bestRowCount = rowCount
				}
			}
		}
		if bestColumnI == listTerm {
			break
		}
		bestColumn := &c.peelCols[bestColumnI]
		bestColumn.Mark = markDefer
		c.deferCount++
		bestColumn.Next = c.deferHeadColumns
		c.deferHeadColumns = bestColumnI
		c.PeelAvalancheOnSolve(bestColumnI)
	}
}

func (c *Codec) SetDeferredColumns() {
	var column *peelColumn
	geColumnI := uint16(0)
	for deferI := c.deferHeadColumns; deferI != listTerm; deferI = column.Next {
		column = &c.peelCols[deferI]
		refs := &c.peelColRefs[deferI]
		for i := uint16(0); i < refs.RowCount; i++ {
			rowI := refs.Rows[i]
			row := c.compressRow(int(rowI))
			row[geColumnI>>6] |= uint64(1) << (geColumnI & 63)
		}
		c.geColMap[geColumnI] = deferI
		column.GEColumn = geColumnI
		geColumnI++
	}
	for addedI := uint16(0); addedI < c.mixCount; addedI++ {
		geCol := c.deferCount + addedI
		col := c.blockCount + addedI
		c.geColMap[geCol] = col
	}
}

func (c *Codec) SetMixingColumnsForDeferredRows() {
	for deferRowI := c.deferHeadRows; deferRowI != listTerm; deferRowI = c.peelRows[deferRowI].NextRow {
		row := &c.peelRows[deferRowI]
		row.Marks.Result.PeelColumn = listTerm
		geRow := c.compressRow(int(deferRowI))
		mix := NewRowMixIterator(row.Params, c.mixCount, c.mixNextPrime)
		for _, col := range mix.Columns {
			geColumn := uint16(c.deferCount) + col
			geRow[geColumn>>6] ^= uint64(1) << (geColumn & 63)
		}
	}
}

func (c *Codec) copyInputBlockToRecovery(destIndex int, rowI uint16) {
	dest := c.recoveryBlock(destIndex)
	src := c.inputBlock(int(rowI))
	if int(rowI) != int(c.blockCount)-1 {
		copy(dest, src)
		return
	}
	copy(dest[:c.inputFinalBytes], src[:c.inputFinalBytes])
	for i := c.inputFinalBytes; i < c.blockBytes; i++ {
		dest[i] = 0
	}
}

func (c *Codec) PeelDiagonal() {
	for peelRowI := c.peelHeadRows; peelRowI != listTerm; peelRowI = c.peelRows[peelRowI].NextRow {
		row := &c.peelRows[peelRowI]
		peelColumnI := row.Marks.Result.PeelColumn
		geRow := c.compressRow(int(peelRowI))
		mix := NewRowMixIterator(row.Params, c.mixCount, c.mixNextPrime)
		for _, col := range mix.Columns {
			geCol := int(c.deferCount) + int(col)
			geRow[geCol>>6] ^= uint64(1) << (geCol & 63)
		}
		tempBlockSrc := c.recoveryBlock(int(peelColumnI))
		if row.Marks.Result.IsCopied == 0 {
			c.copyInputBlockToRecovery(int(peelColumnI), peelRowI)
		}
		refs := &c.peelColRefs[peelColumnI]
		for i := uint16(0); i < refs.RowCount; i++ {
			refRowI := refs.Rows[i]
			if refRowI == peelRowI {
				continue
			}
			geRefRow := c.compressRow(int(refRowI))
			for j := 0; j < c.gePitch; j++ {
				geRefRow[j] ^= geRow[j]
			}
			refRow := &c.peelRows[refRowI]
			refColumnI := refRow.Marks.Result.PeelColumn
			if refColumnI != listTerm {
				tempBlockDest := c.recoveryBlock(int(refColumnI))
				if refRow.Marks.Result.IsCopied != 0 {
					gf256AddMem(tempBlockDest, tempBlockSrc)
				} else {
					blockSrc := c.inputBlock(int(refRowI))
					if int(refRowI) != int(c.blockCount)-1 {
						gf256AddSetMem(tempBlockDest, tempBlockSrc, blockSrc)
					} else {
						gf256AddSetMem(tempBlockDest[:c.inputFinalBytes], tempBlockSrc[:c.inputFinalBytes], blockSrc[:c.inputFinalBytes])
						copy(tempBlockDest[c.inputFinalBytes:], tempBlockSrc[c.inputFinalBytes:])
					}
					refRow.Marks.Result.IsCopied = 1
				}
			}
		}
	}
}

func (c *Codec) CopyDeferredRows() {
	geRowI := int(c.denseCount)
	for deferRowI := c.deferHeadRows; deferRowI != listTerm; deferRowI = c.peelRows[deferRowI].NextRow {
		geRow := c.geRow(geRowI)
		compressRow := c.compressRow(int(deferRowI))
		copyUint64s(geRow, compressRow)
		c.geRowMap[geRowI] = deferRowI
		geRowI++
	}
}

func (c *Codec) MultiplyDenseRows() {
	var prng PCGRandom
	prng.Seed(uint64(c.dSeed), 0)
	tempRow := c.geRow(int(c.denseCount + c.deferCount))
	rows := make([]uint16, c.denseCount)
	bits := make([]uint16, c.denseCount)
	for columnI := uint16(0); columnI < c.blockCount; columnI += c.denseCount {
		maxX := int(c.denseCount)
		if int(columnI)+int(c.denseCount) > int(c.blockCount) {
			maxX = int(c.blockCount - columnI)
		}
		shuffleDeck16(&prng, rows, uint32(c.denseCount))
		shuffleDeck16(&prng, bits, uint32(c.denseCount))
		setCount := int((c.denseCount + 1) >> 1)
		setBits := bits[:setCount]
		clrBits := bits[setCount:]
		zeroUint64s(tempRow)
		columnBase := c.peelCols[columnI:]
		for ii := 0; ii < setCount; ii++ {
			bitI := int(setBits[ii])
			if bitI < maxX {
				col := columnBase[bitI]
				if col.Mark == markPeel {
					src := c.compressRow(int(col.PeelRow))
					for j := 0; j < c.gePitch; j++ {
						tempRow[j] ^= src[j]
					}
				} else {
					geColumnI := int(col.GEColumn)
					tempRow[geColumnI>>6] ^= uint64(1) << (geColumnI & 63)
				}
			}
		}
		rowIndex := 0
		geDest := c.geRow(int(rows[rowIndex]))
		for j := 0; j < c.gePitch; j++ {
			geDest[j] ^= tempRow[j]
		}
		rowIndex++
		shuffleDeck16(&prng, bits, uint32(c.denseCount))
		loopCount := int(c.denseCount >> 1)
		setBits = bits[:setCount]
		clrBits = bits[setCount:]
		for ii := 0; ii < loopCount; ii++ {
			for _, bit := range []int{int(setBits[ii]), int(clrBits[ii])} {
				if bit < maxX {
					col := columnBase[bit]
					if col.Mark == markPeel {
						src := c.compressRow(int(col.PeelRow))
						for j := 0; j < c.gePitch; j++ {
							tempRow[j] ^= src[j]
						}
					} else {
						geColumnI := int(col.GEColumn)
						tempRow[geColumnI>>6] ^= uint64(1) << (geColumnI & 63)
					}
				}
			}
			geDest = c.geRow(int(rows[rowIndex]))
			for j := 0; j < c.gePitch; j++ {
				geDest[j] ^= tempRow[j]
			}
			rowIndex++
		}
		shuffleDeck16(&prng, bits, uint32(c.denseCount))
		setBits = bits[:setCount]
		clrBits = bits[setCount:]
		secondLoopCount := loopCount - 1 + int(c.denseCount&1)
		for ii := 0; ii < secondLoopCount; ii++ {
			for _, bit := range []int{int(setBits[ii]), int(clrBits[ii])} {
				if bit < maxX {
					col := columnBase[bit]
					if col.Mark == markPeel {
						src := c.compressRow(int(col.PeelRow))
						for j := 0; j < c.gePitch; j++ {
							tempRow[j] ^= src[j]
						}
					} else {
						geColumnI := int(col.GEColumn)
						tempRow[geColumnI>>6] ^= uint64(1) << (geColumnI & 63)
					}
				}
			}
			geDest = c.geRow(int(rows[rowIndex]))
			for j := 0; j < c.gePitch; j++ {
				geDest[j] ^= tempRow[j]
			}
			rowIndex++
		}
	}
}

func (c *Codec) SetHeavyRows() {
	heavyOffset := int(c.extraCount) * c.heavyPitch
	for rowI := 0; rowI < heavyRows; rowI++ {
		heavyRow := c.heavyMatrix[heavyOffset+rowI*c.heavyPitch:]
		for colI := 0; colI < c.heavyColumns; colI++ {
			heavyRow[colI] = heavyMatrix[rowI][colI]
		}
	}
}
