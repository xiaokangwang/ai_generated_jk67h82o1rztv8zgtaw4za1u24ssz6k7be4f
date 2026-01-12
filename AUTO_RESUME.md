# Auto-Resume: Smart Default Behavior

## Overview

The scanner now **automatically resumes** interrupted scans by default. No flags needed!

## How It Works

### Automatic Resume (Default)

Simply run the same command twice:

```bash
# First run: Starts scan
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw

# Press Ctrl+C after some progress

# Second run: Automatically resumes!
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw
```

**Output on second run:**
```
Found existing progress file, resuming scan...
Loaded checkpoint: 45/256 IPs already scanned (17.6%)
Resuming scan: 211/256 IPs remaining

Mode: raw, Workers: 10, Max Hops: 30, Timeout: 3s
Output: traceroute_results.jsonl
Progress: traceroute_results.jsonl.progress

Progress: 50/256 (19.5%)
...
```

**No special flags needed!** The scanner automatically detects the progress file and resumes.

### Force Fresh Start

If you want to start over, use `-fresh`:

```bash
# Deletes old progress and starts from scratch
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -fresh
```

**Output:**
```
Deleting existing progress file: traceroute_results.jsonl.progress
Deleting existing output file: traceroute_results.jsonl
Starting traceroute scan for 256 IP addresses (shuffled)...
```

## Behavior Summary

| Scenario | Command | Behavior |
|----------|---------|----------|
| **First run (no progress)** | `./traceroute-scanner -range X` | Starts new scan |
| **Second run (has progress)** | `./traceroute-scanner -range X` | Auto-resumes ✨ |
| **Already complete** | `./traceroute-scanner -range X` | "All IPs scanned" message |
| **Force fresh** | `./traceroute-scanner -range X -fresh` | Deletes progress, starts over |

## Examples

### Example 1: Normal Workflow

```bash
# Start scan (gets 30% done)
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw

# Interrupted! No problem...

# Just run again - auto-resumes at 30%
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw

# Completes the remaining 70%
```

### Example 2: Large Scan Over Days

```bash
# Monday: Start big scan
sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 100

# End of day: Ctrl+C (10% done)

# Tuesday: Resume automatically
sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 100

# End of day: Ctrl+C (45% done)

# Wednesday: Resume again
sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 100

# Completes!
```

### Example 3: Check If Complete

```bash
# Run command
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw

# If already done:
# "All IPs already scanned! Nothing to do."
# "Results are in: traceroute_results.jsonl"
# "To start a fresh scan, use: -fresh flag"
```

### Example 4: Start Over

```bash
# Want to re-scan? Use -fresh
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -fresh

# Old progress deleted, starts from 0%
```

## Advantages of Auto-Resume

### Before (Manual Resume)

❌ Have to remember to add `-resume` flag
❌ Easy to forget and overwrite progress
❌ Requires understanding of resume concept

### After (Auto-Resume)

✅ Just run the same command
✅ Never lose progress accidentally
✅ Intuitive - "it just works"

## Technical Details

### Progress Detection

On startup, the scanner checks for `<output_file>.progress`:

```go
if progressFileExists && !freshFlag {
    // Auto-resume
    loadCheckpoint()
    filterCompletedIPs()
    appendToOutput()
} else if freshFlag {
    // Delete old files, start fresh
    deleteCheckpoint()
    deleteOutput()
    createNew()
} else {
    // No progress file, start new
    createNew()
}
```

### File Management

**Auto-resume mode:**
- Loads `.progress` file
- Opens output file in append mode
- Continues where it left off

**Fresh mode (-fresh flag):**
- Deletes `.progress` file
- Deletes output file
- Creates new files

## Migration from Old Behavior

### Old Behavior (Before)

```bash
# Start scan
./traceroute-scanner -range 10.0.0.0/24 -mode external

# Interrupted...

# Resume required explicit flag
./traceroute-scanner -range 10.0.0.0/24 -mode external -resume
```

### New Behavior (Now)

```bash
# Start scan
./traceroute-scanner -range 10.0.0.0/24 -mode external

# Interrupted...

# Just run again - auto-resumes!
./traceroute-scanner -range 10.0.0.0/24 -mode external
```

**Much simpler!**

## Common Scenarios

### Scenario 1: Scan Gets Stuck

```bash
# Scan seems stuck, want to restart with more workers
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -workers 10

# Kill it

# Restart with more workers - resumes automatically
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -workers 50
```

Progress is preserved, just runs faster now!

### Scenario 2: Need to Free Resources

```bash
# Scan running with 100 workers
sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 100

# Need CPU for something else - kill it

# Resume later with fewer workers
sudo ./traceroute-scanner -range 10.0.0.0/16 -mode raw -workers 10
```

### Scenario 3: Testing Different Timeouts

```bash
# Start with 3s timeout (default)
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw

# Too slow - kill it

# Resume with faster timeout
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -timeout 1s
```

### Scenario 4: Truly Starting Over

```bash
# Previous scan has bad data
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -fresh

# Clean slate!
```

## Best Practices

### 1. Let It Resume By Default

```bash
# ✓ Good: Just repeat the command
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw
# Auto-resumes if interrupted

# ✗ Bad: Don't use -fresh unless you mean it
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -fresh
# Loses all progress!
```

### 2. Use Same Output Filename

```bash
# ✓ Good: Consistent output name
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -output scan.jsonl
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -output scan.jsonl

# ✗ Bad: Changing output name breaks resume
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -output scan1.jsonl
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -output scan2.jsonl
```

### 3. Check Progress Before Fresh

```bash
# Check if scan is complete
cat scan.jsonl.progress | jq '.completed | length'

# If not complete, let it resume
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw

# Only use -fresh if you really want to restart
```

### 4. Keep Progress Files

```bash
# DON'T manually delete .progress files unless you want to start over
rm scan.jsonl.progress  # ⚠️  Loses progress!

# Let the scanner manage them automatically
```

## Troubleshooting

### "All IPs already scanned! Nothing to do."

**Meaning**: Scan completed successfully!

**What to do**:
- Results are in your output file
- Progress file will be auto-deleted
- To re-scan: use `-fresh` flag

### Scan keeps resuming but I want fresh start

**Solution**: Use `-fresh` flag

```bash
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -fresh
```

### Progress file from different IP range

**Problem**: Progress file is for 192.168.1.0/24 but now scanning 192.168.2.0/24

**Solution**: Use `-fresh` or change output filename

```bash
# Option 1: Fresh start (same filename)
sudo ./traceroute-scanner -range 192.168.2.0/24 -mode raw -fresh

# Option 2: Different filename (preserves old scan)
sudo ./traceroute-scanner -range 192.168.2.0/24 -mode raw -output scan2.jsonl
```

## Summary

### Key Changes

✨ **Auto-resume is now default** - No flag needed
🔄 **Just run the same command** - It remembers where it was
🆕 **Use `-fresh` to start over** - Explicit opt-in for fresh start
🎯 **Simpler workflow** - Less to remember, harder to lose work

### What This Means

**Old way:**
```bash
./traceroute-scanner ...           # Start
./traceroute-scanner ... -resume   # Remember to add flag!
```

**New way:**
```bash
./traceroute-scanner ...   # Start
./traceroute-scanner ...   # Auto-resumes ✨
```

**To start fresh:**
```bash
./traceroute-scanner ... -fresh   # Explicit opt-in
```

### Philosophy

**"Do the safe thing by default"**

- Resuming = Safe (preserves work)
- Starting fresh = Dangerous (loses work)
- Default = Resume
- Fresh = Explicit flag

This matches user expectations:
- "Run command again" = Continue where I left off
- "I want to start over" = Add special flag

Your scanner is now even smarter and more user-friendly! 🚀

---

**Auto-resume makes large scans practical:**
- No mental overhead
- No lost work
- Just works™

Perfect for:
- 🌐 Large subnet scans
- ⏱️ Multi-day operations
- 📡 Unstable connections
- 🔄 Iterative scanning
- 💻 Resource management

**Just run the same command - we'll figure out the rest!** ✨
