package main

import (
	"crypto/rand"
	"encoding/binary"
	"math/big"
	"net"
)

// ShuffleIPs randomizes the order of IP addresses using crypto/rand for security
func ShuffleIPs(ips []net.IP) {
	n := len(ips)
	for i := n - 1; i > 0; i-- {
		// Use crypto/rand for cryptographically secure randomness
		j := cryptoRandInt(i + 1)
		ips[i], ips[j] = ips[j], ips[i]
	}
}

// cryptoRandInt returns a cryptographically secure random integer in [0, n)
func cryptoRandInt(n int) int {
	if n <= 0 {
		return 0
	}

	// For small n, use crypto/rand directly
	max := big.NewInt(int64(n))
	result, err := rand.Int(rand.Reader, max)
	if err != nil {
		// Fallback to a less ideal but still working approach
		var b [8]byte
		rand.Read(b[:])
		return int(binary.BigEndian.Uint64(b[:]) % uint64(n))
	}

	return int(result.Int64())
}
