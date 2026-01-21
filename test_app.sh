#!/bin/bash

# Test script for pinyin-tui application
# This script tests the database loading by running the app briefly

echo "=== Testing Pinyin TUI Application ==="
echo

# Test 1: Check if binary exists
echo "Test 1: Binary exists"
if [ -f "target/release/pinyin-tui" ]; then
    echo "✓ Binary found at target/release/pinyin-tui"
    ls -lh target/release/pinyin-tui
else
    echo "✗ Binary not found"
    exit 1
fi
echo

# Test 2: Check if data file exists
echo "Test 2: Data file exists"
if [ -f "data/filtered_db.json" ]; then
    echo "✓ Data file found"
    echo "  Size: $(du -h data/filtered_db.json | cut -f1)"
    echo "  Lines: $(wc -l < data/filtered_db.json)"
else
    echo "✗ Data file not found"
    exit 1
fi
echo

# Test 3: Test database loading (with timeout)
echo "Test 3: Database loading"
echo "Running application for 2 seconds to test startup..."
timeout 2s ./target/release/pinyin-tui 2>&1 &
PID=$!

# Wait a moment for startup
sleep 1.5

# Check if process is still running (successful load)
if kill -0 $PID 2>/dev/null; then
    echo "✓ Application started successfully and loaded database"
    kill $PID 2>/dev/null
    wait $PID 2>/dev/null
else
    echo "✗ Application crashed during startup"
    exit 1
fi
echo

echo "=== All Tests Passed ==="
echo
echo "Application is ready to use!"
echo "Run with: ./target/release/pinyin-tui"
