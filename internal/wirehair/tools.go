package wirehair

import (
	"math/bits"
)

const (
	kTinyTableCount       = 65
	kSmallTableCount      = 2048 - kTinyTableCount
	kDenseSeedCount       = 100
	kPeelSeedSubdivisions = 2048
	kMaxDenseCount        = 400
	kMaxNForWeight1       = 2048
	kMaxPeelCount         = 64
	p1                    = ^uint32(0) / 128
)

type PCGRandom struct {
	State uint64
	Inc   uint64
}

func (p *PCGRandom) Seed(y uint64, x uint64) {
	p.State = 0
	p.Inc = (y << 1) | 1
	p.Next()
	p.State += x
	p.Next()
}

func (p *PCGRandom) Next() uint32 {
	old := p.State
	p.State = old*6364136223846793005 + p.Inc
	xorshifted := uint32(((old >> 18) ^ old) >> 27)
	rot := uint32(old >> 59)
	return bits.RotateLeft32(xorshifted, -int(rot))
}

func floorSquareRoot16(x uint16) uint16 {
	var nonzeroBits uint
	if x < 0x100 {
		nonzeroBits = 6
	} else {
		nonzeroBits = uint(31 - bits.LeadingZeros32(uint32(x)))
	}
	tableShift := (nonzeroBits - 6) & 0xfe
	resultShift := (15 - nonzeroBits) / 2
	r := uint16(kSquareRootTable[uint32(x)>>tableShift]) >> resultShift
	if r*r > x {
		r--
	}
	return r
}

func nextPrime16(n uint16) uint16 {
	switch n {
	case 0:
		return 1
	case 1:
		return 1
	case 2:
		return 2
	case 3:
		return 3
	case 4, 5:
		return 5
	case 6, 7:
		return 7
	}
	if n > 65521 {
		return 0
	}
	offset := int(n % uint16(len(kSieveTable)))
	next := uint16(kSieveTable[offset])
	offset += int(next) + 1
	n += next
	pMax := uint16(floorSquareRoot16(n))
	pMax2 := pMax * pMax
tryNext:
	for _, p := range kPrimesUnder256From11 {
		if uint16(p) > pMax {
			return n
		}
		if n%uint16(p) == 0 {
			if offset >= len(kSieveTable) {
				offset -= len(kSieveTable)
			}
			next = uint16(kSieveTable[offset])
			offset += int(next) + 1
			n += next + 1
			if pMax2 < n {
				pMax++
				pMax2 = pMax * pMax
			}
			goto tryNext
		}
	}
	return n
}

func random64(prng *PCGRandom) uint64 {
	rv1 := prng.Next()
	rv2 := prng.Next()
	return uint64(rv2)<<32 | uint64(rv1)
}

func addInvertibleGF2Matrix(matrix []uint64, offset, pitchWords, n uint) {
	if n >= uint(len(kInvertibleMatrixSeeds)) {
		for ii := uint(0); ii < n; ii++ {
			row := matrix[ii*pitchWords:]
			columnI := offset + ii
			row[columnI>>6] ^= uint64(1) << (columnI & 63)
			if ii+1 < n {
				columnJ := columnI + 1
				row[columnJ>>6] ^= uint64(1) << (columnJ & 63)
			}
		}
		return
	}

	seed := kInvertibleMatrixSeeds[n]
	var prng PCGRandom
	prng.Seed(uint64(seed), 0)
	shift := offset & 63
	wordOffset := offset >> 6

	for rowI := uint(0); rowI < n; rowI++ {
		destBase := rowI*pitchWords + wordOffset
		target := n
		prev := uint64(0)
		word0 := random64(&prng)
		temp0 := word0
		written0 := uint(64 - shift)
		if target < 64 {
			temp0 &= (uint64(1) << target) - 1
			if written0 > target {
				written0 = target
			}
		}
		matrix[destBase] ^= temp0 << shift
		target -= written0
		prev = word0
		if target == 0 {
			continue
		}
		destIndex := destBase + 1
		for {
			temp := uint64(0)
			if shift != 0 {
				temp = prev >> (64 - shift)
			}
			word := uint64(0)
			if target > shift {
				word = random64(&prng)
				temp |= word << shift
			}
			if target < 64 {
				temp &= (uint64(1) << target) - 1
			}
			matrix[destIndex] ^= temp
			if target <= 64 {
				break
			}
			target -= 64
			destIndex++
			prev = word
		}
	}
}

func shuffleDeck16(prng *PCGRandom, deck []uint16, count uint32) {
	if count == 0 {
		return
	}
	deck[0] = 0
	if count <= 256 {
		for ii := uint32(1); ; {
			rv := prng.Next()
			switch count - ii {
			default:
				jj := uint32(uint8(rv)) % ii
				deck[ii] = deck[jj]
				deck[jj] = uint16(ii)
				ii++
				jj = uint32(uint8(rv>>8)) % ii
				deck[ii] = deck[jj]
				deck[jj] = uint16(ii)
				ii++
				jj = uint32(uint8(rv>>16)) % ii
				deck[ii] = deck[jj]
				deck[jj] = uint16(ii)
				ii++
				jj = uint32(uint8(rv>>24)) % ii
				deck[ii] = deck[jj]
				deck[jj] = uint16(ii)
				ii++
			case 3:
				jj := uint32(uint8(rv)) % ii
				deck[ii] = deck[jj]
				deck[jj] = uint16(ii)
				ii++
				fallthrough
			case 2:
				jj := uint32(uint8(rv>>8)) % ii
				deck[ii] = deck[jj]
				deck[jj] = uint16(ii)
				ii++
				fallthrough
			case 1:
				jj := uint32(uint8(rv>>16)) % ii
				deck[ii] = deck[jj]
				deck[jj] = uint16(ii)
				fallthrough
			case 0:
				return
			}
		}
	}

	for ii := uint32(1); ; {
		rv := prng.Next()
		switch count - ii {
		default:
			jj := uint32(uint16(rv)) % ii
			deck[ii] = deck[jj]
			deck[jj] = uint16(ii)
			ii++
			jj = uint32(uint16(rv>>16)) % ii
			deck[ii] = deck[jj]
			deck[jj] = uint16(ii)
			ii++
		case 1:
			jj := uint32(uint16(rv)) % ii
			deck[ii] = deck[jj]
			deck[jj] = uint16(ii)
			fallthrough
		case 0:
			return
		}
	}
}

func iterateNextColumn(x *uint16, b, p, a uint16) {
	*x = uint16((uint32(*x) + uint32(a)) % uint32(p))
	if *x >= b {
		distanceToP := p - *x
		if a >= distanceToP {
			*x = a - distanceToP
		} else {
			*x = uint16((((uint32(a) << 16) - uint32(distanceToP)) % uint32(a)))
		}
	}
}

func generatePeelRowWeight(rv uint32, blockCount uint16) uint16 {
	if blockCount <= kMaxNForWeight1 {
		if rv < p1 {
			return 1
		}
		rv -= p1
	}
	if rv <= kPeelCountDistribution[1] {
		return 2
	}
	if rv <= kPeelCountDistribution[2] {
		return 3
	}
	weight := uint16(3)
	for rv > kPeelCountDistribution[weight] {
		weight++
	}
	return weight + 1
}

type PeelRowParameters struct {
	PeelCount uint16
	PeelFirst uint16
	PeelAdd   uint16
	MixFirst  uint16
	MixAdd    uint16
}

func (p *PeelRowParameters) Initialize(rowSeed, pSeed uint32, peelColumnCount, mixColumnCount uint16) {
	var prng PCGRandom
	prng.Seed(uint64(rowSeed), uint64(pSeed))
	weight := generatePeelRowWeight(prng.Next(), peelColumnCount)
	maxWeight := peelColumnCount / 2
	if weight > maxWeight {
		p.PeelCount = maxWeight
	} else {
		p.PeelCount = weight
	}
	rvPeel := prng.Next()
	p.PeelAdd = (uint16(rvPeel) % (peelColumnCount - 1)) + 1
	p.PeelFirst = uint16(rvPeel>>16) % peelColumnCount
	rvMix := prng.Next()
	p.MixAdd = (uint16(rvMix) % (mixColumnCount - 1)) + 1
	p.MixFirst = uint16(rvMix>>16) % mixColumnCount
}

type PeelRowIterator struct {
	ColumnsRemaining     uint16
	NextColumn           uint16
	Adder                uint16
	ColumnCount          uint16
	ColumnCountNextPrime uint16
}

func NewPeelRowIterator(params PeelRowParameters, columnCount, columnCountNextPrime uint16) PeelRowIterator {
	return PeelRowIterator{
		ColumnsRemaining:     params.PeelCount - 1,
		NextColumn:           params.PeelFirst,
		Adder:                params.PeelAdd,
		ColumnCount:          columnCount,
		ColumnCountNextPrime: columnCountNextPrime,
	}
}

func (p *PeelRowIterator) GetColumn() uint16 {
	return p.NextColumn
}

func (p *PeelRowIterator) Iterate() bool {
	if p.ColumnsRemaining == 0 {
		return false
	}
	p.ColumnsRemaining--
	iterateNextColumn(&p.NextColumn, p.ColumnCount, p.ColumnCountNextPrime, p.Adder)
	return true
}

type RowMixIterator struct {
	Columns [3]uint16
}

func NewRowMixIterator(params PeelRowParameters, columnCount, columnCountNextPrime uint16) RowMixIterator {
	var out RowMixIterator
	x := params.MixFirst
	out.Columns[0] = x
	for i := 1; i < len(out.Columns); i++ {
		iterateNextColumn(&x, columnCount, columnCountNextPrime, params.MixAdd)
		out.Columns[i] = x
	}
	return out
}

func linearInterpolate(n0, n1, count0, count1, n int) uint16 {
	numerator := (n - n0) * (count1 - count0)
	denominator := n1 - n0
	count := count0 + numerator/denominator
	return uint16(count)
}

func getDenseCount(n unsigned) uint16 {
	var lowPoint, highPoint densePoint
	switch {
	case n < kTinyTableCount:
		return uint16(kTinyDenseCounts[n])
	case n < kTinyTableCount+kSmallTableCount:
		switch {
		case n <= 500:
			lowPoint = densePoint{N: 64, DenseCount: 26}
			highPoint = densePoint{N: 500, DenseCount: 35}
		case n <= 1000:
			lowPoint = densePoint{N: 500, DenseCount: 35}
			highPoint = densePoint{N: 1000, DenseCount: 48}
		default:
			lowPoint = densePoint{N: 1000, DenseCount: 48}
			highPoint = densePoint{N: 2048, DenseCount: 62}
		}
	default:
		low, high := 0, len(kDensePoints)-1
		for {
			mid := (high + low) / 2
			if mid == low {
				break
			}
			midPoint := kDensePoints[mid]
			if n > unsigned(midPoint.N) {
				low = mid
			} else {
				high = mid
			}
		}
		lowPoint = kDensePoints[low]
		highPoint = kDensePoints[low+1]
	}
	denseCount := linearInterpolate(int(lowPoint.N), int(highPoint.N), int(lowPoint.DenseCount), int(highPoint.DenseCount), int(n))
	switch denseCount % 4 {
	case 0:
		denseCount += 2
	case 1:
		denseCount += 1
	case 2:
	case 3:
		denseCount += 3
	}
	return denseCount
}

func getDenseSeed(n unsigned, denseCount unsigned) uint16 {
	switch {
	case n < kTinyTableCount:
		return uint16(kTinyDenseSeeds[n])
	case n < kTinyTableCount+kSmallTableCount:
		return uint16(kSmallDenseSeeds[n-kTinyTableCount])
	default:
		tableIndex := denseCount / 4
		return uint16(kDenseSeeds[tableIndex])
	}
}

func getPeelSeed(n unsigned) uint16 {
	if n < kTinyTableCount+kSmallTableCount {
		return uint16(kSmallPeelSeeds[n])
	}
	subdivision := n % kPeelSeedSubdivisions
	return uint16(kPeelSeeds[subdivision])
}

type unsigned = uint
