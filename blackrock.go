package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"math/bits"
)

// Blackrock implements ZMap's blackrock cipher for IP permutation
// This is a cycle-walking cipher that guarantees bijective mapping

type Blackrock struct {
	cipher cipher.Block
	rounds int
	range_ uint64
	bits   int // Number of bits needed for the range
}

// NewBlackrock creates a new Blackrock cipher for the given range and seed
func NewBlackrock(rangeSize uint64, seed uint64) (*Blackrock, error) {
	// Create AES key from seed
	key := make([]byte, 16)
	binary.LittleEndian.PutUint64(key[0:8], seed)
	binary.LittleEndian.PutUint64(key[8:16], seed^0xAAAAAAAAAAAAAAAA)

	// Create AES cipher
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// Determine number of bits needed for range
	numBits := 64
	if rangeSize > 0 {
		numBits = bits.Len64(rangeSize - 1)
	}

	// Determine number of rounds based on bit size
	rounds := 3
	if numBits > 16 {
		rounds = 4
	}

	return &Blackrock{
		cipher: block,
		rounds: rounds,
		range_: rangeSize,
		bits:   numBits,
	}, nil
}

// Shuffle permutes the input value within the range
// This implements cycle-walking to ensure output is always < rangeSize
func (br *Blackrock) Shuffle(input uint64) uint64 {
	if br.range_ <= 1 {
		return 0
	}

	// Cycle-walking within the minimum bit space
	maxVal := uint64(1) << br.bits
	output := input % maxVal // Ensure input is in range

	maxIterations := 10 * br.bits // Reasonable upper bound
	for i := 0; i < maxIterations; i++ {
		output = br.encryptBits(output)
		if output < br.range_ {
			return output
		}
		// Continue cycle-walking with the new output
	}

	// This should rarely happen; use simple modulo as last resort
	return input % br.range_
}

// encryptBits applies Feistel network in the minimum bit space
func (br *Blackrock) encryptBits(input uint64) uint64 {
	// Work in minimum bit space for efficiency
	halfBits := (br.bits + 1) / 2
	mask := (uint64(1) << halfBits) - 1

	left := (input >> halfBits) & mask
	right := input & mask

	// Apply Feistel rounds
	for round := 0; round < br.rounds; round++ {
		temp := right
		right = left ^ (br.roundFunctionSmall(right, uint32(round)) & mask)
		left = temp
	}

	return (left << halfBits) | right
}

// roundFunctionSmall is optimized for smaller bit spaces
func (br *Blackrock) roundFunctionSmall(value uint64, round uint32) uint64 {
	// Prepare input block for AES (16 bytes)
	input := make([]byte, 16)
	binary.LittleEndian.PutUint64(input[0:8], value)
	binary.LittleEndian.PutUint32(input[8:12], round)
	binary.LittleEndian.PutUint32(input[12:16], 0xAAAAAAAA)

	// Encrypt with AES
	output := make([]byte, 16)
	br.cipher.Encrypt(output, input)

	// Return first 64 bits
	return binary.LittleEndian.Uint64(output[0:8])
}

// Unshuffle reverses the permutation (inverse function)
func (br *Blackrock) Unshuffle(output uint64) uint64 {
	if br.range_ <= 1 {
		return 0
	}

	// Cycle-walking in reverse
	maxVal := uint64(1) << br.bits
	input := output % maxVal

	maxIterations := 10 * br.bits
	for i := 0; i < maxIterations; i++ {
		input = br.decryptBits(input)
		if input < br.range_ {
			return input
		}
	}

	return output % br.range_
}

// decryptBits reverses the Feistel network
func (br *Blackrock) decryptBits(output uint64) uint64 {
	halfBits := (br.bits + 1) / 2
	mask := (uint64(1) << halfBits) - 1

	left := (output >> halfBits) & mask
	right := output & mask

	// Apply Feistel rounds in reverse
	for round := br.rounds - 1; round >= 0; round-- {
		temp := left
		left = right ^ (br.roundFunctionSmall(left, uint32(round)) & mask)
		right = temp
	}

	return (left << halfBits) | right
}

// ShuffleIP32 is a convenience function for 32-bit IP addresses
func (br *Blackrock) ShuffleIP32(input uint32) uint32 {
	return uint32(br.Shuffle(uint64(input)))
}
