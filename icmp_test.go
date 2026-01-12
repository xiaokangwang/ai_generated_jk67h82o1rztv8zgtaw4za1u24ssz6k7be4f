package main

import (
	"net"
	"testing"
)

func TestCreateEchoRequest(t *testing.T) {
	packet := CreateEchoRequest(12345, 1)

	if packet.Header.Type != ICMPTypeEchoRequest {
		t.Errorf("Expected type %d, got %d", ICMPTypeEchoRequest, packet.Header.Type)
	}

	if packet.Header.ID != 12345 {
		t.Errorf("Expected ID 12345, got %d", packet.Header.ID)
	}

	if packet.Header.Sequence != 1 {
		t.Errorf("Expected sequence 1, got %d", packet.Header.Sequence)
	}
}

func TestICMPMarshalUnmarshal(t *testing.T) {
	original := CreateEchoRequest(54321, 42)

	data, err := original.MarshalBinary()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if len(data) < 8 {
		t.Errorf("Expected at least 8 bytes, got %d", len(data))
	}

	// Verify checksum
	checksum := calculateChecksum(data)
	if checksum != 0 {
		t.Errorf("Invalid checksum: 0x%04x (should be 0)", checksum)
	}

	// Unmarshal
	decoded := &ICMPPacket{}
	err = decoded.UnmarshalBinary(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Header.ID != original.Header.ID {
		t.Errorf("ID mismatch: expected %d, got %d", original.Header.ID, decoded.Header.ID)
	}

	if decoded.Header.Sequence != original.Header.Sequence {
		t.Errorf("Sequence mismatch: expected %d, got %d", original.Header.Sequence, decoded.Header.Sequence)
	}
}

func TestParseSingleIP(t *testing.T) {
	ips, err := ParseIPRange("8.8.8.8")
	if err != nil {
		t.Fatalf("Failed to parse single IP: %v", err)
	}

	if len(ips) != 1 {
		t.Errorf("Expected 1 IP, got %d", len(ips))
	}

	if ips[0].String() != "8.8.8.8" {
		t.Errorf("Expected 8.8.8.8, got %s", ips[0])
	}
}

func TestParseCIDR(t *testing.T) {
	tests := []struct {
		cidr     string
		expected int
	}{
		{"192.168.1.0/30", 4},
		{"10.0.0.0/29", 8},
		{"172.16.0.0/28", 16},
	}

	for _, tt := range tests {
		ips, err := ParseIPRange(tt.cidr)
		if err != nil {
			t.Errorf("Failed to parse CIDR %s: %v", tt.cidr, err)
			continue
		}

		if len(ips) != tt.expected {
			t.Errorf("CIDR %s: expected %d IPs, got %d", tt.cidr, tt.expected, len(ips))
		}
	}
}

func TestParseRange(t *testing.T) {
	ips, err := ParseIPRange("10.0.0.1-10.0.0.5")
	if err != nil {
		t.Fatalf("Failed to parse range: %v", err)
	}

	if len(ips) != 5 {
		t.Errorf("Expected 5 IPs, got %d", len(ips))
	}

	if ips[0].String() != "10.0.0.1" {
		t.Errorf("First IP should be 10.0.0.1, got %s", ips[0])
	}

	if ips[4].String() != "10.0.0.5" {
		t.Errorf("Last IP should be 10.0.0.5, got %s", ips[4])
	}
}

func TestIPConversion(t *testing.T) {
	testCases := []struct {
		ip       string
		expected uint32
	}{
		{"0.0.0.0", 0},
		{"1.0.0.0", 1 << 24},
		{"192.168.1.1", 3232235777},
		{"255.255.255.255", 4294967295},
	}

	for _, tc := range testCases {
		ip := net.ParseIP(tc.ip)
		result := ipToUint32(ip)

		if result != tc.expected {
			t.Errorf("IP %s: expected %d, got %d", tc.ip, tc.expected, result)
		}

		// Test reverse conversion
		backIP := uint32ToIP(result)
		if backIP.String() != tc.ip {
			t.Errorf("Reverse conversion failed: %d → %s (expected %s)", result, backIP, tc.ip)
		}
	}
}

func TestIncrementIP(t *testing.T) {
	ip := net.ParseIP("192.168.1.1").To4()
	incrementIP(ip)

	if ip.String() != "192.168.1.2" {
		t.Errorf("Expected 192.168.1.2, got %s", ip)
	}

	// Test overflow
	ip = net.ParseIP("192.168.1.255").To4()
	incrementIP(ip)

	if ip.String() != "192.168.2.0" {
		t.Errorf("Expected 192.168.2.0, got %s", ip)
	}
}
