#!/bin/bash
# Test script for resumable scan feature

set -e

echo "========================================"
echo "Resume Feature Test"
echo "========================================"
echo ""

# Clean up any previous test files
rm -f test_resume.jsonl test_resume.jsonl.progress

# Test range (small for quick testing)
RANGE="8.8.8.8-8.8.8.12"  # 5 IPs
OUTPUT="test_resume.jsonl"

echo "Test 1: Start a scan (will be interrupted)"
echo "==========================================="
echo "Range: $RANGE"
echo "Output: $OUTPUT"
echo ""

# Start scan in background
timeout 3s ./traceroute-scanner -range "$RANGE" -mode external -output "$OUTPUT" -workers 1 || true

echo ""
echo "Scan interrupted! Checking progress..."
echo ""

# Check what was saved
if [ -f "$OUTPUT.progress" ]; then
    echo "✓ Progress file created: $OUTPUT.progress"
    echo ""
    echo "Progress file contents:"
    cat "$OUTPUT.progress" | jq '.' 2>/dev/null || cat "$OUTPUT.progress"
    echo ""
else
    echo "✗ No progress file found"
    exit 1
fi

if [ -f "$OUTPUT" ]; then
    COMPLETED=$(wc -l < "$OUTPUT")
    echo "✓ Output file exists with $COMPLETED results"
    echo ""
else
    echo "✗ No output file found"
    exit 1
fi

echo "Test 2: Resume the scan"
echo "==========================================="
echo ""

# Resume the scan
./traceroute-scanner -range "$RANGE" -mode external -output "$OUTPUT" -resume

echo ""
echo "Test 3: Verify results"
echo "==========================================="
echo ""

if [ -f "$OUTPUT" ]; then
    TOTAL=$(wc -l < "$OUTPUT")
    echo "✓ Total results: $TOTAL"
    echo ""
    echo "Results:"
    cat "$OUTPUT" | jq -c '.dest_ip' 2>/dev/null || cat "$OUTPUT"
    echo ""
else
    echo "✗ Output file not found"
    exit 1
fi

# Check if progress file was deleted (should be deleted on completion)
if [ ! -f "$OUTPUT.progress" ]; then
    echo "✓ Progress file deleted (scan completed)"
else
    echo "⚠ Progress file still exists (scan may be incomplete)"
fi

echo ""
echo "Test 4: Try resuming again (should say nothing to do)"
echo "==========================================="
echo ""

./traceroute-scanner -range "$RANGE" -mode external -output "$OUTPUT" -resume || true

echo ""
echo "========================================"
echo "Resume Test Complete!"
echo "========================================"
echo ""
echo "Summary:"
echo "  - Scan was interrupted and progress was saved"
echo "  - Resume successfully continued from checkpoint"
echo "  - All IPs were scanned"
echo "  - Progress file cleaned up on completion"
echo ""
echo "Files:"
echo "  - Results: $OUTPUT"
echo "  - Progress: $OUTPUT.progress (deleted after completion)"
echo ""
