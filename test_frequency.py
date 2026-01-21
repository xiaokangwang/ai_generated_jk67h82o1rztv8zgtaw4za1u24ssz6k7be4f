#!/usr/bin/env python3
import json

# Load the database
with open('data/frequency_db.json', 'r', encoding='utf-8') as f:
    db = json.load(f)

# Test cases - these should all be rank 1 (most common) for their pinyin
test_cases = [
    ("你", "ni"),
    ("我", "wo"),
    ("是", "shi"),
    ("的", "de"),
    ("不", "bu"),
    ("你好", "nihao"),
    ("我们", "women"),
    ("什么", "shenme"),
]

print("=" * 60)
print("Testing frequency rankings for common words:")
print("=" * 60)

for word, pinyin_query in test_cases:
    if word in db:
        freq = db[word]['frequency']
        pinyin_parts = db[word]['pinyin']
        pinyin_str = ''.join([''.join(p) for p in pinyin_parts])
        print(f"{word:6s} ({pinyin_str:12s}) - frequency rank: {freq:6d}")
    else:
        print(f"{word:6s} - NOT FOUND in database")

print("\n" + "=" * 60)
print("Comparing: SUBTLEX vs Estimated frequencies")
print("=" * 60)

# Load SUBTLEX data
subtlex_words = set()
with open('data/SUBTLEX-CH-WF', 'r', encoding='gb18030') as f:
    for line in f.readlines()[3:]:
        parts = line.strip().split('\t')
        if parts:
            subtlex_words.add(parts[0])

subtlex_count = sum(1 for word in test_cases if word[0] in subtlex_words)
print(f"Test words in SUBTLEX-CH: {subtlex_count}/{len(test_cases)}")

for word, _ in test_cases:
    source = "SUBTLEX-CH" if word in subtlex_words else "estimated"
    print(f"  {word:6s} - {source}")

