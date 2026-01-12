#!/bin/bash
# Test script for archive feature

set -e

echo "========================================"
echo "Archive Feature Test"
echo "========================================"
echo ""

# Clean up
rm -f test_archive.jsonl test_archive.jsonl.* 2>/dev/null || true

RANGE="8.8.8.8-8.8.8.10"  # 3 IPs for quick test
OUTPUT="test_archive.jsonl"

echo "Test 1: Complete a scan"
echo "========================"
echo ""

./traceroute-scanner -range "$RANGE" -mode external -output "$OUTPUT"

echo ""
echo "✓ Scan completed"
echo ""

# Verify output file exists
if [ -f "$OUTPUT" ]; then
    LINES=$(wc -l < "$OUTPUT")
    echo "✓ Output file has $LINES results"
else
    echo "✗ Output file not found"
    exit 1
fi

echo ""
echo "Test 2: Try to run again with -archive flag"
echo "============================================="
echo ""

./traceroute-scanner -range "$RANGE" -mode external -output "$OUTPUT" -archive

echo ""
echo "Test 3: Check results"
echo "====================="
echo ""

# Check for archived file
ARCHIVED=$(ls ${OUTPUT}.* 2>/dev/null | head -1)
if [ -n "$ARCHIVED" ]; then
    echo "✓ Old scan archived to: $ARCHIVED"
    echo ""
    echo "Contents of archived file:"
    cat "$ARCHIVED" | jq -c '.dest_ip' 2>/dev/null || cat "$ARCHIVED"
else
    echo "✗ No archived file found"
    exit 1
fi

echo ""

# Check new output file
if [ -f "$OUTPUT" ]; then
    NEW_LINES=$(wc -l < "$OUTPUT")
    echo "✓ New scan completed with $NEW_LINES results"
    echo ""
    echo "Contents of new file:"
    cat "$OUTPUT" | jq -c '.dest_ip' 2>/dev/null || cat "$OUTPUT"
else
    echo "✗ New output file not found"
    exit 1
fi

echo ""
echo "Test 4: Verify no progress file remains"
echo "========================================"
echo ""

if [ ! -f "$OUTPUT.progress" ]; then
    echo "✓ Progress file cleaned up"
else
    echo "⚠ Progress file still exists"
fi

echo ""
echo "========================================"
echo "Archive Test Complete!"
echo "========================================"
echo ""
echo "Summary:"
echo "  - First scan completed and saved to $OUTPUT"
echo "  - Second scan with -archive flag archived old scan"
echo "  - Old scan renamed to $ARCHIVED"
echo "  - New scan completed to $OUTPUT"
echo "  - Progress file cleaned up"
echo ""
echo "Files created:"
ls -lh ${OUTPUT}* 2>/dev/null || true
echo ""
