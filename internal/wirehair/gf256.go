package wirehair

import "sync"

type gf256Context struct {
	mulTable [256 * 256]uint8
	divTable [256 * 256]uint8
	invTable [256]uint8
	sqrTable [256]uint8
	logTable [256]uint16
	expTable [512*2 + 1]uint8
	poly     uint16
}

var (
	gfCtx        gf256Context
	gfInitOnce   sync.Once
	gfInitResult ResultCode = ResultSuccess
)

const (
	gfVersion             = 2
	gf256GenPolyCount     = 16
	gf256DefaultPolyIndex = 3
)

var gf256GenPoly = [gf256GenPolyCount]uint8{
	0x8e, 0x95, 0x96, 0xa6, 0xaf, 0xb1, 0xb2, 0xb4,
	0xb8, 0xc3, 0xc6, 0xd4, 0xe1, 0xe7, 0xf3, 0xfa,
}

func Init() error {
	if code := initVersion(Version); code != ResultSuccess {
		return &Error{Code: code}
	}
	return nil
}

func initVersion(expected int) ResultCode {
	if expected != Version {
		return ResultInvalidInput
	}
	gfInitOnce.Do(func() {
		gfInitResult = gf256Init(gfVersion)
	})
	return gfInitResult
}

func gf256Init(version int) ResultCode {
	if version != gfVersion {
		return ResultInvalidInput
	}
	gf256PolyInit(gf256DefaultPolyIndex)
	gf256ExpLogInit()
	gf256MulDivInit()
	gf256InvInit()
	gf256SqrInit()
	if !gf256SelfTest() {
		return ResultUnsupportedPlatform
	}
	return ResultSuccess
}

func gf256PolyInit(polynomialIndex int) {
	if polynomialIndex < 0 || polynomialIndex >= gf256GenPolyCount {
		polynomialIndex = gf256DefaultPolyIndex
	}
	gfCtx.poly = uint16(gf256GenPoly[polynomialIndex])<<1 | 1
}

func gf256ExpLogInit() {
	poly := uint32(gfCtx.poly)
	gfCtx.logTable[0] = 512
	gfCtx.expTable[0] = 1
	for j := 1; j < 255; j++ {
		next := uint32(gfCtx.expTable[j-1]) * 2
		if next >= 256 {
			next ^= poly
		}
		gfCtx.expTable[j] = uint8(next)
		gfCtx.logTable[gfCtx.expTable[j]] = uint16(j)
	}
	gfCtx.expTable[255] = gfCtx.expTable[0]
	gfCtx.logTable[gfCtx.expTable[255]] = 255
	for j := 256; j < 2*255; j++ {
		gfCtx.expTable[j] = gfCtx.expTable[j%255]
	}
	gfCtx.expTable[2*255] = 1
	for j := 2*255 + 1; j < 4*255; j++ {
		gfCtx.expTable[j] = 0
	}
}

func gf256MulDivInit() {
	m := gfCtx.mulTable[:]
	d := gfCtx.divTable[:]
	for x := 0; x < 256; x++ {
		m[x] = 0
		d[x] = 0
	}
	offset := 0
	for y := 1; y < 256; y++ {
		logY := uint8(gfCtx.logTable[y])
		logYN := uint8(255 - logY)
		offset += 256
		m[offset] = 0
		d[offset] = 0
		for x := 1; x < 256; x++ {
			logX := gfCtx.logTable[x]
			m[offset+x] = gfCtx.expTable[int(logX)+int(logY)]
			d[offset+x] = gfCtx.expTable[int(logX)+int(logYN)]
		}
	}
}

func gf256InvInit() {
	for x := 0; x < 256; x++ {
		gfCtx.invTable[x] = gf256Div(1, uint8(x))
	}
}

func gf256SqrInit() {
	for x := 0; x < 256; x++ {
		gfCtx.sqrTable[x] = gf256Mul(uint8(x), uint8(x))
	}
}

func gf256Add(x, y uint8) uint8 {
	return x ^ y
}

func gf256Mul(x, y uint8) uint8 {
	return gfCtx.mulTable[int(y)<<8|int(x)]
}

func gf256Div(x, y uint8) uint8 {
	return gfCtx.divTable[int(y)<<8|int(x)]
}

func gf256Inv(x uint8) uint8 {
	return gfCtx.invTable[x]
}

func gf256Sqr(x uint8) uint8 {
	return gfCtx.sqrTable[x]
}

func gf256AddMem(x, y []byte) {
	xorBytesInplace(x, y)
}

func gf256Add2Mem(z, x, y []byte) {
	xorBytesAcc3(z, x, y)
}

func gf256AddSetMem(z, x, y []byte) {
	xorBytesSet(z, x, y)
}

func gf256MulMem(z, x []byte, y uint8) {
	table := gfCtx.mulTable[int(y)<<8:]
	for i := range z {
		z[i] = table[x[i]]
	}
}

func gf256MulAddMem(z []byte, y uint8, x []byte) {
	table := gfCtx.mulTable[int(y)<<8:]
	for i := range z {
		z[i] ^= table[x[i]]
	}
}

func gf256DivMem(z, x []byte, y uint8) {
	table := gfCtx.divTable[int(y)<<8:]
	for i := range z {
		z[i] = table[x[i]]
	}
}

func gf256MemSwap(x, y []byte) {
	for i := range x {
		x[i], y[i] = y[i], x[i]
	}
}

func gf256SelfTest() bool {
	for i := 0; i < 256; i++ {
		for j := 0; j < 256; j++ {
			prod := gf256Mul(uint8(i), uint8(j))
			if i != 0 && j != 0 {
				if gf256Div(prod, uint8(i)) != uint8(j) {
					return false
				}
				if gf256Div(prod, uint8(j)) != uint8(i) {
					return false
				}
			} else if prod != 0 {
				return false
			}
			if j == 1 && prod != uint8(i) {
				return false
			}
		}
	}

	a := make([]byte, 63)
	b := make([]byte, 63)
	c := make([]byte, 63)
	for i := range a {
		a[i] = 0x1f
		b[i] = 0xf7
	}
	gf256AddMem(a, b)
	for i := range a {
		if a[i] != (0x1f ^ 0xf7) {
			return false
		}
	}

	for i := range a {
		a[i] = 0x1f
		b[i] = 0xf7
		c[i] = 0x71
	}
	gf256Add2Mem(a, b, c)
	for i := range a {
		if a[i] != (0x1f ^ 0xf7 ^ 0x71) {
			return false
		}
	}

	for i := range a {
		a[i] = 0x55
		b[i] = 0xaa
		c[i] = 0x6c
	}
	gf256AddSetMem(a, b, c)
	for i := range a {
		if a[i] != (0xaa ^ 0x6c) {
			return false
		}
	}

	for i := range a {
		a[i] = 0xff
		b[i] = 0xaa
	}
	expectedMulAdd := gf256Mul(0xaa, 0x6c)
	gf256MulAddMem(a, 0x6c, b)
	for i := range a {
		if a[i] != (expectedMulAdd ^ 0xff) {
			return false
		}
	}

	for i := range a {
		a[i] = 0xff
		b[i] = 0x55
	}
	expectedMul := gf256Mul(0xa2, 0x55)
	gf256MulMem(a, b, 0xa2)
	for i := range a {
		if a[i] != expectedMul {
			return false
		}
	}
	return true
}
