# Quick Start Guide

## TL;DR - It Works! 🎉

Your traceroute scanner is **fully functional** with UDP-based raw mode working through NAT!

## Quick Test

```bash
# Test raw mode (requires root)
sudo ./debug-traceroute-udp -ip 8.8.8.8 -hops 30

# Expected: 10-15 hops showing your gateway, routers, and 8.8.8.8
```

If you see multiple hops (not just 1), you're good to go! ✅

## Choose Your Mode

### Option 1: Raw Mode (UDP-based)
**Pros**: Direct packet control, slightly faster, proper validation
**Cons**: Requires root

```bash
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw
```

### Option 2: External Mode
**Pros**: No root required, proven everywhere
**Cons**: Subprocess overhead

```bash
./traceroute-scanner -range 8.8.8.8 -mode external
```

**Both work perfectly in your KVM NAT VM!**

## Common Usage Examples

### 1. Single IP Scan
```bash
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw
# Output: traceroute_results.jsonl
```

### 2. Subnet Scan
```bash
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -workers 20
# Scans entire /24 subnet with 20 concurrent workers
```

### 3. IP Range Scan
```bash
sudo ./traceroute-scanner -range 10.0.0.1-10.0.0.100 -mode raw
# Scans 100 IPs
```

### 4. Stealth Scan (Shuffled IPs)
```bash
sudo ./traceroute-scanner -range 192.168.0.0/16 -mode raw -shuffle=true -workers 50
# Randomizes scan order to avoid detection
```

### 5. Fast Scan (Lower Timeout)
```bash
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -timeout 1s -workers 100
# Aggressive scanning for responsive networks
```

### 6. Local Network Scan
```bash
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -max-hops 10
# Reduce hops for local networks
```

### 7. Custom Output File
```bash
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw -output my_results.jsonl
```

### 8. No Root Required (External Mode)
```bash
./traceroute-scanner -range 192.168.1.0/24 -mode external -workers 20
# Works without sudo
```

### 9. Resume Interrupted Scan (Automatic!)
```bash
# Start a large scan
sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 50

# If interrupted (Ctrl+C, power loss, etc.), just run the same command:
sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 50
# Automatically resumes where it left off!
```

### 10. Force Fresh Start
```bash
# Want to start over? Use -fresh flag
sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 50 -fresh
```

## Output Format

Results are saved in JSON Lines format (`.jsonl`):

```json
{
  "dest_ip": "8.8.8.8",
  "hops": [
    {"ttl": 1, "ip": "10.0.2.2", "rtt_ns": 250000, "timeout": false},
    {"ttl": 2, "ip": "192.168.1.1", "rtt_ns": 78690000, "timeout": false},
    {"ttl": 3, "ip": "", "rtt_ns": 0, "timeout": true},
    {"ttl": 4, "ip": "172.24.112.89", "rtt_ns": 16700000, "timeout": false},
    ...
    {"ttl": 12, "ip": "8.8.8.8", "rtt_ns": 17940000, "timeout": false}
  ],
  "reached": true,
  "timestamp": "2026-01-12T16:45:00Z",
  "duration_ns": 4370000000
}
```

### Processing Results

**View with jq:**
```bash
cat traceroute_results.jsonl | jq '.'
```

**Count successful scans:**
```bash
cat traceroute_results.jsonl | jq 'select(.reached == true)' | wc -l
```

**Extract all discovered IPs:**
```bash
cat traceroute_results.jsonl | jq -r '.hops[].ip' | grep -v '^$' | sort -u
```

**Find average hop count:**
```bash
cat traceroute_results.jsonl | jq '.hops | length' | awk '{sum+=$1; n++} END {print sum/n}'
```

## Command-Line Options

```
Usage: ./traceroute-scanner [OPTIONS]

Required:
  -range string
        IP range to scan (single IP, CIDR, or range)
        Examples: 8.8.8.8, 192.168.1.0/24, 10.0.0.1-10.0.0.10

Optional:
  -mode string
        Traceroute mode: 'raw' or 'external' (default "external")

  -output string
        Output file path (default "traceroute_results.jsonl")

  -workers int
        Number of concurrent workers (default 10)

  -max-hops int
        Maximum number of hops (default 30)

  -timeout duration
        Timeout per hop (default 3s)

  -shuffle
        Shuffle IP order for stealth (default true)
```

## Performance Tips

### Speed vs Accuracy Trade-offs

**Fast Scanning** (may miss some hops):
```bash
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -workers 100 -timeout 1s -max-hops 15
```

**Thorough Scanning** (slower but complete):
```bash
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -workers 10 -timeout 5s -max-hops 30
```

**Balanced** (recommended):
```bash
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -workers 20 -timeout 2s -max-hops 30
```

### Worker Count Guidelines

- **Local network (fast)**: 50-100 workers
- **Internet scanning (mixed)**: 20-50 workers
- **Slow/congested networks**: 5-10 workers
- **Respectful scanning**: 10-20 workers

### When to Use Each Mode

**Use Raw Mode When:**
- ✅ You have root access
- ✅ You want direct packet control
- ✅ You need maximum performance
- ✅ You want detailed packet validation

**Use External Mode When:**
- ✅ You don't have root access
- ✅ You want proven reliability
- ✅ You're scanning from a restricted environment
- ✅ Root privilege is inconvenient

## Stealth Scanning

### Why Shuffle?

Sequential IP scanning (1.1.1.1, 1.1.1.2, 1.1.1.3...) is:
- ❌ Easily detected by IDS/IPS
- ❌ Flagged as malicious behavior
- ❌ Creates obvious patterns

Shuffled scanning:
- ✅ Harder to correlate
- ✅ Less suspicious
- ✅ Evades simple detection

### Enable Shuffling
```bash
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -shuffle=true
# Default: shuffle=true (already enabled)
```

### Disable Shuffling (Not Recommended)
```bash
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -shuffle=false
# WARNING: Creates obvious scanning pattern!
```

## Verification

### Test Raw Mode Works
```bash
sudo ./debug-traceroute-udp -ip 8.8.8.8 -hops 30
```

Expected output:
```
=== Results ===
Destination: 8.8.8.8
Reached: true
Total hops: 12

   1. 10.0.2.2             0.25ms    ← Your gateway
   2. 192.168.1.1          78.69ms   ← Your router
   3. *                    -         ← Timeout
   ...
  12. 8.8.8.8              17.94ms   ← Destination!
```

If you see multiple hops: ✅ **Working!**
If you see only 1 hop: ❌ **Problem** (use external mode)

### Test External Mode Works
```bash
./traceroute-scanner -range 8.8.8.8 -mode external -output test_external.jsonl
cat test_external.jsonl | jq '.hops | length'
```

Expected: 10-15 (number of hops)

### Compare Both Modes
```bash
# Run test script
sudo ./test_udp_mode.sh

# Compares raw vs external mode automatically
```

## Security Considerations

### Legal
⚠️ **Only scan networks you own or have permission to scan**
- Unauthorized scanning may be illegal
- Always obtain proper authorization
- Be aware of local laws and regulations

### Ethical
- 🤝 Be respectful of network resources
- 🚦 Consider rate limiting for large scans
- 📊 Document your authorization
- 🔒 Secure your scan results

### Technical
- 🔀 Use IP shuffling to reduce detection risk
- ⏱️ Lower worker count for less aggressive scanning
- 📝 Keep logs of what you scanned and why
- 🛡️ Some networks may block or throttle you

## Troubleshooting

### "Permission denied" or "Operation not permitted"
**Problem**: Running raw mode without root
**Solution**: Use `sudo` or switch to external mode

```bash
# Fix 1: Add sudo
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw

# Fix 2: Use external mode (no sudo needed)
./traceroute-scanner -range 8.8.8.8 -mode external
```

### "traceroute: command not found"
**Problem**: External mode needs traceroute binary
**Solution**: Install traceroute

```bash
# Debian/Ubuntu
sudo apt-get install traceroute

# RHEL/CentOS
sudo yum install traceroute

# Or use raw mode instead
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw
```

### Only seeing timeouts (all hops show "*")
**Problem**: Network blocking ICMP or UDP
**Solutions**:
1. Check firewall settings
2. Try increasing timeout: `-timeout 5s`
3. Reduce hops for local network: `-max-hops 10`
4. Try external mode instead

### Scan is very slow
**Solutions**:
1. Increase workers: `-workers 50`
2. Reduce timeout: `-timeout 1s`
3. Reduce max hops: `-max-hops 15`
4. Use raw mode (faster than external)

## Real-World Examples

### Example 1: Map Your Home Network
```bash
# Discover all hops to your router and beyond
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -max-hops 20 -output home_network.jsonl
```

### Example 2: Check Internet Connectivity
```bash
# Trace to common DNS servers
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw -output google_dns.jsonl
sudo ./traceroute-scanner -range 1.1.1.1 -mode raw -output cloudflare_dns.jsonl
```

### Example 3: Survey Network Infrastructure
```bash
# Scan office subnet to map network topology
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -workers 50 -shuffle=true -output office_network.jsonl
```

### Example 4: Monitor Network Changes
```bash
# Scan daily and compare results to detect routing changes
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw -output results_$(date +%Y%m%d).jsonl
```

## Files Reference

### Binaries
- **traceroute-scanner** - Main application
- **debug-traceroute-udp** - Quick test tool
- **debug-traceroute-udp-verbose** - Detailed debugging

### Documentation
- **README.md** - Comprehensive documentation
- **QUICK_START_GUIDE.md** - This file
- **SUCCESS_SUMMARY.md** - Test results and success story
- **UDP_TRACEROUTE_IMPLEMENTATION.md** - Technical details
- **VM_NAT_LIMITATION.md** - NAT issue and UDP solution

### Scripts
- **test_udp_mode.sh** - Automated testing script

## Next Steps

1. **Test it works:**
   ```bash
   sudo ./debug-traceroute-udp -ip 8.8.8.8 -hops 30
   ```

2. **Run a real scan:**
   ```bash
   sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -workers 20
   ```

3. **Analyze results:**
   ```bash
   cat traceroute_results.jsonl | jq '.'
   ```

4. **Scale up:**
   ```bash
   sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 50 -shuffle=true
   ```

## Support

If you encounter issues:

1. **Check raw mode works:**
   ```bash
   sudo ./debug-traceroute-udp -ip 8.8.8.8
   ```

2. **Try external mode:**
   ```bash
   ./traceroute-scanner -range 8.8.8.8 -mode external
   ```

3. **Run verbose debug:**
   ```bash
   sudo ./debug-traceroute-udp-verbose -ip 8.8.8.8 -hops 5
   ```

4. **Check documentation:**
   - README.md - Full documentation
   - SUCCESS_SUMMARY.md - Confirmed working results
   - UDP_TRACEROUTE_IMPLEMENTATION.md - Technical details

## Summary

✅ **Working Features:**
- Raw UDP mode works through NAT
- External mode works everywhere
- IP shuffling for stealth
- Concurrent scanning with workers
- Multiple IP range formats
- JSONL output for easy parsing
- Comprehensive error handling
- Detailed validation

🎯 **Ready to use!** Your traceroute scanner is fully functional.

Start with:
```bash
sudo ./debug-traceroute-udp -ip 8.8.8.8 -hops 30
```

Then scale up:
```bash
sudo ./traceroute-scanner -range YOUR_NETWORK/24 -mode raw -workers 20
```

Happy scanning! 🚀
