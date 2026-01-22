# TUI Pinyin Input Tool

A Terminal User Interface (TUI) application for Chinese pinyin input, built with Rust and the [ratatui](https://github.com/ratatui-org/ratatui) library.

## Features

- Real-time pinyin-to-Chinese character conversion
- **Three input modes**:
  - **Full pinyin with tones**: `ni3hao3` → 你好 (highest priority)
  - **Abbreviated with tones**: `n3h3` → 你好 (medium priority)
  - **Fuzzy search without tones**: `nihao` → 你好 (lowest priority)
- **Tab mode switching**: Switch between Pinyin and Direct typing modes
  - **Pinyin Mode**: Normal Chinese input with suggestions
  - **Direct Mode**: Type English, punctuation, mixed content directly
- **Frequency-based sorting**: Most common characters appear first
- **Large database**: 120,604 entries from CC-CEDICT
- **Comprehensive frequency data**:
  - SUBTLEX-CH: 45,318 words with actual usage frequencies
  - Jun Da: Character frequencies for fallback estimation
- Interactive TUI with keyboard navigation
- Fast suggestion matching with intelligent result prioritization

## Requirements

- Rust toolchain (1.70 or later)
- A C compiler (GCC or Clang) with glibc development files
- Terminal with UTF-8 support

## Building

On a system with proper development tools installed:

```bash
cargo build --release
```

The compiled binary will be available at `target/release/pinyin-tui`.

## Usage

Run the application from the project directory:

```bash
cargo run --release
```

Or run the compiled binary directly:

```bash
./target/release/pinyin-tui
```

### Advanced Usage

**Save output to file**:
```bash
./target/release/pinyin-tui > output.txt
```

**Copy to clipboard** (Linux with xclip):
```bash
./target/release/pinyin-tui | xclip -selection clipboard
```

**Copy to clipboard** (macOS):
```bash
./target/release/pinyin-tui | pbcopy
```

When you exit the application (Esc, Ctrl+C, or Ctrl+Q), your typed Chinese text will be printed to stdout, making it easy to copy, save, or pipe to other applications.

## How to Use

1. **Type pinyin with tone numbers**: Type romanized Chinese with tone numbers (1-5)
   - **Use Shift+Number to input tone numbers**: `n`, `i`, `Shift+3` for `ni3`
   - Example: `ni3hao3` for 你好 (type: n, i, Shift+3, h, a, o, Shift+3)
   - Abbreviated form: `n3h3` also works

2. **Navigate suggestions**:
   - Press `↑` / `↓` to move through suggestions one by one
   - Press `←` / `→` to jump by 5 suggestions
   - Press `1-9` (without Shift) to directly select a suggestion
   - The display automatically scrolls to keep the selected item visible (shows 20 items at a time)

3. **Select a character**:
   - Press `Space` to select the highlighted suggestion (primary method)
   - Alternatively, press `Enter`
   - Press `1-9` to directly select by number
   - The selected characters will appear in the output area

4. **Mixed language input** (NEW):
   - Press `Tab` to switch to Direct Mode
   - In Direct Mode, type English, punctuation, etc. directly
   - Press `Tab` again to return to Pinyin Mode
   - Perfect for mixed Chinese-English content!

5. **Delete**:
   - Press `Backspace` to delete from the input buffer
   - If input is empty, `Backspace` deletes from the output

6. **Exit**: Press `Ctrl+C`, `Ctrl+Q`, or `Esc` to exit
   - Your typed text will be output to stdout
   - Easy to copy from terminal or redirect to file/clipboard

### Important: Tone Number Input
- **Shift+1** through **Shift+5** = Input tone numbers
- **1-9** alone = Select candidates from suggestions
- This design prevents number input conflicts with candidate selection

## Architecture

The application consists of:

- **PinyinDatabase**: Parses and indexes the pinyin dictionary from JSON
- **App**: Main application state and logic
- **UI**: Terminal user interface rendered with ratatui
- **Event Loop**: Handles keyboard input and updates

### Pinyin Matching

The tool supports three matching modes with intelligent prioritization:

1. **Full pinyin with tones** (highest priority): Matches complete pinyin syllables with tones
   - Example: `ni3` matches characters with initial "n", final "i", and tone 3
   - Example: `ni3hao3` → 你好

2. **Abbreviated pinyin with tones** (medium priority): Matches only initials plus tones
   - Example: `n3` matches any character starting with "n" and tone 3
   - Example: `n3h3` → 你好
   - Useful for faster typing

3. **Fuzzy search without tones** (lowest priority): Matches pinyin without tone numbers
   - Example: `ni` matches all characters with initial "n" and final "i", regardless of tone
   - Example: `nihao` → 你好
   - Ideal for quick input when you don't know or don't want to specify tones

Results are automatically sorted by match type (exact > abbreviated > fuzzy) and then by character length, ensuring the most relevant suggestions appear first.

## Data

The application uses the `data/frequency_db.json` file, which contains:
- **120,604 Chinese characters and words**
- Corresponding pinyin representations (initial, final, tone)
- **Comprehensive frequency information** for intelligent sorting:
  - **SUBTLEX-CH**: 45,318 words with actual usage frequencies from 33.5M word corpus
  - **Jun Da**: Character frequencies for fallback estimation of remaining words
- Derived from **CC-CEDICT** (124K entries)
- Most common characters (的, 我, 你, 是, 了, 不, etc.) appear first

## Dependencies

- `ratatui` 0.29 - Terminal UI framework
- `crossterm` 0.28 - Cross-platform terminal manipulation
- `serde` 1.0 - Serialization framework
- `serde_json` 1.0 - JSON parsing

## Implementation Notes

This implementation does NOT use the original librustpinyin C library. Instead, it:
- Directly parses the JSON dictionary format
- Implements custom pinyin matching logic
- Avoids C FFI complexity

This approach simplifies the build process and makes the code more maintainable.

## License

This project uses data derived from CC-CEDICT, which is licensed under Creative Commons Attribution-ShareAlike 4.0 International License.

## Troubleshooting

### Build fails with linker errors

Ensure you have a complete C development environment:

**Ubuntu/Debian:**
```bash
sudo apt-get install build-essential
```

**Fedora/RHEL:**
```bash
sudo dnf groupinstall "Development Tools"
```

**macOS:**
```bash
xcode-select --install
```

### Characters not displaying correctly

Ensure your terminal supports UTF-8 and has Chinese fonts installed.

### Performance issues with large dictionary

The initial load may take 3-4 seconds as it parses and sorts 120,000+ entries by frequency. This is a one-time cost at startup and ensures the most common characters appear first in suggestions.
