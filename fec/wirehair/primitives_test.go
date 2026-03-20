//go:build wirehairnativeoracle
// +build wirehairnativeoracle

package wirehair

import (
	"testing"

	"codextest2/internal/nativeoracle"
)

func TestInitAndResultStringCompatibility(t *testing.T) {
	if err := Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	code, err := nativeoracle.NativeInit(Version)
	if err != nil {
		t.Fatalf("native init: %v", err)
	}
	if ResultCode(code) != ResultSuccess {
		t.Fatalf("native init returned %v", code)
	}

	code, err = nativeoracle.NativeInit(Version + 1)
	if err != nil {
		t.Fatalf("native version mismatch: %v", err)
	}
	if ResultCode(code) != ResultInvalidInput {
		t.Fatalf("native invalid version returned %v", code)
	}

	for code := ResultSuccess; code <= ResultUnsupportedPlatform; code++ {
		got := ResultString(code)
		want, err := nativeoracle.NativeResultString(int32(code))
		if err != nil {
			t.Fatalf("native result string %d: %v", code, err)
		}
		if got != want {
			t.Fatalf("result string %d mismatch: got %q want %q", code, got, want)
		}
	}
}

func TestToolsMatchNative(t *testing.T) {
	cases := []uint32{2, 3, 4, 5, 31, 32, 33, 64, 65, 128, 500, 999, 1000, 2048, 2049, 4096, 8192, 16384, 32768, 64000}
	for _, n := range cases {
		gotDenseCount := getDenseCount(unsigned(n))
		gotDenseSeed := getDenseSeed(unsigned(n), unsigned(gotDenseCount))
		gotPeelSeed := getPeelSeed(unsigned(n))

		wantDenseCount, wantDenseSeed, wantPeelSeed, err := nativeoracle.NativeTools(n)
		if err != nil {
			t.Fatalf("native tools %d: %v", n, err)
		}
		if gotDenseCount != wantDenseCount || gotDenseSeed != wantDenseSeed || gotPeelSeed != wantPeelSeed {
			t.Fatalf("tools mismatch for N=%d: got (%d,%d,%d) want (%d,%d,%d)",
				n, gotDenseCount, gotDenseSeed, gotPeelSeed, wantDenseCount, wantDenseSeed, wantPeelSeed)
		}
	}
}

func TestRowParametersMatchNative(t *testing.T) {
	cases := []struct {
		rowSeed     uint32
		pSeed       uint32
		peelColumns uint16
		mixColumns  uint16
	}{
		{0, 1234, 8, 32},
		{17, 4321, 64, 40},
		{123, 999, 511, 74},
		{1024, 7, 2048, 86},
		{4095, 65535, 777, 92},
	}

	for _, tc := range cases {
		var params PeelRowParameters
		params.Initialize(tc.rowSeed, tc.pSeed, tc.peelColumns, tc.mixColumns)

		want, err := nativeoracle.NativeRowParams(tc.rowSeed, tc.pSeed, tc.peelColumns, tc.mixColumns)
		if err != nil {
			t.Fatalf("native row params %+v: %v", tc, err)
		}
		if params.PeelCount != want.PeelCount ||
			params.PeelFirst != want.PeelFirst ||
			params.PeelAdd != want.PeelAdd ||
			params.MixFirst != want.MixFirst ||
			params.MixAdd != want.MixAdd {
			t.Fatalf("row params mismatch %+v: got %+v want %+v", tc, params, want)
		}
	}
}
