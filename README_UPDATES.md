# README Updates - January 2026

This document summarizes the major updates made to README.md to reflect the new shuffle and streaming features.

## New Sections Added

### 1. Enhanced Features Section
- Added **Streaming Mode** description
- Expanded **Advanced IP Shuffling** with Blackrock cipher details
- Updated to mention unlimited IP range support

### 2. Streaming vs Standard Mode
- New subsection explaining the difference
- Memory usage comparison
- Recommendations for when to use each

### 3. Stealth Scanning & IP Shuffling (Expanded)
- Added Blackrock cipher explanation
- Added reproducible scan examples with `-shuffle-seed`
- Added use cases for different seed strategies
- Examples for random vs deterministic shuffling

### 4. Streaming Mode with Blackrock Shuffle
- New "How It Works" subsection
- Technical details about the algorithm
- Performance metrics (~321ns per permutation)
- Memory efficiency explanation (O(1))

### 5. Checkpoint & Resume
- New "How It Works" subsection
- Explanation of progress tracking
- Range-based efficiency details

### 6. Streaming Mode & Blackrock Cipher Implementation
- New "Implementation Details" subsection
- Technical algorithm details
- Bit-space optimization explanation
- Performance benchmarks

### 7. Additional Documentation Section (NEW)
- Links to BLACKROCK_SHUFFLE.md
- Links to SHUFFLE_USAGE_GUIDE.md
- Links to SHUFFLE_DIAGNOSIS.md
- Links to test_shuffle_live.sh

### 8. Testing Section (NEW)
- Commands to run full test suite
- Commands to run shuffle-specific tests
- Test statistics (134 total tests)

## Updated Sections

### Options
- Added `-streaming` flag
- Added `-shuffle-seed` flag with explanation

### Examples
- Added large range with streaming mode example
- Added reproducible scan with seed example
- Added random shuffle example
- Added quick test with routable IPs example
- Updated most examples to use `-streaming` flag

### Troubleshooting (Major Expansion)
- **NEW**: "Scanner appears stuck with no progress" - comprehensive explanation
  - Problem description
  - Root cause (private IP ranges)
  - Two solutions provided
  - Note about shuffle working correctly
- Added "Want to verify shuffle is working?" section with test commands
- Added note about streaming mode for slow scanning

### Limitations
- Removed outdated "Large ranges (>1M IPs) have safety limit"
- Added note about private/unreachable IPs taking long
- Added solutions for private network scanning

### Performance Tips
- Reordered to put streaming mode first
- Added streaming mode as #1 tip
- Updated all examples to use `-streaming` flag
- Added new tip #7: "Test with Routable IPs First"

## Key Command Changes

### Before:
```bash
./traceroute-scanner -range 10.0.0.0/24 -mode external
```

### After (Recommended):
```bash
./traceroute-scanner -range 10.0.0.0/24 -streaming
```

### New Capabilities:
```bash
# Reproducible scan
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle-seed=42

# Random shuffle
./traceroute-scanner -range 1.0.0.0/16 -streaming -shuffle

# Quick test
./traceroute-scanner -range 8.8.8.8-8.8.8.10 -streaming -fresh
```

## Documentation Structure

README.md now covers:
1. Features (updated)
2. Requirements
3. Installation
4. Usage (expanded with streaming)
5. IP Range Formats
6. Options (added new flags)
7. Examples (many new examples)
8. Resume/Archive examples
9. Stealth Scanning & IP Shuffling (greatly expanded)
10. Output Format
11. How It Works (added streaming and checkpoints)
12. Implementation Details (added streaming section)
13. Security Considerations
14. Limitations (updated)
15. Performance Tips (expanded)
16. Troubleshooting (major expansion)
17. **Additional Documentation (NEW)**
18. **Testing (NEW)**
19. License

## Supporting Documentation

The README now references these additional files:
- **BLACKROCK_SHUFFLE.md** - Algorithm deep dive
- **SHUFFLE_USAGE_GUIDE.md** - Comprehensive usage examples
- **SHUFFLE_DIAGNOSIS.md** - Troubleshooting guide
- **test_shuffle_live.sh** - Automated test script

## Key Messages

1. **Streaming mode is recommended** for ranges larger than /24
2. **Shuffle is enabled by default** for stealth
3. **Seeds provide reproducibility** - same seed = same order
4. **Test with routable IPs** (8.8.8.8-8.8.8.10) before large scans
5. **Private IP ranges will appear stuck** - this is expected behavior, use shorter timeouts

## Backward Compatibility

All existing commands continue to work:
- Standard mode (non-streaming) still available
- All existing flags unchanged
- `-mode external` and `-mode raw` work as before
- Default behavior preserved (shuffle enabled, external mode)

New flags are optional enhancements:
- `-streaming` - opt-in for memory efficiency
- `-shuffle-seed` - opt-in for reproducibility
