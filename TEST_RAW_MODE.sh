#!/bin/bash

echo "==================================="
echo "Raw Mode Traceroute Fix Verification"
echo "==================================="
echo ""
echo "This script tests the raw ICMP traceroute mode."
echo "NOTE: Raw mode requires root privileges!"
echo ""

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "ERROR: This test must be run as root (use sudo)"
    echo ""
    echo "Usage: sudo ./TEST_RAW_MODE.sh"
    exit 1
fi

echo "Test 1: Raw mode to 8.8.8.8 (Google DNS)"
echo "Expected: Multiple hops showing intermediate routers"
echo "----------------------------------------"
rm -f traceroute_results.jsonl

./traceroute-scanner -range 8.8.8.8 -mode raw -max-hops 15 -timeout 2s

echo ""
echo "Results:"
echo "--------"

if [ -f traceroute_results.jsonl ]; then
    python3 << 'EOF'
import json
import sys

try:
    with open('traceroute_results.jsonl', 'r') as f:
        data = json.load(f)

    print(f"Destination: {data['dest_ip']}")
    print(f"Reached: {data['reached']}")
    print(f"Total hops: {len(data['hops'])}")
    print(f"Duration: {data['duration_ns']/1e9:.2f}s")
    print("")
    print("Hop details:")
    print("-" * 60)

    for hop in data['hops']:
        status = "TIMEOUT" if hop['timeout'] else hop['ip']
        rtt_ms = f"{hop['rtt_ns']/1e6:.2f}ms" if hop['rtt_ns'] > 0 else "-"
        print(f"  TTL {hop['ttl']:2d}: {status:20s}  RTT: {rtt_ms}")

    print("")

    # Verification
    if len(data['hops']) == 1:
        print("⚠️  WARNING: Only 1 hop detected!")
        print("    This might indicate the fix didn't work properly.")
        print("    A traceroute to 8.8.8.8 should show multiple hops.")
    elif len(data['hops']) > 1:
        print("✅ SUCCESS: Multiple hops detected!")
        print(f"   The raw traceroute is working correctly ({len(data['hops'])} hops).")

    # Check if first hop is the destination
    if len(data['hops']) > 0 and not data['hops'][0]['timeout']:
        first_ip = data['hops'][0]['ip']
        if first_ip == data['dest_ip']:
            print("⚠️  WARNING: First hop is the destination IP!")
            print("    This suggests the traceroute bypassed intermediate routers.")
        else:
            print(f"✅ First hop is intermediate router: {first_ip}")

except Exception as e:
    print(f"Error parsing results: {e}")
    sys.exit(1)
EOF
else
    echo "ERROR: No results file created!"
    exit 1
fi

echo ""
echo "==================================="
echo "Test 2: Comparison with system traceroute"
echo "==================================="
echo ""
echo "Running system traceroute for comparison:"
echo "----------------------------------------"
traceroute -n -m 15 -w 2 8.8.8.8 | head -20

echo ""
echo "==================================="
echo "Testing complete!"
echo "==================================="
