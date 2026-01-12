# Streaming Mode: Scan Billions of IPs

## Overview

The scanner now supports **streaming mode** for massive IP ranges (up to 2^32 = 4.3 billion addresses) with minimal memory usage!

## The Problem

Scanning large IP ranges used to require loading all IPs into memory:
- `/8` network (16.7M IPs) = ~267 MB RAM
- `/0` network (4.3B IPs) = ~68 GB RAM ❌ **IMPOSSIBLE!**

## The Solution: Streaming Mode

Instead of loading all IPs, generate them **on-demand**:
- ✅ **Memory usage**: ~100 KB (constant, regardless of range size!)
- ✅ **Supports**: Up to 2^32 IPs (entire IPv4 space)
- ✅ **Checkpoint**: Range-based (not per-IP)

## Usage

### Auto-Detection (Recommended)

The scanner automatically uses streaming for ranges > 10 million IPs:

```bash
# Small range: uses memory mode
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw

# Large range: auto-switches to streaming
sudo ./traceroute-scanner -range 10.0.0.0/8 -mode raw
# Output: "⚠️ Large IP range detected (16777216 IPs > 10M limit)"
#         "Switching to streaming mode for memory efficiency..."
```

### Manual Streaming

Force streaming mode with `-streaming` flag:

```bash
# Explicit streaming
sudo ./traceroute-scanner -range 192.168.0.0/16 -mode raw -streaming
```

### Scan Entire IPv4 Space

**Yes, you can scan ALL IPv4 addresses!**

```bash
# All 4.3 billion IPv4 addresses
sudo ./traceroute-scanner \
    -range 0.0.0.0/0 \
    -mode raw \
    -streaming \
    -workers 1000 \
    -timeout 1s
```

## How It Works

### Memory Mode (Small Ranges)

1. **Load** all IPs into slice: `[]net.IP`
2. **Shuffle** IPs (optional)
3. **Submit** to workers
4. **Track** individual IPs in checkpoint

**Memory**: O(n) where n = number of IPs

### Streaming Mode (Large Ranges)

1. **Generate** IPs on-demand via channel
2. **Stream** to workers (no shuffle)
3. **Submit** as generated
4. **Track** IP ranges in checkpoint

**Memory**: O(1) constant!

## Architecture

### IP Generator (ipgenerator.go)

Generates IPs on demand:

```go
type IPRangeIterator struct {
    Start uint32  // First IP as integer
    End   uint32  // Last IP as integer
    Total uint64  // Total count
}

// Generate IPs on demand
func (r *IPRangeIterator) Generate(excludeRanges []CompletedRange) <-chan net.IP {
    ch := make(chan net.IP, 100)
    go func() {
        for i := r.Start; i <= r.End; i++ {
            if !excluded(i) {
                ch <- uint32ToIP(i)
            }
        }
    }()
    return ch
}
```

### Range Checkpoint (rangecheckpoint.go)

Tracks completed IP ranges instead of individual IPs:

```go
type CompletedRange struct {
    Start uint32
    End   uint32
}

// Instead of storing 4 billion IPs:
// CompletedIPs: ["1.0.0.1", "1.0.0.2", ..., "255.255.255.255"]  // 68 GB!

// Store ranges:
// CompletedRanges: [{Start: 16777216, End: 33554431}, ...]  // ~100 KB
```

**Memory savings**: Billions of IPs → Thousands of ranges!

## Performance

### Memory Usage

| Range Size | Memory Mode | Streaming Mode |
|------------|-------------|----------------|
| /24 (256) | 4 KB | 100 KB |
| /16 (65K) | 1 MB | 100 KB |
| /12 (1M) | 16 MB | 100 KB |
| /8 (16M) | 267 MB | 100 KB |
| /4 (256M) | 4.3 GB | 100 KB |
| /0 (4.3B) | 68 GB ❌ | 100 KB ✅ |

### Checkpoint Size

| IPs Scanned | Individual Tracking | Range Tracking |
|-------------|---------------------|----------------|
| 1 million | 20 MB | ~10 KB |
| 100 million | 2 GB | ~100 KB |
| 1 billion | 20 GB | ~1 MB |
| 4.3 billion | 86 GB ❌ | ~5 MB ✅ |

## Examples

### Example 1: Scan /8 Network

```bash
# 16.7 million IPs
sudo ./traceroute-scanner \
    -range 10.0.0.0/8 \
    -mode raw \
    -workers 100 \
    -timeout 1s

# Auto-switches to streaming mode
# Memory usage: ~100 KB (constant)
```

### Example 2: Scan Multiple /8s

```bash
# Three /8 networks (50 million IPs)
for NET in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16; do
    sudo ./traceroute-scanner \
        -range $NET \
        -mode raw \
        -output scan_${NET//\//_}.jsonl \
        -streaming \
        -workers 200
done
```

### Example 3: Entire IPv4 Space

```bash
# ALL 4,294,967,296 IPv4 addresses!
sudo ./traceroute-scanner \
    -range 0.0.0.0/0 \
    -mode raw \
    -streaming \
    -workers 1000 \
    -timeout 500ms \
    -max-hops 15

# Estimated time: ~50 days with 1000 workers at 1 sec/IP
# But fully resumable!
```

### Example 4: Resume Massive Scan

```bash
# Start scan of /6 (67M IPs)
sudo ./traceroute-scanner -range 64.0.0.0/6 -mode raw -streaming -workers 500

# Interrupted after 10M IPs...

# Resume automatically
sudo ./traceroute-scanner -range 64.0.0.0/6 -mode raw -streaming -workers 500
# Output: "Resuming scan: 57000000 IPs remaining"
```

## Features

### Automatic Resume

Streaming mode fully supports resume:

```bash
# Scan 1 billion IPs
sudo ./traceroute-scanner -range 0.0.0.0/2 -mode raw -streaming

# Interrupted after 100M...

# Just run again - automatically resumes
sudo ./traceroute-scanner -range 0.0.0.0/2 -mode raw -streaming
```

### Archive Support

Archive completed scans:

```bash
# Weekly scan of /8
sudo ./traceroute-scanner -range 10.0.0.0/8 -mode raw -streaming -archive

# Archives previous scan with timestamp
# Starts fresh scan
```

### Progress Tracking

Real-time progress with percentage:

```
IP Range: 10.0.0.0 to 10.255.255.255 (16777216 IPs)
Mode: raw (streaming), Workers: 100, Max Hops: 30, Timeout: 1s
...
Progress: 100/16777216 (0.00%)
Progress: 200/16777216 (0.00%)
...
Progress: 1000000/16777216 (5.96%)
Progress: 2000000/16777216 (11.92%)
```

## Limitations

### No Shuffling in Streaming Mode

Streaming mode scans sequentially:

```bash
# Memory mode: Can shuffle
./traceroute-scanner -range 192.168.1.0/24 -shuffle=true

# Streaming mode: Sequential only
./traceroute-scanner -range 10.0.0.0/8 -streaming
# Scans: 10.0.0.0, 10.0.0.1, 10.0.0.2, ...
```

**Why**: Can't shuffle 4 billion IPs in memory!

**Workaround**: Scan multiple smaller ranges in random order.

### Range-Based Checkpoint

Checkpoint tracks ranges, not individual IPs:

```json
{
  "completed_ranges": [
    {"start": 167772160, "end": 167772415},
    {"start": 167772416, "end": 167777215}
  ],
  "scanned_count": 5312,
  "total_ips": 16777216
}
```

**Benefit**: Compact (KB instead of GB)
**Trade-off**: Can't see individual IPs without parsing output file

## Memory Efficiency Details

### IP Generation

IPs generated just-in-time:

```go
// Old way (memory mode)
ips := []net.IP{...}  // All IPs loaded

// New way (streaming)
ipChan := iterator.Generate(excludeRanges)
for ip := range ipChan {
    // Generated on demand
}
```

### Checkpoint Consolidation

Completed IPs merged into ranges automatically:

```
Scan IPs: 10.0.0.1, 10.0.0.2, 10.0.0.3
    ↓
Pending: {10.0.0.1, 10.0.0.2, 10.0.0.3}
    ↓
Consolidate every 100 IPs
    ↓
Range: {Start: 167772161, End: 167772163}
```

Thousands of IPs → One range!

### Automatic Merging

Overlapping ranges merged:

```
Ranges: [{1-100}, {50-150}, {200-300}]
    ↓
Merged: [{1-150}, {200-300}]
```

Keeps checkpoint small.

## Best Practices

### 1. Use Many Workers for Large Scans

```bash
# Small range: 10 workers fine
./traceroute-scanner -range 192.168.1.0/24 -workers 10

# Large range: scale up!
./traceroute-scanner -range 10.0.0.0/8 -workers 1000 -streaming
```

### 2. Reduce Timeout for Speed

```bash
# Fast scan (may miss some hops)
sudo ./traceroute-scanner \
    -range 10.0.0.0/8 \
    -streaming \
    -timeout 500ms \
    -workers 500
```

### 3. Use Raw Mode for Performance

```bash
# External mode: spawns traceroute subprocess per IP
./traceroute-scanner -range X -mode external  # Slower

# Raw mode: direct packets
sudo ./traceroute-scanner -range X -mode raw  # Faster
```

### 4. Monitor Progress File

```bash
# Watch progress in another terminal
watch -n 10 'cat traceroute_results.jsonl.progress | jq ".scanned_count, .total_ips"'
```

### 5. Backup Important Scans

```bash
# Backup progress periodically
while true; do
    cp scan.jsonl.progress scan.jsonl.progress.backup
    sleep 3600  # Every hour
done &
```

## Troubleshooting

### "IP range too large" Error

**Old behavior**: Would try to load billions of IPs, run out of memory

**New behavior**: Auto-switches to streaming mode

If you still see this error, explicitly use `-streaming`:

```bash
sudo ./traceroute-scanner -range 0.0.0.0/0 -mode raw -streaming
```

### High Memory Usage

If memory usage is high even in streaming mode:
- Check worker count (more workers = more memory)
- Check output buffer (results being written?)
- Check pending IPs map (consolidates every 100 IPs)

```bash
# Reduce workers if memory constrained
sudo ./traceroute-scanner -range X -streaming -workers 50
```

### Progress File Growing Too Large

Range consolidation should keep it small, but if it grows:
- Check if many disconnected ranges (normal for sparse scans)
- Checkpoint size ≈ number of ranges * 20 bytes
- Even 100,000 ranges = only 2 MB

## Technical Details

### IP to Integer Conversion

IPs stored as uint32 for efficiency:

```go
// IP to uint32
ip := net.ParseIP("10.0.0.1")
ipInt := binary.BigEndian.Uint32(ip.To4())  // 167772161

// uint32 to IP
ip := make(net.IP, 4)
binary.BigEndian.PutUint32(ip, 167772161)  // 10.0.0.1
```

### Range Exclusion

When resuming, completed ranges excluded during generation:

```go
for i := Start; i <= End; i++ {
    if inCompletedRange(i) {
        i = endOfRange  // Skip entire range
        continue
    }
    ch <- uint32ToIP(i)
}
```

Efficient: O(log n) range check, not O(n) IP check!

### Checkpoint Format

```json
{
  "output_file": "scan.jsonl",
  "completed_ranges": [
    {"start": 167772160, "end": 167777215},
    {"start": 184549376, "end": 184614911}
  ],
  "scanned_count": 71024,
  "total_ips": 16777216,
  "range_start": 167772160,
  "range_end": 184549375
}
```

## Summary

### Key Features

✅ **Memory Efficient**: ~100 KB for any range size
✅ **Supports 2^32 IPs**: Entire IPv4 space scannable
✅ **Auto-Detection**: Switches automatically for large ranges
✅ **Range Checkpoints**: KB instead of GB progress files
✅ **Fully Resumable**: All features work (resume, archive)
✅ **Progress Tracking**: Real-time percentage updates

### When to Use

| Range Size | Mode | Memory |
|------------|------|--------|
| < 10M IPs | Memory (default) | MB |
| > 10M IPs | Streaming (auto) | ~100 KB |
| Force streaming | `-streaming` flag | ~100 KB |

### Performance

**Scanning 1 billion IPs:**
- Memory mode: 16 GB RAM ❌
- Streaming mode: 100 KB RAM ✅

**Time estimate** (1000 workers, 1 sec/IP):
- 1M IPs: ~17 minutes
- 10M IPs: ~3 hours
- 100M IPs: ~1 day
- 1B IPs: ~12 days
- 4.3B IPs: ~50 days

**But fully resumable**, so interruptions don't matter!

---

**You can now scan the entire IPv4 internet with minimal memory!** 🌍🚀

```bash
# The ultimate command
sudo ./traceroute-scanner \
    -range 0.0.0.0/0 \
    -mode raw \
    -streaming \
    -workers 1000 \
    -timeout 500ms \
    -max-hops 15 \
    -output internet_scan.jsonl
```

Just be sure you have permission and prepare for a long scan! 😄
