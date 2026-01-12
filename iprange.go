package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ParseIPRange parses an IP range string and returns a list of IPs
// Supports:
//   - Single IP: "8.8.8.8"
//   - CIDR notation: "192.168.1.0/24"
//   - IP range: "10.0.0.1-10.0.0.10"
func ParseIPRange(rangeStr string) ([]net.IP, error) {
	// Check for CIDR notation
	if strings.Contains(rangeStr, "/") {
		return parseCIDR(rangeStr)
	}

	// Check for IP range
	if strings.Contains(rangeStr, "-") {
		return parseRange(rangeStr)
	}

	// Single IP
	ip := net.ParseIP(rangeStr)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP address: %s", rangeStr)
	}

	ip = ip.To4()
	if ip == nil {
		return nil, fmt.Errorf("not an IPv4 address: %s", rangeStr)
	}

	return []net.IP{ip}, nil
}

// parseCIDR parses CIDR notation (e.g., "192.168.1.0/24")
func parseCIDR(cidr string) ([]net.IP, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR notation: %v", err)
	}

	// Ensure IPv4
	ip = ip.To4()
	if ip == nil {
		return nil, fmt.Errorf("not an IPv4 CIDR: %s", cidr)
	}

	var ips []net.IP
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); incrementIP(ip) {
		// Make a copy of the IP
		ipCopy := make(net.IP, len(ip))
		copy(ipCopy, ip)
		ips = append(ips, ipCopy)
	}

	return ips, nil
}

// parseRange parses IP range notation (e.g., "10.0.0.1-10.0.0.10")
func parseRange(rangeStr string) ([]net.IP, error) {
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid IP range format: %s", rangeStr)
	}

	startIP := net.ParseIP(strings.TrimSpace(parts[0]))
	endIP := net.ParseIP(strings.TrimSpace(parts[1]))

	if startIP == nil || endIP == nil {
		return nil, fmt.Errorf("invalid IP in range: %s", rangeStr)
	}

	startIP = startIP.To4()
	endIP = endIP.To4()

	if startIP == nil || endIP == nil {
		return nil, fmt.Errorf("not IPv4 addresses in range: %s", rangeStr)
	}

	// Convert to uint32 for comparison
	start := ipToUint32(startIP)
	end := ipToUint32(endIP)

	if start > end {
		return nil, fmt.Errorf("start IP is greater than end IP: %s", rangeStr)
	}

	// Calculate number of IPs
	count := end - start + 1
	if count > 1000000 {
		return nil, fmt.Errorf("IP range too large (%d addresses), maximum 1,000,000", count)
	}

	var ips []net.IP
	current := make(net.IP, len(startIP))
	copy(current, startIP)

	for ipToUint32(current) <= end {
		ipCopy := make(net.IP, len(current))
		copy(ipCopy, current)
		ips = append(ips, ipCopy)
		incrementIP(current)
	}

	return ips, nil
}

// incrementIP increments an IP address by 1
func incrementIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] > 0 {
			break
		}
	}
}

// ipToUint32 converts an IPv4 address to uint32
func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	if ip == nil {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

// uint32ToIP converts uint32 to an IPv4 address
func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	ip[0] = byte(n >> 24)
	ip[1] = byte(n >> 16)
	ip[2] = byte(n >> 8)
	ip[3] = byte(n)
	return ip
}

// FormatIPCount formats the IP count in a human-readable way
func FormatIPCount(count int) string {
	if count < 1000 {
		return strconv.Itoa(count)
	} else if count < 1000000 {
		return fmt.Sprintf("%.1fK", float64(count)/1000)
	} else if count < 1000000000 {
		return fmt.Sprintf("%.1fM", float64(count)/1000000)
	}
	return fmt.Sprintf("%.1fB", float64(count)/1000000000)
}
