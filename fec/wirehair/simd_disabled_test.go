//go:build !wirehairsimd || !(amd64 || arm64)

package wirehair

import "testing"

func TestSIMDBuildTagDisabled(t *testing.T) {
	if simdEnabled {
		t.Fatal("expected generic path to be enabled")
	}
}
