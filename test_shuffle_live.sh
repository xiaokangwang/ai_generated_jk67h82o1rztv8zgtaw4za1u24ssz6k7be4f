#!/bin/bash

# Test script to verify shuffle mode works with routable IPs

echo "============================================="
echo "Testing Shuffle Mode with Routable IPs"
echo "============================================="
echo ""

# Clean up any previous test files
rm -f test_shuffle*.jsonl test_shuffle*.progress

echo "Test 1: Sequential scan of 3 IPs (Google DNS)"
echo "---------------------------------------------"
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -output test_shuffle_seq.jsonl -fresh -workers 3 -timeout 2s
echo ""

echo "Test 2: Shuffled scan with seed=42"
echo "---------------------------------------------"
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -shuffle -shuffle-seed=42 -output test_shuffle_42.jsonl -fresh -workers 3 -timeout 2s
echo ""

echo "Test 3: Shuffled scan with seed=123 (different order)"
echo "---------------------------------------------"
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -shuffle -shuffle-seed=123 -output test_shuffle_123.jsonl -fresh -workers 3 -timeout 2s
echo ""

echo "Test 4: Shuffled scan with seed=42 again (same order as Test 2)"
echo "---------------------------------------------"
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -shuffle -shuffle-seed=42 -output test_shuffle_42_repeat.jsonl -fresh -workers 3 -timeout 2s
echo ""

echo "============================================="
echo "Results"
echo "============================================="
echo ""

echo "Sequential scan order:"
jq -r '.DestIP' test_shuffle_seq.jsonl 2>/dev/null | sort | xargs
echo ""

echo "Shuffle seed=42 (first run):"
jq -r '.DestIP' test_shuffle_42.jsonl 2>/dev/null | sort | xargs
echo ""

echo "Shuffle seed=123:"
jq -r '.DestIP' test_shuffle_123.jsonl 2>/dev/null | sort | xargs
echo ""

echo "Shuffle seed=42 (second run - should match first):"
jq -r '.DestIP' test_shuffle_42_repeat.jsonl 2>/dev/null | sort | xargs
echo ""

echo "Scan order for seed=42 (first run):"
jq -r '.DestIP' test_shuffle_42.jsonl 2>/dev/null | xargs
echo ""

echo "Scan order for seed=42 (second run):"
jq -r '.DestIP' test_shuffle_42_repeat.jsonl 2>/dev/null | xargs
echo ""

echo "✓ Shuffle mode test complete!"
echo ""
echo "Expected results:"
echo "  - All scans should have found all 3 IPs: 8.8.8.8, 8.8.8.9, 8.8.8.10"
echo "  - Seed=42 runs should have IDENTICAL scan order"
echo "  - Seed=123 should have DIFFERENT scan order than seed=42"
echo "  - All should complete in <10 seconds (routable IPs respond quickly)"
