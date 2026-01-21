# Quick Start Guide - TUI Pinyin Input Tool

## Installation

```bash
# Build the project
cargo build --release

# Run the application
cargo run --release
# or
./target/release/pinyin-tui
```

## Basic Usage

### Three Ways to Input Chinese Characters

#### 1. With Tone Numbers (Most Accurate)
Type pinyin letters, then use **Shift+Number** for tones:

```
Input:  n i Shift+3 h a o Shift+3
Shows:  ni3hao3
Result: 你好
```

**Tone Keys**:
- Shift+1 = tone 1 (ā)
- Shift+2 = tone 2 (á)
- Shift+3 = tone 3 (ǎ)
- Shift+4 = tone 4 (à)
- Shift+5 = tone 5 (neutral)

#### 2. Without Tone Numbers (Fastest)
Just type the pinyin letters:

```
Input:  n i h a o
Shows:  nihao
Result: 你好 (among other matches)
```

#### 3. Abbreviated (Quick Input)
Type first letter + tone:

```
Input:  n Shift+3 h Shift+3
Shows:  n3h3
Result: 你好, 拟好, etc.
```

## Keyboard Controls

### Selection
- **Space**: Select highlighted suggestion (primary method)
- **1-9** (without Shift): Select suggestion by number
- **↑/↓**: Navigate suggestions one by one
- **←/→**: Jump 5 suggestions at a time
- **Enter** or **Tab**: Alternative selection keys

### Editing
- **Type letters**: Add to input
- **Shift+1-5**: Add tone numbers
- **Backspace**: Delete from input (or output if input is empty)

### Exit
- **Ctrl+C**: Quit (typed text output to stdout)
- **Ctrl+Q**: Quit (typed text output to stdout)
- **Esc**: Quit (typed text output to stdout)

**Note**: When you exit, your typed Chinese text will appear below the TUI, ready to copy!

## Examples

### Example 1: Hello (你好)
```
Method 1: n i Shift+3 h a o Shift+3 → ni3hao3 → Press Space
Method 2: n i h a o → nihao → Press Space
Method 3: n Shift+3 h Shift+3 → n3h3 → Press Space
```

### Example 2: I (我)
```
Method 1: w o Shift+3 → wo3 → Press Space
Method 2: w o → wo → Press Space
Method 3: w Shift+3 → w3 → Press Space
```

### Example 3: China (中国)
```
Method 1: z h o n g Shift+1 g u o Shift+2 → zhong1guo2 → Press Space
Method 2: z h o n g g u o → zhongguo → Press Space
```

### Example 4: Thank you (谢谢)
```
Method 1: x i e Shift+4 x i e → xie4xie → Press Space
Method 2: x i e x i e → xiexie → Press Space
```

## Tips

1. **Start with fuzzy search**: If you don't know the tones, just type the letters
2. **Use number keys**: When you see your desired character, press its number (1-9)
3. **Arrow keys for browsing**: Use ↑↓ to explore all suggestions
4. **Mix and match**: You can use different input methods for different characters

## Common Pinyin Patterns

### Single Characters
- `wo` → 我 (I/me)
- `ni` → 你 (you)
- `ta` → 他/她/它 (he/she/it)
- `hao` → 好 (good)
- `ren` → 人 (person)

### Common Words
- `nihao` → 你好 (hello)
- `xiexie` → 谢谢 (thank you)
- `zaijian` → 再见 (goodbye)
- `duibuqi` → 对不起 (sorry)
- `zhongguo` → 中国 (China)

## Getting Your Text Out

When you exit the application, your typed Chinese text is automatically printed to stdout. Here's how to use it:

### Copy from Terminal
1. Type your Chinese text
2. Press Esc (or Ctrl+C)
3. Text appears below
4. Select and copy from terminal

### Save to File
```bash
./pinyin-tui > my_text.txt
# Type your Chinese text
# Press Esc
# Text saved to my_text.txt
```

### Copy to Clipboard (Linux)
```bash
./pinyin-tui | xclip -selection clipboard
# Type your Chinese text
# Press Esc
# Paste anywhere with Ctrl+V
```

### Copy to Clipboard (macOS)
```bash
./pinyin-tui | pbcopy
# Type your Chinese text
# Press Esc
# Paste anywhere with Cmd+V
```

### Append to File
```bash
./pinyin-tui >> notes.txt
# Type your Chinese text
# Press Esc
# Text appended to notes.txt
```

## Troubleshooting

### Q: How do I input tone numbers?
A: Use **Shift+Number** (Shift+1 through Shift+5). This produces symbols (!@#$%) that are converted to tone numbers.

### Q: Why isn't my character showing up?
A: Try fuzzy search (without tones) to see more options. The character might have a different tone than you expected.

### Q: How do I know which tone to use?
A: If you don't know the tone, use fuzzy search (no tones). The suggestions will show you all possibilities.

### Q: Can I input multi-character words?
A: Yes! Type the full pinyin (e.g., `nihao` for 你好). The suggestions will include both single characters and common phrases.

### Q: The suggestions show many characters. How do I find the right one?
A:
- Use ↑↓ arrows to browse
- Press 1-9 to quickly select if it's in the first 9 results
- More specific pinyin (with tones) gives fewer, more accurate results

## Interface Overview

```
┌─────────────────────────────────────────┐
│ TUI Pinyin Input Tool                   │
└─────────────────────────────────────────┘

┌─────────────────────────────────────────┐
│ Output                                  │
│ [Your Chinese text will appear here]    │
└─────────────────────────────────────────┘

┌─────────────────────────────────────────┐
│ Suggestions (0 matches)                 │
│                                         │
│ Type pinyin with or without tones:      │
│   • With tones: ni3hao3 → 你好         │
│     (use Shift+Number)                  │
│   • Without tones: nihao → 你好        │
│     (fuzzy search)                      │
│   • Abbreviated: n3h3 → 你好           │
└─────────────────────────────────────────┘

┌─────────────────────────────────────────┐
│ Input & Controls                        │
│ Input: _                                │
│                                         │
│ Tone: Shift+1-5 │ Select: 1-9 or Enter │
│ Navigate: ↑↓/←→ │ Delete: Backspace    │
│ Exit: Ctrl+C/Esc                        │
└─────────────────────────────────────────┘
```

## Learning Path

1. **Day 1**: Practice with fuzzy search (no tones)
   - Type `wo`, `ni`, `hao`, `ren`, etc.
   - Get comfortable with basic navigation

2. **Day 2**: Learn tone input
   - Practice Shift+Number for common characters
   - Try `ni3`, `hao3`, `wo3`

3. **Day 3**: Try multi-character words
   - Input `nihao`, `xiexie`, `zaijian`
   - Explore the suggestions

4. **Day 4**: Master all three input modes
   - Mix exact, abbreviated, and fuzzy input
   - Find your preferred workflow

## Need Help?

- Check `README.md` for detailed documentation
- See `SHIFT_INPUT_FIX.md` for tone input technical details
- See `FUZZY_SEARCH_IMPLEMENTATION.md` for search algorithm details
- Run verification scripts:
  - `./verify_tone_input.sh` - Test tone input
  - `./verify_fuzzy_search.sh` - Test fuzzy search

Happy typing! 🎉
