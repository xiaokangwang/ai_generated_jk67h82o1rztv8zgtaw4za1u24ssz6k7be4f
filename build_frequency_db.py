#!/usr/bin/env python3
"""
Build a frequency-enhanced pinyin database.

Combines CC-CEDICT with comprehensive frequency data from:
1. SUBTLEX-CH (99,121 words with actual usage frequencies)
2. Jun Da's bigram lists (217,263 unique 2-character words from two sources)
3. Jun Da's combined character frequency list (12,041 characters)
4. Fallback estimation for remaining words
"""

import json
import re
from collections import defaultdict
import math

def load_subtlex_frequencies():
    """
    Load word frequencies from SUBTLEX-CH database.
    Returns a dictionary mapping Chinese words to their frequency rank.
    """
    freq_map = {}
    print("Loading SUBTLEX-CH word frequencies...")

    with open('data/SUBTLEX-CH-WF', 'r', encoding='gb18030') as f:
        lines = f.readlines()
        for i, line in enumerate(lines[3:], start=1):
            parts = line.strip().split('\t')
            if len(parts) >= 2:
                word = parts[0]
                freq_map[word] = i

    print(f"  Loaded {len(freq_map):,} words from SUBTLEX-CH")
    return freq_map

def load_jun_da_bigrams():
    """
    Load 2-character word frequencies from Jun Da's bigram list.
    Returns a dictionary mapping 2-char words to their rank.
    """
    freq_map = {}
    print("Loading Jun Da 2-character word frequencies...")

    with open('user-data/Bigram(2).txt', 'r', encoding='gb18030') as f:
        for line in f:
            if line.startswith('/*') or not line.strip():
                continue
            parts = line.strip().split('\t')
            if len(parts) >= 3 and parts[0].isdigit():
                rank = int(parts[0])
                word = parts[1]
                if len(word) == 2:  # Ensure it's 2 characters
                    freq_map[word] = rank

    print(f"  Loaded {len(freq_map):,} 2-character words from Jun Da bigrams")
    return freq_map

def load_jun_da_bigrams_alt():
    """
    Load additional 2-character word frequencies from Jun Da's alternate bigram list.
    Returns a dictionary mapping 2-char words to their rank.
    """
    freq_map = {}
    print("Loading Jun Da alternate 2-character word frequencies...")

    with open('user-data/Bigram(3).txt', 'r', encoding='gb18030') as f:
        for line in f:
            if line.startswith('/*') or not line.strip():
                continue
            parts = line.strip().split('\t')
            if len(parts) >= 3 and parts[0].isdigit():
                rank = int(parts[0])
                word = parts[1]
                if len(word) == 2:  # 2-character words
                    freq_map[word] = rank

    print(f"  Loaded {len(freq_map):,} additional 2-character words from Jun Da")
    return freq_map

def load_jun_da_char_combined():
    """
    Load character frequencies from Jun Da's combined classical/modern list.
    Returns a dictionary mapping single characters to their rank.
    """
    char_freq_map = {}
    print("Loading Jun Da combined character frequencies...")

    with open('user-data/CharFreq-Combined.csv', 'r', encoding='utf-8') as f:
        for line in f:
            if line.startswith('/*') or not line.strip():
                continue
            # CSV format: rank,char,freq,cumulative%,pinyin,english
            parts = line.strip().split(',')
            if len(parts) >= 3 and parts[0].isdigit():
                rank = int(parts[0])
                char = parts[1]
                if len(char) == 1:  # Single character only
                    char_freq_map[char] = rank

    print(f"  Loaded {len(char_freq_map):,} characters from Jun Da combined list")
    return char_freq_map

def calculate_word_frequency_fallback(word, char_freq_map):
    """
    Estimate frequency for words not in any frequency database.
    This is the last resort fallback.
    """
    if len(word) == 0:
        return 500000

    # For single characters, use direct frequency
    if len(word) == 1:
        return char_freq_map.get(word, 250000)

    # For multi-character words, estimate from components
    freqs = [char_freq_map.get(char, 250000) for char in word]

    # Geometric mean + aggressive length penalty
    geo_mean = math.exp(sum(math.log(f + 1) for f in freqs) / len(freqs))
    length_penalty = (len(word) - 1) * 50000

    return int(geo_mean + length_penalty)

def parse_cedict_line(line):
    """Parse a single CEDICT entry."""
    if line.startswith('#') or not line.strip():
        return None

    # Format: Traditional Simplified [pinyin] /definition/
    match = re.match(r'^(\S+)\s+(\S+)\s+\[([^\]]+)\]\s+/(.+)/$', line.strip())
    if not match:
        return None

    traditional, simplified, pinyin, definition = match.groups()
    return {
        'traditional': traditional,
        'simplified': simplified,
        'pinyin': pinyin,
        'definition': definition
    }

def pinyin_to_parts(pinyin_str):
    """
    Convert pinyin with tone numbers to [initial, final, tone] format.
    """
    initials = ['b', 'p', 'm', 'f', 'd', 't', 'n', 'l', 'g', 'k', 'h',
                'j', 'q', 'x', 'zh', 'ch', 'sh', 'r', 'z', 'c', 's', 'y', 'w']

    pinyin_str = pinyin_str.lower().strip()

    # Extract tone
    tone = '5'
    if pinyin_str and pinyin_str[-1].isdigit():
        tone = pinyin_str[-1]
        pinyin_str = pinyin_str[:-1]

    # Find initial
    initial = ''
    final = pinyin_str

    # Check two-letter initials first
    for init in ['zh', 'ch', 'sh']:
        if pinyin_str.startswith(init):
            initial = init
            final = pinyin_str[len(init):]
            break

    # Check single-letter initials
    if not initial:
        for init in initials:
            if len(init) == 1 and pinyin_str.startswith(init):
                initial = init
                final = pinyin_str[1:]
                break

    return [initial, final, tone]

def build_frequency_database():
    """Build enhanced database with comprehensive frequency information."""
    print("=" * 70)
    print("Building Comprehensive Frequency Database")
    print("=" * 70)

    # Load all frequency data sources
    print("\n[Step 1/5] Loading frequency data from all sources...")
    subtlex_freq = load_subtlex_frequencies()
    bigram_freq = load_jun_da_bigrams()
    bigram_freq_alt = load_jun_da_bigrams_alt()
    char_freq_map = load_jun_da_char_combined()

    # Parse CEDICT
    print("\n[Step 2/5] Parsing CC-CEDICT...")
    entries = []
    with open('data/cedict.txt', 'r', encoding='utf-8') as f:
        for line in f:
            entry = parse_cedict_line(line)
            if entry:
                entries.append(entry)

    print(f"  Parsed {len(entries):,} entries from CEDICT")

    # Build database grouped by hanzi
    print("\n[Step 3/5] Processing entries with frequency priority...")
    db = defaultdict(list)

    # Statistics counters
    stats = {
        'subtlex': 0,
        'bigram': 0,
        'bigram_alt': 0,
        'char_freq': 0,
        'fallback': 0
    }

    for entry in entries:
        hanzi = entry['simplified']
        pinyin = entry['pinyin']

        # Split pinyin into syllables and convert to parts
        syllables = pinyin.split()
        pinyin_parts = []
        for syl in syllables:
            parts = pinyin_to_parts(syl)
            pinyin_parts.append(parts)

        # Determine frequency with priority order
        source = 'fallback'

        if hanzi in subtlex_freq:
            frequency = subtlex_freq[hanzi]
            source = 'subtlex'
        elif len(hanzi) == 2 and hanzi in bigram_freq:
            # Jun Da bigrams (primary) use rank, offset to avoid conflicts with SUBTLEX
            frequency = 100000 + bigram_freq[hanzi]
            source = 'bigram'
        elif len(hanzi) == 2 and hanzi in bigram_freq_alt:
            # Jun Da bigrams (alternate) use rank, offset more
            frequency = 250000 + bigram_freq_alt[hanzi]
            source = 'bigram_alt'
        elif len(hanzi) == 1 and hanzi in char_freq_map:
            # Single characters from combined list
            frequency = 400000 + char_freq_map[hanzi]
            source = 'char_freq'
        else:
            # Fallback estimation
            frequency = calculate_word_frequency_fallback(hanzi, char_freq_map)
            source = 'fallback'

        stats[source] += 1

        # Add to database (keep most common if duplicate)
        if hanzi not in db:
            db[hanzi] = {
                'pinyin': pinyin_parts,
                'frequency': frequency,
                'source': source
            }
        else:
            if frequency < db[hanzi]['frequency']:
                db[hanzi] = {
                    'pinyin': pinyin_parts,
                    'frequency': frequency,
                    'source': source
                }

    print(f"  Built database with {len(db):,} unique entries")

    # Convert to JSON format
    print("\n[Step 4/5] Saving to data/frequency_db.json...")
    output = {}
    for hanzi, data in db.items():
        output[hanzi] = {
            'pinyin': data['pinyin'],
            'frequency': data['frequency']
        }

    with open('data/frequency_db.json', 'w', encoding='utf-8') as f:
        json.dump(output, f, ensure_ascii=False, indent=2)

    print(f"  ✓ Saved to data/frequency_db.json")

    # Show statistics
    print("\n[Step 5/5] Database Statistics")
    print("=" * 70)
    print(f"Total entries: {len(db):,}")
    print(f"\nFrequency Source Breakdown:")
    print(f"  SUBTLEX-CH (real usage):        {stats['subtlex']:>7,} ({stats['subtlex']*100//len(entries):>2}%)")
    print(f"  Jun Da bigrams primary:         {stats['bigram']:>7,} ({stats['bigram']*100//len(entries):>2}%)")
    print(f"  Jun Da bigrams alternate:       {stats['bigram_alt']:>7,} ({stats['bigram_alt']*100//len(entries):>2}%)")
    print(f"  Jun Da char freq (1-char):      {stats['char_freq']:>7,} ({stats['char_freq']*100//len(entries):>2}%)")
    print(f"  Fallback estimates (3+ char):   {stats['fallback']:>7,} ({stats['fallback']*100//len(entries):>2}%)")

    # Coverage statistics
    single_char = sum(1 for h in db if len(h) == 1)
    two_char = sum(1 for h in db if len(h) == 2)
    three_char = sum(1 for h in db if len(h) == 3)
    four_plus = sum(1 for h in db if len(h) >= 4)

    print(f"\nLength Distribution:")
    print(f"  Single characters:     {single_char:>7,}")
    print(f"  Two-character words:   {two_char:>7,}")
    print(f"  Three-character words: {three_char:>7,}")
    print(f"  Four+ character words: {four_plus:>7,}")

    # Show top 30 entries
    print(f"\n{'=' * 70}")
    print("Top 30 Most Common Entries:")
    print(f"{'=' * 70}")
    sorted_entries = sorted(db.items(), key=lambda x: x[1]['frequency'])[:30]
    for rank, (hanzi, data) in enumerate(sorted_entries, 1):
        pinyin_str = ' '.join([''.join(p) for p in data['pinyin']])
        freq = data['frequency']
        source = data['source'].upper()
        print(f"  {rank:2d}. {hanzi:8s} ({pinyin_str:18s}) freq:{freq:>8d} [{source}]")

    print(f"\n{'=' * 70}")
    print("✓ Comprehensive frequency database build complete!")
    print("✓ Integrated all frequency data from Jun Da's website")
    print(f"{'=' * 70}")

if __name__ == '__main__':
    build_frequency_database()
