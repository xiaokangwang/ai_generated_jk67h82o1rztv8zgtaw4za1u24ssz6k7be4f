//go:build wirehairsimd && (amd64 || arm64)

package wirehair

import "unsafe"

const simdEnabled = true

func xorBytesInplace(dst, src []byte) {
	if len(dst) == 0 {
		return
	}
	xorBytesInplaceAsm(unsafe.SliceData(dst), unsafe.SliceData(src), uintptr(len(dst)))
}

func xorBytesAcc3(dst, x, y []byte) {
	if len(dst) == 0 {
		return
	}
	xorBytesAcc3Asm(
		unsafe.SliceData(dst),
		unsafe.SliceData(x),
		unsafe.SliceData(y),
		uintptr(len(dst)),
	)
}

func xorBytesSet(dst, x, y []byte) {
	if len(dst) == 0 {
		return
	}
	xorBytesSetAsm(
		unsafe.SliceData(dst),
		unsafe.SliceData(x),
		unsafe.SliceData(y),
		uintptr(len(dst)),
	)
}

//go:noescape
func xorBytesInplaceAsm(dst, src *byte, n uintptr)

//go:noescape
func xorBytesAcc3Asm(dst, x, y *byte, n uintptr)

//go:noescape
func xorBytesSetAsm(dst, x, y *byte, n uintptr)
