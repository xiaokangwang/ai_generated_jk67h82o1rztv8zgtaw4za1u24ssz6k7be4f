# Auto-Archive Feature

## Overview

The scanner can **automatically archive** completed scans with timestamps, then start a fresh scan - perfect for periodic rescanning of the same IP ranges!

## How It Works

### The Problem

You've scanned a network and want to scan it again later:

```bash
# First scan (completed)
./traceroute-scanner -range 192.168.1.0/24 -mode external

# Later... want to scan again
./traceroute-scanner -range 192.168.1.0/24 -mode external
# Output: "All IPs already scanned! Nothing to do."
```

### The Solution: Archive Flag

```bash
# First scan (completed)
./traceroute-scanner -range 192.168.1.0/24 -mode external -output scan.jsonl

# Later... scan again with -archive
./traceroute-scanner -range 192.168.1.0/24 -mode external -output scan.jsonl -archive
```

**What happens:**
1. ✓ Detects previous scan is complete
2. ✓ Renames `scan.jsonl` → `scan.jsonl.2026-01-12_18-30-45`
3. ✓ Deletes progress file
4. ✓ Starts fresh scan, saves to `scan.jsonl`

## Usage

### Basic Archive

```bash
# Complete a scan
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw

# Days later... rescan with archive
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -archive
```

**Output:**
```
Checking if previous scan is complete...
Previous scan is complete! Archiving...
  Old file: traceroute_results.jsonl
  New file: traceroute_results.jsonl.2026-01-12_18-30-45
✓ Archived to: traceroute_results.jsonl.2026-01-12_18-30-45
✓ Deleted progress file

Starting fresh scan...
Starting traceroute scan for 256 IP addresses (shuffled)...
Mode: raw, Workers: 10, Max Hops: 30, Timeout: 3s
...
```

### Archive with Custom Output

```bash
# First scan
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -output network_scan.jsonl

# Rescan with archive
sudo ./traceroute-scanner -range 192.168.1.0/24 -mode raw -output network_scan.jsonl -archive

# Creates: network_scan.jsonl.2026-01-12_18-30-45
```

## Behavior Matrix

| Previous State | Command | Result |
|----------------|---------|--------|
| **No previous scan** | `... -archive` | Starts new scan |
| **Incomplete scan** | `... -archive` | Resumes scan (doesn't archive) |
| **Complete scan** | `... -archive` | Archives + starts fresh |
| **Complete scan** | `... (no flag)` | "Nothing to do" message |
| **Complete scan** | `... -fresh` | Deletes old + starts fresh |

## Archive vs Fresh vs Default

### Default Behavior (No Flag)

```bash
./traceroute-scanner -range 10.0.0.0/24 -mode external

# If scan complete: "All IPs already scanned! Nothing to do."
# If scan incomplete: Resumes automatically
```

### Fresh Flag (-fresh)

```bash
./traceroute-scanner -range 10.0.0.0/24 -mode external -fresh

# Deletes old results
# Starts new scan
# ⚠️ Old data is LOST
```

### Archive Flag (-archive)

```bash
./traceroute-scanner -range 10.0.0.0/24 -mode external -archive

# If complete: Archives old results with timestamp
# Starts new scan
# ✓ Old data is PRESERVED
```

## Use Cases

### 1. Periodic Network Monitoring

Scan the same network daily/weekly:

```bash
# Cron job - runs daily at 2am
0 2 * * * cd /scans && ./traceroute-scanner -range 10.0.0.0/16 -mode raw -archive

# Creates timestamped archives:
# traceroute_results.jsonl.2026-01-12_02-00-00
# traceroute_results.jsonl.2026-01-13_02-00-00
# traceroute_results.jsonl.2026-01-14_02-00-00
```

### 2. Change Detection

Compare network topology over time:

```bash
# Scan every Monday
./traceroute-scanner -range 192.168.0.0/16 -mode raw -output weekly_scan.jsonl -archive

# After several weeks, compare:
diff weekly_scan.jsonl.2026-01-06_* weekly_scan.jsonl.2026-01-13_*
```

### 3. Security Audits

Keep audit trail:

```bash
# Monthly security scan
./traceroute-scanner -range 10.0.0.0/8 -mode raw -output audit.jsonl -archive

# Archives show network changes over time
ls -lh audit.jsonl.*
# audit.jsonl.2026-01-01_00-00-00
# audit.jsonl.2026-02-01_00-00-00
# audit.jsonl.2026-03-01_00-00-00
```

### 4. Before/After Comparison

Test network changes:

```bash
# Scan before maintenance
./traceroute-scanner -range 192.168.1.0/24 -mode raw -output network.jsonl

# Do network maintenance...

# Scan after maintenance (archives before, scans again)
./traceroute-scanner -range 192.168.1.0/24 -mode raw -output network.jsonl -archive

# Compare
diff network.jsonl.2026-01-12_* network.jsonl
```

## Archive Filename Format

Archives use ISO 8601 timestamp format:

```
<original_filename>.<YYYY-MM-DD_HH-MM-SS>
```

**Examples:**
- `traceroute_results.jsonl.2026-01-12_18-30-45`
- `scan.jsonl.2026-01-12_14-25-10`
- `network_audit.jsonl.2026-12-31_23-59-59`

**Benefits:**
- ✓ Sortable (lexicographic order = chronological order)
- ✓ Human readable
- ✓ No conflicts (includes seconds)
- ✓ Easy to parse programmatically

## Handling Incomplete Scans

If you use `-archive` but the scan isn't complete, it **resumes** instead of archiving:

```bash
# Scan gets 50% done, then interrupted
./traceroute-scanner -range 10.0.0.0/24 -mode raw

# Try to archive
./traceroute-scanner -range 10.0.0.0/24 -mode raw -archive
```

**Output:**
```
Checking if previous scan is complete...
Previous scan not complete (128/256 IPs remaining)
Resuming scan...

Resuming scan: 128/256 IPs remaining
...
```

**Rationale:** You probably don't want to archive incomplete data. The scanner assumes you want to finish the scan first.

## Examples

### Example 1: Daily Monitoring

```bash
#!/bin/bash
# daily_scan.sh - Run daily via cron

OUTPUT="network_scan.jsonl"
RANGE="10.0.0.0/16"

# Archive previous scan (if complete) and start fresh
sudo ./traceroute-scanner \
    -range "$RANGE" \
    -mode raw \
    -output "$OUTPUT" \
    -archive \
    -workers 50

# Result: Timestamped archives accumulate
# network_scan.jsonl.2026-01-12_02-00-00
# network_scan.jsonl.2026-01-13_02-00-00
# etc.
```

### Example 2: Weekly Comparison

```bash
#!/bin/bash
# weekly_compare.sh

OUTPUT="weekly.jsonl"
RANGE="192.168.0.0/16"

# Scan with archive
sudo ./traceroute-scanner -range "$RANGE" -mode raw -output "$OUTPUT" -archive

# Find last two scans
ARCHIVES=$(ls -t ${OUTPUT}.* | head -2)
LATEST=$(echo "$ARCHIVES" | head -1)
PREVIOUS=$(echo "$ARCHIVES" | tail -1)

# Compare
echo "Comparing $PREVIOUS vs $LATEST"
diff <(jq -r '.dest_ip' "$PREVIOUS" | sort) \
     <(jq -r '.dest_ip' "$LATEST" | sort)
```

### Example 3: Retention Policy

```bash
#!/bin/bash
# cleanup_old_archives.sh

OUTPUT="scan.jsonl"

# Archive and scan
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -output "$OUTPUT" -archive

# Keep only last 30 days of archives
find . -name "${OUTPUT}.*" -type f -mtime +30 -delete

echo "Cleaned up archives older than 30 days"
```

### Example 4: Multiple Networks

```bash
#!/bin/bash
# scan_all_networks.sh

NETWORKS=(
    "10.0.0.0/16:internal"
    "192.168.0.0/16:office"
    "172.16.0.0/12:dmz"
)

for NET in "${NETWORKS[@]}"; do
    RANGE="${NET%%:*}"
    NAME="${NET##*:}"
    OUTPUT="${NAME}_scan.jsonl"

    echo "Scanning $NAME ($RANGE)..."
    sudo ./traceroute-scanner \
        -range "$RANGE" \
        -mode raw \
        -output "$OUTPUT" \
        -archive \
        -workers 50
done

echo "All networks scanned and archived!"
```

## Advanced: Processing Archives

### Count Total Scans

```bash
# How many times have we scanned?
ls scan.jsonl.* | wc -l
```

### Find Latest Archive

```bash
# Get most recent archive
LATEST=$(ls -t scan.jsonl.* | head -1)
echo "Latest: $LATEST"
```

### Compare All Archives

```bash
# Show IPs that appeared in latest but not in previous
LATEST=$(ls -t scan.jsonl.* | head -1)
PREVIOUS=$(ls -t scan.jsonl.* | head -2 | tail -1)

NEW_IPS=$(comm -13 \
    <(jq -r '.dest_ip' "$PREVIOUS" | sort) \
    <(jq -r '.dest_ip' "$LATEST" | sort))

echo "New IPs detected: $NEW_IPS"
```

### Extract Hop Changes

```bash
# Find IPs where hop count changed
for ARCHIVE in scan.jsonl.*; do
    echo "=== $ARCHIVE ==="
    jq -r '"\(.dest_ip): \(.hops | length) hops"' "$ARCHIVE"
done
```

## Combining with Other Features

### Archive + Shuffling

```bash
# Shuffled scan with archive
sudo ./traceroute-scanner \
    -range 10.0.0.0/24 \
    -mode raw \
    -archive \
    -shuffle=true
```

### Archive + Custom Workers

```bash
# Fast scan with archive
sudo ./traceroute-scanner \
    -range 192.168.0.0/16 \
    -mode raw \
    -archive \
    -workers 100 \
    -timeout 1s
```

### Archive + External Mode

```bash
# No root required
./traceroute-scanner \
    -range 10.0.0.0/24 \
    -mode external \
    -archive
```

## Troubleshooting

### "Previous scan not complete"

**Situation:** Used `-archive` but scan isn't done

**Behavior:** Resumes instead of archiving

**Solution:** Let it finish, then archive will work next time

```bash
# Let it complete first
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw

# Then archive will work
sudo ./traceroute-scanner -range 10.0.0.0/24 -mode raw -archive
```

### Want to Archive Incomplete Scan

**Problem:** Need to archive even though scan isn't complete

**Solution:** Use `-fresh` or manually rename

```bash
# Option 1: Manual rename
mv scan.jsonl scan.jsonl.incomplete.$(date +%Y-%m-%d_%H-%M-%S)
rm scan.jsonl.progress
./traceroute-scanner -range 10.0.0.0/24 -mode raw

# Option 2: Use -fresh (loses data)
./traceroute-scanner -range 10.0.0.0/24 -mode raw -fresh
```

### Archive Without Progress File

**Situation:** No `.progress` file exists

**Behavior:** Starts new scan (nothing to archive)

```bash
./traceroute-scanner -range 10.0.0.0/24 -mode raw -archive
# Output: "No previous scan found, starting new scan..."
```

## Best Practices

### 1. Use Consistent Output Names

```bash
# ✓ Good: Same output name each time
./traceroute-scanner -range X -output daily_scan.jsonl -archive
./traceroute-scanner -range X -output daily_scan.jsonl -archive

# ✗ Bad: Changing output names
./traceroute-scanner -range X -output scan1.jsonl -archive
./traceroute-scanner -range X -output scan2.jsonl -archive
```

### 2. Let Scans Complete

```bash
# ✓ Good: Wait for completion before archiving
./traceroute-scanner -range X -mode raw
# ... let it finish ...
./traceroute-scanner -range X -mode raw -archive

# ✗ Bad: Trying to archive incomplete scan
./traceroute-scanner -range X -mode raw
# Ctrl+C after 10%
./traceroute-scanner -range X -mode raw -archive
# Won't archive, will resume instead
```

### 3. Implement Retention Policy

```bash
# Clean up old archives periodically
find . -name "scan.jsonl.*" -mtime +90 -delete
```

### 4. Use Descriptive Names

```bash
# ✓ Good: Clear purpose
./traceroute-scanner -range X -output corp_network_audit.jsonl -archive

# ✗ Bad: Generic names
./traceroute-scanner -range X -output scan.jsonl -archive
```

## Summary

### Key Features

✅ **Automatic Archiving** - Preserves old scans with timestamps
✅ **Smart Detection** - Only archives complete scans
✅ **No Data Loss** - Old scans kept, not deleted
✅ **Sortable Names** - ISO 8601 format for easy sorting
✅ **Resume Fallback** - Incomplete scans resume instead

### When to Use

| Scenario | Flag |
|----------|------|
| **First scan** | (no flag) |
| **Scan interrupted** | (no flag - auto-resumes) |
| **Rescan same network** | `-archive` |
| **Start completely over** | `-fresh` |
| **Don't care about old data** | `-fresh` |
| **Want to keep old data** | `-archive` |

### Perfect For

- 📅 Periodic monitoring (daily/weekly/monthly)
- 🔍 Change detection over time
- 📊 Historical analysis
- 🛡️ Security audit trails
- 🔄 Before/after comparisons
- 📈 Trend analysis

**The archive feature makes the scanner perfect for long-term network monitoring and change tracking!** 🎯

---

**Example workflow:**
```bash
# January: First scan
./traceroute-scanner -range 10.0.0.0/16 -mode raw -output network.jsonl

# February: Archive Jan, scan again
./traceroute-scanner -range 10.0.0.0/16 -mode raw -output network.jsonl -archive

# March: Archive Feb, scan again
./traceroute-scanner -range 10.0.0.0/16 -mode raw -output network.jsonl -archive

# Result:
# network.jsonl (current)
# network.jsonl.2026-01-15_10-00-00
# network.jsonl.2026-02-15_10-00-00
```

Simple, automatic, and preserves your historical data! 🚀
