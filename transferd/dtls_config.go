package main

import "github.com/pion/dtls/v3"

func newClientDTLSConfig() *dtls.Config {
	config := newBaseDTLSConfig()
	config.PSKIdentityHint = []byte{}
	return config
}

func newServerDTLSConfig() *dtls.Config {
	config := newBaseDTLSConfig()
	// VLite already tracks clients by remote address. Skipping HelloVerify keeps
	// the handshake on a single UDP flow and avoids the rejected cookie round-trip.
	config.InsecureSkipVerifyHello = true
	return config
}

func newBaseDTLSConfig() *dtls.Config {
	return &dtls.Config{
		MTU:                    1450,
		ReplayProtectionWindow: 1024,
		PSK: func(bytes []byte) ([]byte, error) {
			return []byte(mustGetConfFromEnv("PSKey")), nil
		},
		CipherSuites: []dtls.CipherSuiteID{
			dtls.TLS_ECDHE_PSK_WITH_AES_128_CBC_SHA256,
		},
	}
}
