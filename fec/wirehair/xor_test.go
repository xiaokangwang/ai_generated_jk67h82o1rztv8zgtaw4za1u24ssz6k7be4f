package wirehair

import "testing"

func fillPattern(buf []byte, seed byte) {
	for i := range buf {
		buf[i] = byte((int(seed) + i*29 + i/3) ^ (i << 1))
	}
}

func TestXORKernels(t *testing.T) {
	for _, offset := range []int{0, 1, 3, 7, 15} {
		for n := 0; n <= 129; n++ {
			size := offset + n + 16

			dstBuf := make([]byte, size)
			srcBuf := make([]byte, size)
			yBuf := make([]byte, size)
			fillPattern(dstBuf, 0x11)
			fillPattern(srcBuf, 0x5a)
			fillPattern(yBuf, 0xc3)

			dst := dstBuf[offset : offset+n]
			src := srcBuf[offset : offset+n]
			y := yBuf[offset : offset+n]

			wantInplace := append([]byte(nil), dst...)
			for i := range wantInplace {
				wantInplace[i] ^= src[i]
			}
			gotInplace := append([]byte(nil), dst...)
			xorBytesInplace(gotInplace, src)
			if string(gotInplace) != string(wantInplace) {
				t.Fatalf("xorBytesInplace offset=%d n=%d", offset, n)
			}

			wantAcc3 := append([]byte(nil), dst...)
			for i := range wantAcc3 {
				wantAcc3[i] ^= src[i] ^ y[i]
			}
			gotAcc3 := append([]byte(nil), dst...)
			xorBytesAcc3(gotAcc3, src, y)
			if string(gotAcc3) != string(wantAcc3) {
				t.Fatalf("xorBytesAcc3 offset=%d n=%d", offset, n)
			}

			wantSet := make([]byte, n)
			for i := range wantSet {
				wantSet[i] = src[i] ^ y[i]
			}
			gotSet := make([]byte, n)
			xorBytesSet(gotSet, src, y)
			if string(gotSet) != string(wantSet) {
				t.Fatalf("xorBytesSet offset=%d n=%d", offset, n)
			}
		}
	}
}
