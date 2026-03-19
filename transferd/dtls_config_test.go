package main

import (
	"testing"

	"github.com/pion/dtls/v3"
)

func TestNewClientDTLSConfig(t *testing.T) {
	t.Parallel()

	config := newClientDTLSConfig()
	if config.PSK == nil {
		t.Fatal("expected client PSK callback to be configured")
	}
	if config.PSKIdentityHint == nil {
		t.Fatal("expected client PSK identity hint to be configured")
	}
	if got, want := config.CipherSuites, []dtls.CipherSuiteID{dtls.TLS_ECDHE_PSK_WITH_AES_128_CBC_SHA256}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("unexpected client cipher suites: %v", got)
	}
}

func TestNewServerDTLSConfigSkipsHelloVerify(t *testing.T) {
	t.Parallel()

	config := newServerDTLSConfig()
	if !config.InsecureSkipVerifyHello {
		t.Fatal("expected server hello verify to be disabled")
	}
	if config.PSK == nil {
		t.Fatal("expected server PSK callback to be configured")
	}
}
