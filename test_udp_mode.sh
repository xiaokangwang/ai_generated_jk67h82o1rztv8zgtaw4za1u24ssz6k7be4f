#!/bin/bash
# Test script for UDP traceroute implementation
# Tests both raw UDP mode and external mode for comparison

set -e

echo "========================================"
echo "UDP Traceroute Test Script"
echo "========================================"
echo ""

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "❌ Error: This script must be run as root (use sudo)"
    echo ""
    echo "Usage: sudo ./test_udp_mode.sh"
    exit 1
fi

# Test target
TARGET="8.8.8.8"
HOPS=10

echo "Testing UDP-based raw mode..."
echo "Target: $TARGET"
echo "Max hops: $HOPS"
echo ""

# Test 1: Debug tool
echo "=== Test 1: UDP Debug Tool ==="
echo "Command: ./debug-traceroute-udp -ip $TARGET -hops $HOPS"
echo ""

if [ -f "./debug-traceroute-udp" ]; then
    ./debug-traceroute-udp -ip "$TARGET" -hops "$HOPS"
    echo ""
    echo "✅ Debug tool completed"
else
    echo "❌ Debug tool not found. Run: go build -o debug-traceroute-udp cmd_debug_udp.go traceroute.go udp_traceroute.go icmp.go socket.go"
    exit 1
fi

echo ""
echo "----------------------------------------"
echo ""

# Test 2: Main scanner with raw mode
echo "=== Test 2: Main Scanner (Raw Mode) ==="
echo "Command: ./traceroute-scanner -range $TARGET -mode raw -max-hops $HOPS"
echo ""

if [ -f "./traceroute-scanner" ]; then
    OUTPUT_FILE="test_raw_output.jsonl"
    ./traceroute-scanner -range "$TARGET" -mode raw -max-hops "$HOPS" -output "$OUTPUT_FILE"
    echo ""
    echo "Raw mode output saved to: $OUTPUT_FILE"
    echo ""
    echo "Result:"
    cat "$OUTPUT_FILE" | jq '.' 2>/dev/null || cat "$OUTPUT_FILE"
    echo ""
    echo "✅ Raw mode scan completed"
else
    echo "❌ Scanner not found. Run: go build -o traceroute-scanner"
    exit 1
fi

echo ""
echo "----------------------------------------"
echo ""

# Test 3: External mode for comparison
echo "=== Test 3: External Mode (Comparison) ==="
echo "Command: ./traceroute-scanner -range $TARGET -mode external -max-hops $HOPS"
echo ""

OUTPUT_FILE_EXT="test_external_output.jsonl"
./traceroute-scanner -range "$TARGET" -mode external -max-hops "$HOPS" -output "$OUTPUT_FILE_EXT"
echo ""
echo "External mode output saved to: $OUTPUT_FILE_EXT"
echo ""
echo "Result:"
cat "$OUTPUT_FILE_EXT" | jq '.' 2>/dev/null || cat "$OUTPUT_FILE_EXT"
echo ""
echo "✅ External mode scan completed"

echo ""
echo "========================================"
echo "Test Summary"
echo "========================================"
echo ""

# Compare hop counts
RAW_HOPS=$(cat test_raw_output.jsonl | jq '.hops | length' 2>/dev/null || echo "unknown")
EXT_HOPS=$(cat test_external_output.jsonl | jq '.hops | length' 2>/dev/null || echo "unknown")

echo "Raw mode hops: $RAW_HOPS"
echo "External mode hops: $EXT_HOPS"
echo ""

if [ "$RAW_HOPS" != "unknown" ] && [ "$EXT_HOPS" != "unknown" ]; then
    if [ "$RAW_HOPS" -gt 1 ]; then
        echo "✅ SUCCESS: Raw UDP mode is working!"
        echo "   Raw mode discovered $RAW_HOPS hops (not just destination)"
        echo ""
        echo "   UDP traceroute works through your NAT VM! 🎉"
    else
        echo "⚠️  WARNING: Raw mode only found 1 hop"
        echo "   This suggests UDP mode may still have issues in your environment"
        echo ""
        echo "   Recommendation: Use external mode instead"
    fi

    if [ "$EXT_HOPS" -gt 1 ]; then
        echo "✅ External mode working correctly ($EXT_HOPS hops)"
    fi
else
    echo "⚠️  Could not parse results. Check output files manually:"
    echo "   - test_raw_output.jsonl"
    echo "   - test_external_output.jsonl"
fi

echo ""
echo "========================================"
echo "Files created:"
echo "  - test_raw_output.jsonl"
echo "  - test_external_output.jsonl"
echo "========================================"
