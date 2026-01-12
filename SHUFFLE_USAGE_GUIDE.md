# Shuffle Mode - Quick Usage Guide

## Basic Usage

### Enable shuffle mode with random seed:
```bash
./traceroute-scanner -range <IP-RANGE> -streaming -shuffle
```

### Enable shuffle mode with specific seed (reproducible):
```bash
./traceroute-scanner -range <IP-RANGE> -streaming -shuffle-seed=42
```

### Without shuffle (sequential):
```bash
./traceroute-scanner -range <IP-RANGE> -streaming
```

## Recommended Settings by Use Case

### 1. Testing / Development (Small Range)
```bash
# Test with 3-10 routable IPs
./traceroute-scanner -range 8.8.8.8-8.8.8.10 \
  -streaming -shuffle-seed=42 -fresh -workers 3 -timeout 2s
```

**Why these settings:**
- Small range completes in seconds
- Routable IPs (Google DNS) respond quickly
- Fixed seed for reproducibility
- Few workers avoid rate limiting

### 2. Research / Reproducible Scans
```bash
# Same seed = same scan order
./traceroute-scanner -range 1.0.0.0/16 \
  -streaming -shuffle-seed=12345 -workers 50 -timeout 2s
```

**Why these settings:**
- Fixed seed ensures reproducibility
- Other researchers can verify your methodology
- Document the seed in your research paper
- Same seed on same range = identical scan order

### 3. Stealth Scanning (Avoid Detection)
```bash
# Random seed = unpredictable pattern
./traceroute-scanner -range <TARGET> \
  -streaming -shuffle -workers 5 -timeout 3s
```

**Why these settings:**
- Random seed prevents predictable patterns
- Fewer workers reduce network noise
- Longer timeout looks less aggressive
- Different runs have different patterns

### 4. Large-Scale Internet Surveys
```bash
# Scan millions of IPs efficiently
./traceroute-scanner -range 1.0.0.0/8 \
  -streaming -shuffle -workers 100 -timeout 1s -max-hops 20
```

**Why these settings:**
- Shuffle avoids sequential patterns that might look like attacks
- Many workers for throughput
- Short timeout and fewer hops for speed
- Streaming mode keeps memory usage constant

### 5. Local Network Scanning
```bash
# Scan private network (LAN)
./traceroute-scanner -range 192.168.1.0/24 \
  -streaming -shuffle -workers 10 -timeout 500ms -max-hops 5
```

**Why these settings:**
- Shuffle prevents overloading network segments sequentially
- Very short timeout (500ms) for LAN speeds
- Few hops (devices are close)
- Will quickly skip unreachable IPs

### 6. Distributed Scanning (Multiple Machines)
```bash
# Machine 1: Uses seed=1000
./traceroute-scanner -range 0.0.0.0/8 \
  -streaming -shuffle-seed=1000 -workers 50

# Machine 2: Uses seed=2000 (different pattern)
./traceroute-scanner -range 0.0.0.0/8 \
  -streaming -shuffle-seed=2000 -workers 50
```

**Why these settings:**
- Different seeds create different scan orders
- Reduces collision risk if machines overlap
- Can later verify no duplicates by checking seeds
- Each machine has deterministic, reproducible order

## Performance Tuning

### For Speed (may be detected as aggressive)
```bash
./traceroute-scanner -range <TARGET> \
  -streaming -shuffle \
  -workers 200 \
  -timeout 500ms \
  -max-hops 15
```

### For Stealth (slower but less noticeable)
```bash
./traceroute-scanner -range <TARGET> \
  -streaming -shuffle \
  -workers 2 \
  -timeout 5s \
  -max-hops 30
```

### For Accuracy (thorough scanning)
```bash
./traceroute-scanner -range <TARGET> \
  -streaming -shuffle \
  -workers 20 \
  -timeout 5s \
  -max-hops 50
```

## Resume / Archive Behavior

### Resume a previous scan (uses same seed automatically)
```bash
# Just run the same command - will auto-resume
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle-seed=42
```

**Note:** Seed is saved in checkpoint file, so resumed scans continue with the same order.

### Start fresh (delete progress)
```bash
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle-seed=42 -fresh
```

### Archive completed scan and start new one
```bash
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle-seed=42 -archive
```

## Troubleshooting

### Scanner appears stuck?

**Symptom:** No progress output after initialization

**Likely Causes:**
1. **Private IP range** (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16) - these don't route on internet
2. **First batch of IPs unreachable** - waiting for timeouts

**Solutions:**
- Use routable IPs for testing: `8.8.8.8-8.8.8.10`
- Reduce timeout for private ranges: `-timeout 1s -max-hops 10`
- Be patient: First results appear after timeout period

### Want to verify shuffle is working?

```bash
# Quick test with routable IPs
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -shuffle-seed=42 -fresh -output test1.jsonl
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -shuffle-seed=42 -fresh -output test2.jsonl

# Check scan order (should be identical)
jq -r '.DestIP' test1.jsonl
jq -r '.DestIP' test2.jsonl
```

## Memory Usage

**Streaming + Shuffle = O(1) memory!**

All of these use the same amount of memory (a few MB):
- `-range 10.0.0.0/24` (256 IPs)
- `-range 10.0.0.0/16` (65,536 IPs)
- `-range 10.0.0.0/8` (16,777,216 IPs)

The Blackrock cipher generates IPs on-demand, so memory usage is constant regardless of range size.

## Understanding Seeds

### Seed = 0 (default when using -shuffle without -shuffle-seed)
- Generates random seed using crypto/rand
- Different every run
- Unpredictable scan order

### Seed = specific number (e.g., -shuffle-seed=42)
- Deterministic scan order
- Same seed + same range = identical order
- Reproducible for research/debugging

### Examples:
```bash
# These produce DIFFERENT orders each time
./traceroute-scanner -range 10.0.0.0/24 -streaming -shuffle
./traceroute-scanner -range 10.0.0.0/24 -streaming -shuffle

# These produce IDENTICAL order
./traceroute-scanner -range 10.0.0.0/24 -streaming -shuffle-seed=42
./traceroute-scanner -range 10.0.0.0/24 -streaming -shuffle-seed=42

# These produce DIFFERENT orders (different seeds)
./traceroute-scanner -range 10.0.0.0/24 -streaming -shuffle-seed=42
./traceroute-scanner -range 10.0.0.0/24 -streaming -shuffle-seed=99
```

## Integration with Other Flags

Shuffle works with all other features:

```bash
# Shuffle + Resume
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle-seed=42
# ... interrupt with Ctrl+C ...
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle-seed=42  # resumes

# Shuffle + Fresh start
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle -fresh

# Shuffle + Archive old results
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle -archive

# Shuffle + Custom output
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle -output my_results.jsonl

# Shuffle + ICMP mode (requires root)
sudo ./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle -mode raw

# All flags together
sudo ./traceroute-scanner -range 1.0.0.0/16 \
  -streaming -shuffle-seed=42 -fresh \
  -workers 50 -timeout 2s -max-hops 20 \
  -mode raw -output results.jsonl
```

## Security Considerations

### When to use shuffle:
- ✅ Scanning large IP ranges (looks less like an attack)
- ✅ Repeated scans of same targets (vary the pattern)
- ✅ Research that will be published (provide reproducibility)
- ✅ Avoiding sequential pattern detection

### When sequential might be fine:
- Small internal network scans
- Debugging specific IP ranges
- Following IP allocation patterns

### Legal reminder:
- Ensure you have permission to scan target networks
- Respect robots.txt and network policies
- Some networks may block or rate-limit scanners
- Consider notifying network owners of large scans

## Performance Benchmarks

### Blackrock Cipher:
- **321 nanoseconds per permutation**
- Negligible overhead compared to network operations
- Same performance for any range size (/24, /16, /8)

### IP Generation:
- **20,000 IPs in 8 milliseconds**
- Streaming: constant memory usage
- No initial delay (starts immediately)

### Scan Speed (depends on network and targets):
- **Responsive IPs**: 0.1-2 seconds per IP
- **Unreachable IPs**: 90-180 seconds per IP (waiting for timeout)
- **Mixed networks**: Varies by reachability

## Examples from Real Use Cases

### Internet Census:
```bash
./traceroute-scanner -range 0.0.0.0/0 -streaming -shuffle -workers 500 -timeout 1s
```
(Would take weeks, scans entire IPv4 space)

### ISP Network Mapping:
```bash
./traceroute-scanner -range 203.0.113.0/20 -streaming -shuffle-seed=42 -workers 30
```

### University Network Audit:
```bash
./traceroute-scanner -range 10.1.0.0/16 -streaming -shuffle -timeout 500ms -max-hops 5
```

### Honeypot Research:
```bash
./traceroute-scanner -range <honeypot-ranges> -streaming -shuffle-seed=<date> -workers 10
```

## Getting Help

For issues or questions:
1. Check SHUFFLE_DIAGNOSIS.md for common problems
2. Run test_shuffle_live.sh to verify functionality
3. Use `-timeout 2s -max-hops 15` for faster testing
4. Test with 8.8.8.8-8.8.8.10 to verify basic functionality
