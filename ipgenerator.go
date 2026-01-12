package main

import (
	"encoding/binary"
	"fmt"
	"net"
)

// IPRangeIterator represents an IP range that can be iterated
type IPRangeIterator struct {
	Start uint32
	End   uint32
	Total uint64
}

// NewIPRangeIterator creates an iterator from start and end IPs
func NewIPRangeIterator(start, end net.IP) (*IPRangeIterator, error) {
	start4 := start.To4()
	end4 := end.To4()

	if start4 == nil || end4 == nil {
		return nil, fmt.Errorf("only IPv4 addresses supported")
	}

	startInt := binary.BigEndian.Uint32(start4)
	endInt := binary.BigEndian.Uint32(end4)

	if startInt > endInt {
		return nil, fmt.Errorf("start IP must be <= end IP")
	}

	total := uint64(endInt) - uint64(startInt) + 1

	return &IPRangeIterator{
		Start: startInt,
		End:   endInt,
		Total: total,
	}, nil
}

// NewIPRangeIteratorFromString parses IP range string and creates iterator
func NewIPRangeIteratorFromString(rangeStr string) (*IPRangeIterator, error) {
	ips, err := ParseIPRange(rangeStr)
	if err != nil {
		return nil, err
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("empty IP range")
	}

	// Get min and max from the slice
	var minIP, maxIP uint32 = ^uint32(0), 0

	for _, ip := range ips {
		ip4 := ip.To4()
		ipInt := binary.BigEndian.Uint32(ip4)
		if ipInt < minIP {
			minIP = ipInt
		}
		if ipInt > maxIP {
			maxIP = ipInt
		}
	}

	return &IPRangeIterator{
		Start: minIP,
		End:   maxIP,
		Total: uint64(maxIP) - uint64(minIP) + 1,
	}, nil
}

// Generate creates a channel that produces IPs on demand
func (r *IPRangeIterator) Generate(excludeRanges []CompletedRange) <-chan net.IP {
	ch := make(chan net.IP, 100) // Buffer for efficiency

	go func() {
		defer close(ch)

		for i := r.Start; i <= r.End; i++ {
			// Check if this IP is in any excluded range
			excluded := false
			for _, excl := range excludeRanges {
				if i >= excl.Start && i <= excl.End {
					// Skip to end of excluded range
					i = excl.End
					excluded = true
					break
				}
			}

			if excluded {
				continue
			}

			// Convert uint32 to IP and send
			ip := make(net.IP, 4)
			binary.BigEndian.PutUint32(ip, i)
			ch <- ip
		}
	}()

	return ch
}

// GenerateShuffled creates a channel that produces IPs in shuffled order using Blackrock cipher
// This provides pseudo-random order without loading IPs into memory
// seed determines the permutation (same seed = same order)
func (r *IPRangeIterator) GenerateShuffled(excludeRanges []CompletedRange, seed uint64) <-chan net.IP {
	ch := make(chan net.IP, 100)

	go func() {
		defer close(ch)

		// Create blackrock cipher for this range
		br, err := NewBlackrock(r.Total, seed)
		if err != nil {
			// Fallback to sequential if blackrock fails
			for i := r.Start; i <= r.End; i++ {
				if !isInExcludedRange(i, excludeRanges) {
					ch <- Uint32ToIP(i)
				}
			}
			return
		}

		// Iterate through all indices in order
		generated := uint64(0)
		for index := uint64(0); index < r.Total; index++ {
			// Apply blackrock permutation to get shuffled index
			shuffledIndex := br.Shuffle(index)

			// Convert shuffled index back to IP address
			ipInt := r.Start + uint32(shuffledIndex)

			// Check if excluded
			if !isInExcludedRange(ipInt, excludeRanges) {
				ch <- Uint32ToIP(ipInt)
				generated++
			}
		}
	}()

	return ch
}

// isInExcludedRange checks if an IP is within any excluded range
func isInExcludedRange(ipInt uint32, excludeRanges []CompletedRange) bool {
	for _, excl := range excludeRanges {
		if ipInt >= excl.Start && ipInt <= excl.End {
			return true
		}
	}
	return false
}

// Count returns total number of IPs in range
func (r *IPRangeIterator) Count() uint64 {
	return r.Total
}

// IPToUint32 converts IP to uint32
func IPToUint32(ip net.IP) uint32 {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip4)
}

// Uint32ToIP converts uint32 to IP
func Uint32ToIP(val uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, val)
	return ip
}
