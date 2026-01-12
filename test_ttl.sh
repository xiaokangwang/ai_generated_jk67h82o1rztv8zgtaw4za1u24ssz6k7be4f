#!/bin/bash

echo "Testing TTL behavior with raw sockets"
echo "======================================"
echo ""

# Start tcpdump in background
echo "Starting packet capture..."
timeout 10 tcpdump -i any -n 'icmp and host 8.8.8.8' -vvv > /tmp/tcpdump.out 2>&1 &
TCPDUMP_PID=$!

# Give tcpdump time to start
sleep 1

echo "Sending ICMP with TTL=1..."
./debug-traceroute -ip 8.8.8.8 -hops 1 > /tmp/debug.out 2>&1

# Wait a moment for packets
sleep 2

# Stop tcpdump
kill $TCPDUMP_PID 2>/dev/null
wait $TCPDUMP_PID 2>/dev/null

echo ""
echo "=== Debug tool output ==="
cat /tmp/debug.out | grep -A20 "TTL 1"

echo ""
echo "=== Packet capture (tcpdump) ==="
cat /tmp/tcpdump.out | grep -E "(ttl|TTL|ICMP)"

echo ""
echo "Full tcpdump output:"
cat /tmp/tcpdump.out

# Cleanup
rm -f /tmp/tcpdump.out /tmp/debug.out
