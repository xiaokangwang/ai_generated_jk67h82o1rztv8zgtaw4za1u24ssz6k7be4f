# Traceroute Scanner

A high-performance Go application that performs traceroute and ICMP ping scans on IPv4 addresses with multiple operational modes.

## Features

- **Multiple Scanning Modes**:
  - **Raw Socket Mode**: Direct UDP implementation using Linux system calls (requires root, works through NAT)
  - **External Mode**: Uses system traceroute binary (no root required)
  - **Ping-Only Mode**: Fast ICMP ping for host discovery (10-100x faster than traceroute)
- **Fast Sampling**: One-per-24 option to scan only one IP per /24 block (256x reduction in scan size)
- **Streaming Mode**: Memory-efficient scanning for unlimited IP ranges (constant memory usage regardless of range size)
- **Advanced IP Shuffling**:
  - Uses ZMap's Blackrock cipher for cryptographically secure pseudo-random IP ordering
  - Deterministic and reproducible (same seed = same scan order)
  - Zero memory overhead - IPs generated on-demand
  - Enabled by default for stealth
- **Resumable Scans**: Automatic checkpoint saving, resume interrupted scans from where they left off
- **Concurrent Scanning**: Configurable worker pool for parallel operations
- **Flexible IP Ranges**: Supports single IPs, CIDR notation, and IP ranges (any size)
- **JSONL Output**: Results saved in JSON Lines format for easy processing
- **Progress Tracking**: Real-time progress updates with percentage completion
- **Configurable Parameters**: Control workers, max hops, timeouts, and shuffle seeds

## Requirements

- Linux operating system
- Go 1.21 or later
- **For Raw Mode**: Root/sudo privileges
- **For External Mode**: `traceroute` command installed (usually pre-installed)

## Installation

```bash
# Clone or create the project directory
cd /home/claude-3/workdir2

# Download dependencies
go mod download

# Build the binary
go build -o traceroute-scanner
```

## Usage

The program can run in two modes with optional streaming:

### External Mode (Recommended - No Root Required)

Uses the system `traceroute` command:

```bash
# Standard mode (loads IPs into memory)
./traceroute-scanner -range <IP_RANGE> -mode external [OPTIONS]

# Streaming mode (constant memory, unlimited range size)
./traceroute-scanner -range <IP_RANGE> -streaming -mode external [OPTIONS]
```

### Raw Mode (Requires Root)

Uses raw ICMP sockets for direct packet control:

```bash
# Standard mode (loads IPs into memory)
sudo ./traceroute-scanner -range <IP_RANGE> -mode raw [OPTIONS]

# Streaming mode (constant memory, unlimited range size)
sudo ./traceroute-scanner -range <IP_RANGE> -streaming -mode raw [OPTIONS]
```

### Streaming vs Standard Mode

- **Standard Mode**: Loads all IPs into memory, then shuffles. Best for small ranges (<100K IPs).
- **Streaming Mode**: Generates IPs on-demand using Blackrock cipher. Best for large ranges (any size, even /8 networks).
  - Memory usage: O(1) - constant regardless of range size
  - Supports shuffle with deterministic ordering
  - Recommended for ranges larger than /16 (65K IPs)

### IP Range Formats

1. **Single IP**:
   ```bash
   ./traceroute-scanner -range 8.8.8.8 -mode external
   ```

2. **CIDR Notation**:
   ```bash
   ./traceroute-scanner -range 192.168.1.0/24 -mode external
   ```

3. **IP Range**:
   ```bash
   ./traceroute-scanner -range 10.0.0.1-10.0.0.10 -mode external
   ```

### Options

- `-range`: IP range to scan (required) - supports single IP, CIDR, or range notation
- `-mode`: Traceroute mode - `external` or `raw` (default: `external`)
- `-ping-only`: Use ICMP ping instead of traceroute (much faster, checks reachability only)
- `-one-per-24`: Sample only one IP per /24 block (reduces scan size by 256x)
- `-streaming`: Use streaming mode for memory-efficient scanning (recommended for large ranges)
- `-output`: Output file path (default: `traceroute_results.jsonl`)
- `-archive`: If previous scan is complete, archive it with timestamp and start fresh
- `-fresh`: Start a fresh scan, deleting any existing progress (default: auto-resume if progress exists)
- `-shuffle`: Shuffle IP order to avoid obvious scanning patterns (default: `true`)
- `-shuffle-seed`: Seed for shuffle permutation (0 = random, specific number = reproducible order)
- `-workers`: Number of concurrent workers (default: 10)
- `-max-hops`: Maximum number of hops (default: 30)
- `-timeout`: Timeout per hop (default: 3s)

### Examples

**Fast host discovery with ping-only mode:**
```bash
./traceroute-scanner -range 8.8.8.0/24 -ping-only -streaming
# 10-100x faster than traceroute, checks which hosts are reachable
```

**Sample one IP per /24 block (256x faster):**
```bash
./traceroute-scanner -range 1.0.0.0/16 -one-per-24 -streaming
# Scans 256 IPs instead of 65536 (samples 1.0.0.0, 1.0.1.0, 1.0.2.0, ...)
```

**Ultra-fast internet survey (ping + sampling):**
```bash
./traceroute-scanner -range 0.0.0.0/0 -ping-only -one-per-24 -streaming -workers 100
# Scans entire IPv4 space: 16.7M IPs instead of 4.3B (completes in hours, not months)
```

**Basic scan using external mode (no root):**
```bash
./traceroute-scanner -range 8.8.8.8 -mode external
```

**Scan a subnet with external mode:**
```bash
./traceroute-scanner -range 192.168.1.0/24 -mode external -workers 20 -max-hops 20 -timeout 2s
```

**Large range with streaming mode (memory-efficient):**
```bash
./traceroute-scanner -range 10.0.0.0/16 -streaming -workers 50
```

**Reproducible scan with specific seed:**
```bash
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle-seed=42
# Running again with seed=42 produces identical scan order
```

**Random shuffle (different order each run):**
```bash
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle
# Seed generated randomly, scan order differs each time
```

**Scan using raw ICMP mode (requires root):**
```bash
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw
```

**High-performance scan with multiple workers:**
```bash
./traceroute-scanner -range 10.0.0.0/24 -streaming -workers 50 -timeout 1s
```

**Scan range with custom output file:**
```bash
./traceroute-scanner -range 1.1.1.1-1.1.1.100 -streaming -output results.jsonl
```

**Quick test with routable IPs (fast results):**
```bash
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -shuffle-seed=42 -fresh
```

**Resume interrupted scan (automatic):**
```bash
# Start a scan
sudo ./traceroute-scanner -range 192.168.0.0/16 -mode raw -workers 50

# If interrupted (Ctrl+C, network issue, etc.), just run the same command again:
sudo ./traceroute-scanner -range 192.168.0.0/16 -mode raw -workers 50
# Automatically resumes from where it left off!
```

**Start fresh scan (delete existing progress):**
```bash
# Force a fresh start even if progress file exists
sudo ./traceroute-scanner -range 192.168.0.0/16 -mode raw -workers 50 -fresh
```

**Archive completed scan and start new one:**
```bash
# If previous scan is complete, archives it with timestamp
sudo ./traceroute-scanner -range 192.168.0.0/16 -mode raw -workers 50 -archive
# Creates: traceroute_results.jsonl.2026-01-12_18-30-45 (old scan)
# Creates: traceroute_results.jsonl (new scan)
```

### Stealth Scanning & IP Shuffling

By default, the scanner uses **ZMap's Blackrock cipher** to randomize IP addresses, avoiding sequential patterns that trigger IDS/IPS alerts.

**Shuffled scan (default - recommended for stealth):**
```bash
./traceroute-scanner -range 192.168.1.0/24 -streaming
# IPs scanned in pseudo-random order: 192.168.1.73, 192.168.1.12, 192.168.1.201, ...
```

**Reproducible scan (same seed = same order):**
```bash
./traceroute-scanner -range 192.168.1.0/24 -streaming -shuffle-seed=42
# Run again with seed=42 → identical scan order
# Useful for research, debugging, or distributed scanning
```

**Random shuffle each time:**
```bash
./traceroute-scanner -range 192.168.1.0/24 -streaming -shuffle
# Generates random seed, different order each run
```

**Sequential scan (disable shuffle - NOT recommended):**
```bash
./traceroute-scanner -range 192.168.1.0/24 -shuffle=false
# IPs scanned in order: 192.168.1.0, 192.168.1.1, 192.168.1.2, ...
# WARNING: Sequential scans are easily detected by security systems!
```

**Why shuffle?**
- Sequential scans (1.1.1.1, 1.1.1.2, 1.1.1.3...) create obvious patterns
- IDS/IPS systems flag sequential IP scanning as malicious behavior
- Randomized order makes scans less suspicious and harder to correlate
- **Blackrock cipher** provides cryptographically secure pseudo-random permutation
- Zero memory overhead - IPs generated on-demand in streaming mode
- Deterministic when using seeds - same seed always produces same order

**Use Cases for Seeds:**
- **Research**: Document your seed for reproducibility
- **Distributed Scanning**: Different machines use different seeds to avoid overlap
- **Testing**: Same seed ensures consistent test conditions
- **Random (seed=0)**: Maximum unpredictability for stealth

## Understanding the Output

Results are saved in **JSON Lines format** (`.jsonl`) - one JSON object per line. Each line represents the scan result for a single IP address.

### Output File Format

The output file contains one JSON object per line (not a JSON array). This format is:
- **Streamable**: Can process results as they're written
- **Appendable**: New results added without rewriting entire file
- **Parseable**: Easy to process with `jq`, `grep`, or any JSON parser

```bash
# View results with jq
cat results.jsonl | jq .

# Count reachable hosts
cat results.jsonl | jq 'select(.reached == true)' | wc -l

# Extract just IP addresses of reachable hosts
cat results.jsonl | jq -r 'select(.reached == true) | .dest_ip'

# Find hosts with >10 hops
cat results.jsonl | jq 'select(.hops | length > 10)'
```

### Field Reference

#### Top-Level Fields

- **`dest_ip`** (string): The target IP address that was scanned
- **`hops`** (array): Ordered list of network hops from source to destination (empty if unreachable)
- **`reached`** (boolean): `true` if destination was reached, `false` if unreachable or timed out
- **`timestamp`** (string): ISO 8601 timestamp when the scan started
- **`duration_ns`** (integer): Total scan duration in nanoseconds

#### Hop Fields (inside `hops` array)

- **`ttl`** (integer): Time-To-Live value SET in the outgoing packet (not from response)
  - In **traceroute mode**: Starts at 1, increments each hop (1, 2, 3, ...)
  - In **ping-only mode**: Fixed at 64 (standard ping TTL value)
- **`ip`** (string): IP address of the router/host at this hop (empty string "" if timeout)
- **`rtt_ns`** (integer): Round-trip time in nanoseconds (0 if timeout)
- **`timeout`** (boolean): `true` if this hop didn't respond within timeout period

### Example Outputs Explained

#### 1. Successful Traceroute (Reachable Host)

```json
{
  "dest_ip": "8.8.8.8",
  "hops": [
    {"ttl": 1, "ip": "192.168.1.1", "rtt_ns": 2500000, "timeout": false},
    {"ttl": 2, "ip": "10.0.0.1", "rtt_ns": 5000000, "timeout": false},
    {"ttl": 3, "ip": "172.16.0.1", "rtt_ns": 15000000, "timeout": false},
    {"ttl": 4, "ip": "8.8.8.8", "rtt_ns": 20000000, "timeout": false}
  ],
  "reached": true,
  "timestamp": "2026-01-12T10:30:00Z",
  "duration_ns": 85000000
}
```

**What this means:**
- Successfully traced route to Google DNS (8.8.8.8)
- Took 4 hops to reach destination
- **Hop 1** (2.5ms): Local gateway at 192.168.1.1
- **Hop 2** (5ms): Next router at 10.0.0.1
- **Hop 3** (15ms): ISP router at 172.16.0.1
- **Hop 4** (20ms): Reached destination 8.8.8.8
- Total scan time: 85ms

#### 2. Traceroute with Timeouts (Hidden Hops)

```json
{
  "dest_ip": "1.1.1.1",
  "hops": [
    {"ttl": 1, "ip": "192.168.1.1", "rtt_ns": 2000000, "timeout": false},
    {"ttl": 2, "ip": "", "rtt_ns": 0, "timeout": true},
    {"ttl": 3, "ip": "", "rtt_ns": 0, "timeout": true},
    {"ttl": 4, "ip": "1.1.1.1", "rtt_ns": 25000000, "timeout": false}
  ],
  "reached": true,
  "timestamp": "2026-01-12T10:31:00Z",
  "duration_ns": 9250000000
}
```

**What this means:**
- Successfully reached Cloudflare DNS (1.1.1.1)
- **Hop 1** (2ms): Local gateway responded
- **Hops 2-3**: Routers didn't respond to ICMP (common security practice)
  - `"ip": ""` = no response
  - `"timeout": true` = waited full timeout period
- **Hop 4** (25ms): Destination responded
- Total scan time: 9.25 seconds (mostly waiting for timeouts on hops 2-3)

#### 3. Unreachable Host (Traceroute Mode)

```json
{
  "dest_ip": "192.0.2.1",
  "hops": [
    {"ttl": 1, "ip": "192.168.1.1", "rtt_ns": 2500000, "timeout": false},
    {"ttl": 2, "ip": "", "rtt_ns": 0, "timeout": true},
    {"ttl": 3, "ip": "", "rtt_ns": 0, "timeout": true},
    {"ttl": 4, "ip": "", "rtt_ns": 0, "timeout": true}
  ],
  "reached": false,
  "timestamp": "2026-01-12T10:32:00Z",
  "duration_ns": 95000000000
}
```

**What this means:**
- Could NOT reach 192.0.2.1 (documentation IP, not routable)
- **Hop 1**: Local gateway responded
- **Hops 2-4**: No responses (shows first 4 failed hops, continues up to `-max-hops`)
- `"reached": false` = never got response from destination
- Note: Scans of unreachable IPs take full timeout duration × max hops

#### 4. Completely Unreachable (No Route)

```json
{
  "dest_ip": "10.0.0.1",
  "hops": [],
  "reached": false,
  "timestamp": "2026-01-12T10:33:00Z",
  "duration_ns": 90000000000
}
```

**What this means:**
- Destination completely unreachable (no route, possibly local IP on non-local network)
- Empty `hops` array = not even first hop responded
- Scan waited full timeout period for each hop attempt

#### 5. Ping-Only Mode (Reachable)

```json
{
  "dest_ip": "8.8.8.8",
  "hops": [
    {"ttl": 64, "ip": "8.8.8.8", "rtt_ns": 12500000, "timeout": false}
  ],
  "reached": true,
  "timestamp": "2026-01-12T21:32:22Z",
  "duration_ns": 13716204
}
```

**What this means:**
- **Ping-only mode** (not traceroute)
- Host is reachable (responded to ping)
- **Single hop entry**: Represents the destination response
  - `"ttl": 64` = TTL we SET in outgoing ping packet (not hop count)
  - `"rtt_ns": 12500000` = 12.5ms round-trip time
- Much faster: 13ms total vs 85ms+ for full traceroute

#### 6. Ping-Only Mode (Unreachable)

```json
{
  "dest_ip": "192.0.2.1",
  "hops": [],
  "reached": false,
  "timestamp": "2026-01-12T21:32:32Z",
  "duration_ns": 3001236372
}
```

**What this means:**
- Host did not respond to ping
- Empty `hops` array in ping mode = no response
- Waited full timeout (3 seconds in this case)

### Interpreting TTL Values

⚠️ **Important**: The `ttl` field has different meanings depending on the mode:

**In Traceroute Mode:**
- `ttl` = hop number (1st hop, 2nd hop, 3rd hop, etc.)
- Starts at 1, increments for each hop
- Represents how many routers the packet can pass through
- Example: `"ttl": 3` means "third router in the path"

**In Ping-Only Mode:**
- `ttl` = Time-To-Live value SET in outgoing ping packet (fixed at 64)
- Does NOT represent hop count
- Standard value to ensure packet reaches destination
- Example: `"ttl": 64` means "we sent ping with TTL=64"

### Common Patterns

**Pattern 1: Fast Local Host**
```json
{"dest_ip": "192.168.1.100", "hops": [{"ttl": 1, "ip": "192.168.1.100", ...}], "reached": true}
```
→ Host is on local network (1 hop = direct connection)

**Pattern 2: Internet Host**
```json
{"dest_ip": "8.8.8.8", "hops": [{...}, {...}, {...}, {"ttl": 4, ...}], "reached": true}
```
→ Host is 4 hops away through routers

**Pattern 3: Firewall Blocking**
```json
{"hops": [{"ttl": 1, ...}, {"ttl": 2, "ip": "", "timeout": true}, ...], "reached": false}
```
→ First hop works, then firewalls block ICMP Time Exceeded messages

**Pattern 4: No Route**
```json
{"hops": [], "reached": false}
```
→ No routing path exists (IP not reachable from your network)

### Processing Large Result Files

**Count hosts by reachability:**
```bash
echo "Reachable: $(grep -c '"reached":true' results.jsonl)"
echo "Unreachable: $(grep -c '"reached":false' results.jsonl)"
```

**Extract reachable IPs to text file:**
```bash
cat results.jsonl | jq -r 'select(.reached == true) | .dest_ip' > reachable_ips.txt
```

**Calculate average RTT for reachable hosts:**
```bash
cat results.jsonl | jq -r 'select(.reached == true) | .hops[-1].rtt_ns' | awk '{sum+=$1; count++} END {print sum/count/1000000 " ms"}'
```

**Find hosts with exactly N hops:**
```bash
cat results.jsonl | jq 'select(.hops | length == 5)' > five_hop_hosts.jsonl
```

**Convert to CSV for Excel:**
```bash
echo "IP,Reached,Hops,Duration_ms" > results.csv
cat results.jsonl | jq -r '[.dest_ip, .reached, (.hops | length), (.duration_ns / 1000000)] | @csv' >> results.csv
```

## How It Works

### Streaming Mode with Blackrock Shuffle
1. **IP Generation**: Uses Blackrock cipher (ZMap's algorithm) for on-demand IP generation
2. **Feistel Network**: AES-based Feistel network creates bijective (one-to-one) mapping
3. **Format-Preserving Encryption**: Encrypts indices to IP addresses while maintaining range
4. **Memory Efficiency**: O(1) memory - generates IPs as needed, no storage
5. **Deterministic**: Same seed always produces identical scan order
6. **Performance**: ~321 nanoseconds per IP permutation (negligible overhead)

### Raw Mode
1. **Raw Socket Creation**: Creates UDP and ICMP raw sockets using `syscall.Socket()`
2. **TTL Incrementation**: Sends UDP packets to high ports (33434+) with increasing TTL values
3. **Response Parsing**: Receives and parses ICMP Time Exceeded and Destination Unreachable messages
4. **Packet Validation**: Verifies ICMP responses match sent UDP packets by checking embedded protocol and port
5. **Concurrent Execution**: Uses a worker pool to scan multiple IPs in parallel
6. **Result Recording**: Writes results to JSONL file as they complete

### External Mode
1. **Binary Execution**: Spawns system `traceroute` command for each target
2. **Output Capture**: Captures and parses traceroute command output
3. **Data Extraction**: Parses hop information (TTL, IP, RTT) from text output
4. **Concurrent Execution**: Uses a worker pool to scan multiple IPs in parallel
5. **Result Recording**: Writes normalized results to JSONL file

### Checkpoint & Resume
1. **Progress Tracking**: Completed IP ranges saved to `.progress` file
2. **Automatic Resume**: On restart, loads progress and skips completed IPs
3. **Range-Based**: Tracks contiguous ranges for efficiency
4. **Atomic Updates**: Progress saved after each completion

## Implementation Details

### Streaming Mode & Blackrock Cipher
- **Algorithm**: Based on ZMap's Blackrock cipher (format-preserving encryption)
- **Feistel Network**: 3-round Feistel cipher with AES round function
- **Bit-Space Optimization**: Works in minimum required bits (e.g., 16 bits for /16)
- **Cycle-Walking**: Re-encrypts values outside range until valid (with safety limit)
- **Bijection Guarantee**: One-to-one mapping ensures each IP scanned exactly once
- **Memory**: O(1) constant memory regardless of range size
- **Performance**: ~321ns per permutation, 20,000 IPs generated in 8ms
- **Thread-Safe**: Multiple workers can consume from channel concurrently

### Raw Mode Implementation
- Uses `syscall` package for raw socket operations
- Sends UDP packets to incrementing high ports (33434, 33435, etc.)
- Creates separate sockets for sending (UDP) and receiving (ICMP)
- Sets TTL using `setsockopt` on UDP socket
- Validates ICMP responses by checking embedded UDP packet protocol and port
- Handles IP header parsing to extract source addresses
- Works through NAT unlike raw ICMP (NAT gateways don't rewrite UDP traceroute packets the same way)
- Direct control over packet timing and structure

### External Mode Implementation
- Executes `/usr/sbin/traceroute` with appropriate flags
- Parses standard traceroute output format
- Handles timeouts and asterisk responses
- Converts RTT from milliseconds to nanoseconds for consistency
- No root privileges required

### Common Features
- Thread-safe result collection and writing
- Configurable worker pools for concurrent scanning
- Unified JSONL output format for both modes
- Range-based checkpoint tracking for efficient resume

## Security Considerations

- **Raw mode** requires root privileges for raw socket access
- **Raw mode**: Uses UDP-based traceroute which works through NAT (unlike raw ICMP)
- **IP Shuffling**: Enabled by default to avoid sequential scanning patterns that trigger IDS/IPS alerts
- Be respectful when scanning: many networks have IDS/IPS systems
- Some destinations may block ICMP traffic
- Consider rate limiting for large scans to avoid network congestion
- **External mode** is safer as it runs without elevated privileges (recommended)
- Always obtain proper authorization before scanning networks you don't own

## Limitations

- IPv4 only (IPv6 not supported)
- Linux only (uses Linux-specific syscalls for raw mode)
- ICMP may be blocked by firewalls
- Some routers don't respond to ICMP Time Exceeded messages
- External mode depends on system traceroute binary being installed
- Raw mode requires root privileges for raw socket access
- Large scans of private/unreachable IPs can take very long due to timeouts
  - Solution: Use shorter timeouts (`-timeout 1s -max-hops 10`) for private networks
  - Test with routable IPs first to verify functionality

## Performance Tips

1. **Use Streaming Mode for Large Ranges**: Constant memory usage, supports unlimited ranges
   ```bash
   ./traceroute-scanner -range 10.0.0.0/16 -streaming -workers 50
   # Works for /8, /16, any size - memory usage stays constant
   ```

2. **Use External Mode for Convenience**: No root required, generally sufficient for most uses
   ```bash
   ./traceroute-scanner -range 10.0.0.0/24 -streaming -workers 50
   ```

3. **Adjust Workers**: More workers = faster scanning, but may overwhelm network
   ```bash
   ./traceroute-scanner -range 10.0.0.0/24 -streaming -workers 100
   ```

4. **Reduce Timeout**: Shorter timeouts speed up scans of unresponsive hosts
   ```bash
   ./traceroute-scanner -range 10.0.0.0/24 -streaming -timeout 1s
   # For local networks: -timeout 500ms -max-hops 5
   ```

5. **Limit Hops**: Reduce max hops for nearby networks
   ```bash
   ./traceroute-scanner -range 192.168.0.0/24 -streaming -max-hops 15
   ```

6. **Use Raw Mode for Performance**: When you need maximum control and speed (requires root)
   ```bash
   sudo ./traceroute-scanner -range 10.0.0.0/24 -streaming -mode raw -workers 100
   ```

7. **Test with Routable IPs First**: Verify setup before large scans
   ```bash
   ./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -fresh
   # Completes in seconds, confirms everything works
   ```

## Troubleshooting

**Scanner appears stuck with no progress**
- **Problem**: Scanning private IP ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16) that don't route on the internet
- **Symptom**: No output after "Starting IP submission..." and first few IPs
- **Cause**: Each unreachable IP waits for full timeout (up to 180 seconds with default settings)
- **Solution 1**: Test with routable IPs first: `./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -fresh`
- **Solution 2**: For private IPs, use shorter timeouts: `-timeout 1s -max-hops 10` (reduces wait to ~20s per IP)
- **Note**: Shuffle feature is working correctly - this is expected behavior for unreachable IPs

**"failed to create raw socket: operation not permitted"**
- You're using raw mode without sudo
- Solution: Run with sudo `sudo ./traceroute-scanner ... -mode raw` OR
- Switch to external mode: `./traceroute-scanner ... -mode external`

**"External traceroute mode requires 'traceroute' command to be installed"**
- Install traceroute: `sudo apt-get install traceroute` (Debian/Ubuntu) or `sudo yum install traceroute` (RHEL/CentOS)
- Alternatively, use raw mode with sudo

**"invalid IP range format"**
- Check your IP range syntax
- Ensure IPs are valid IPv4 addresses

**Raw mode shows timeouts but external mode works**
- UDP traceroute should work through NAT
- Verify you're using the latest version (UDP-based, not ICMP)
- Check: `./debug-traceroute-udp -ip 8.8.8.8` should show multiple hops
- If still not working, use external mode

**Very slow scanning**
- Increase number of workers: `-workers 50`
- Decrease timeout value: `-timeout 1s`
- Reduce max-hops if scanning local networks: `-max-hops 15`
- Use streaming mode for large ranges: `-streaming`
- Raw mode has lower overhead than external mode

**Want to verify shuffle is working?**
```bash
# Test with routable IPs (completes quickly)
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -shuffle-seed=42 -fresh

# Run again - should see identical scan order
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -shuffle-seed=42 -fresh
```

## Additional Documentation

For more detailed information about specific features:

- **BLACKROCK_SHUFFLE.md** - Deep dive into the Blackrock cipher algorithm, how it works, performance characteristics, and comparison with ZMap
- **SHUFFLE_USAGE_GUIDE.md** - Comprehensive guide with examples for every use case: testing, research, stealth scanning, distributed scanning, and more
- **SHUFFLE_DIAGNOSIS.md** - Troubleshooting guide explaining the "scanner appears stuck" issue with private IP ranges
- **ONE_PER_24_FEATURE.md** - Documentation for the one-per-24 sampling feature (256x scan size reduction)
- **PING_MODE_FEATURE.md** - Documentation for ping-only mode (10-100x faster than traceroute)
- **test_shuffle_live.sh** - Automated test script to verify shuffle functionality with routable IPs

## Testing

Run the test suite:
```bash
go test -v
```

Run shuffle-specific tests:
```bash
go test -v -run Shuffle
go test -v -run Blackrock
```

Quick verification with real IPs:
```bash
./test_shuffle_live.sh
```

All tests (116 total) should pass, including:
- 12 Blackrock cipher tests (bijection, collision detection, edge cases)
- 9 IP generator tests (shuffle, one-per-24, exclusion ranges, large ranges)
- 5 Ping tests (ping functionality, TTL parsing, reachability)
- 18 Checkpoint and resume functionality tests
- 13 Worker pool tests (raw and external traceroute modes)
- External traceroute parsing tests

## License

This project is provided as-is for educational and authorized network testing purposes.
