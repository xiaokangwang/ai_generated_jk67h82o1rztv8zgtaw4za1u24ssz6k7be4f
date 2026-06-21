//go:build !wirehairsimd || !(amd64 || arm64)

package wirehair

const simdEnabled = false

func xorBytesInplace(dst, src []byte) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}

func xorBytesAcc3(dst, x, y []byte) {
	for i := range dst {
		dst[i] ^= x[i] ^ y[i]
	}
}

func xorBytesSet(dst, x, y []byte) {
	for i := range dst {
		dst[i] = x[i] ^ y[i]
	}
}
