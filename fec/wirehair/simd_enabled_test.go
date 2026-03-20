//go:build wirehairsimd && (amd64 || arm64)

package wirehair

import "testing"

func TestSIMDBuildTagEnabled(t *testing.T) {
	if !simdEnabled {
		t.Fatal("expected SIMD path to be enabled")
	}
}
