# Traceroute Scanner

A high-performance Go application that performs traceroute to IPv4 addresses with two modes: raw UDP sockets or external traceroute binary.

## Features

- **Dual Mode Operation**:
  - **Raw Socket Mode**: Direct UDP implementation using Linux system calls (requires root, works through NAT)
  - **External Mode**: Uses system traceroute binary (no root required)
- **IP Shuffling**: Randomizes scan order to avoid obvious patterns and evade detection (enabled by default)
- **Concurrent Scanning**: Configurable worker pool for parallel traceroutes
- **Flexible IP Ranges**: Supports single IPs, CIDR notation, and IP ranges
- **JSONL Output**: Results saved in JSON Lines format for easy processing
- **Configurable Parameters**: Control workers, max hops, and timeouts

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

The program can run in two modes:

### External Mode (Recommended - No Root Required)

Uses the system `traceroute` command:

```bash
./traceroute-scanner -range <IP_RANGE> -mode external [OPTIONS]
```

### Raw Mode (Requires Root)

Uses raw ICMP sockets for direct packet control:

```bash
sudo ./traceroute-scanner -range <IP_RANGE> -mode raw [OPTIONS]
```

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

- `-range`: IP range to scan (required)
- `-mode`: Traceroute mode - `external` or `raw` (default: `external`)
- `-shuffle`: Shuffle IP order to avoid obvious scanning patterns (default: `true`)
- `-output`: Output file path (default: `traceroute_results.jsonl`)
- `-workers`: Number of concurrent workers (default: 10)
- `-max-hops`: Maximum number of hops (default: 30)
- `-timeout`: Timeout per hop (default: 3s)

### Examples

**Basic scan using external mode (no root):**
```bash
./traceroute-scanner -range 8.8.8.8 -mode external
```

**Scan a subnet with external mode:**
```bash
./traceroute-scanner -range 192.168.1.0/24 -mode external -workers 20 -max-hops 20 -timeout 2s
```

**Scan using raw ICMP mode (requires root):**
```bash
sudo ./traceroute-scanner -range 8.8.8.8 -mode raw
```

**High-performance scan with multiple workers:**
```bash
./traceroute-scanner -range 10.0.0.0/24 -mode external -workers 50 -timeout 1s
```

**Scan range with custom output file:**
```bash
./traceroute-scanner -range 1.1.1.1-1.1.1.100 -mode external -output results.jsonl
```

### Stealth Scanning

By default, the scanner randomizes the order of IP addresses to avoid creating obvious sequential scanning patterns that are easily detected by IDS/IPS systems.

**Shuffled scan (default - recommended for stealth):**
```bash
./traceroute-scanner -range 192.168.1.0/24 -mode external
# IPs scanned in random order: 192.168.1.73, 192.168.1.12, 192.168.1.201, ...
```

**Sequential scan (disable shuffle):**
```bash
./traceroute-scanner -range 192.168.1.0/24 -mode external -shuffle=false
# IPs scanned in order: 192.168.1.0, 192.168.1.1, 192.168.1.2, ...
# WARNING: Sequential scans are easily detected by security systems!
```

**Why shuffle?**
- Sequential scans (1.1.1.1, 1.1.1.2, 1.1.1.3...) create obvious patterns
- IDS/IPS systems flag sequential IP scanning as malicious behavior
- Randomized order makes scans less suspicious and harder to correlate
- Uses cryptographically secure randomization (crypto/rand)

## Output Format

Results are saved in JSON Lines format (one JSON object per line). Each line contains:

```json
{
  "dest_ip": "8.8.8.8",
  "hops": [
    {
      "ttl": 1,
      "ip": "192.168.1.1",
      "rtt_ns": 1234567,
      "timeout": false
    },
    {
      "ttl": 2,
      "ip": "10.0.0.1",
      "rtt_ns": 2345678,
      "timeout": false
    }
  ],
  "reached": true,
  "timestamp": "2026-01-12T10:30:00Z",
  "duration_ns": 3456789
}
```

### Fields

- `dest_ip`: Target IP address
- `hops`: Array of hop information
  - `ttl`: Time To Live value
  - `ip`: IP address of the hop (empty if timeout)
  - `rtt_ns`: Round-trip time in nanoseconds
  - `timeout`: Whether the hop timed out
- `reached`: Whether the destination was reached
- `timestamp`: When the traceroute started
- `duration_ns`: Total duration in nanoseconds

## How It Works

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

## Implementation Details

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
- Large ranges (>1M IPs) have safety limit in range parser
- External mode depends on system traceroute binary being installed
- Raw mode requires root privileges for raw socket access

## Performance Tips

1. **Use External Mode for Convenience**: No root required, generally sufficient for most uses
   ```bash
   ./traceroute-scanner -range 10.0.0.0/24 -mode external -workers 50
   ```

2. **Adjust Workers**: More workers = faster scanning, but may overwhelm network
   ```bash
   ./traceroute-scanner -range 10.0.0.0/24 -mode external -workers 50
   ```

3. **Reduce Timeout**: Shorter timeouts speed up scans of unresponsive hosts
   ```bash
   ./traceroute-scanner -range 10.0.0.0/24 -mode external -timeout 1s
   ```

4. **Limit Hops**: Reduce max hops for nearby networks
   ```bash
   ./traceroute-scanner -range 192.168.0.0/24 -mode external -max-hops 15
   ```

5. **Use Raw Mode for Performance**: When you need maximum control and speed (requires root)
   ```bash
   sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -workers 100
   ```

## Troubleshooting

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
- Raw mode has lower overhead than external mode

## License

This project is provided as-is for educational and authorized network testing purposes.
