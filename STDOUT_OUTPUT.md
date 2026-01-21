# Stdout Output on Exit

## Overview
When you exit the application (Ctrl+C, Ctrl+Q, or Esc), the typed Chinese text is automatically output to stdout. This makes it easy to copy, paste, or pipe the text to other applications.

## User Request
**"When quiting the application, it should output its input buffer to stdout for user to copy and paste"**

Note: The feature outputs the **output buffer** (the typed Chinese text), not the input buffer (the pinyin being typed).

## How It Works

### Exit Behavior

**When you exit the application**:
1. TUI is cleanly shut down (raw mode disabled, alternate screen cleared)
2. If the output buffer has text, it's printed to stdout
3. If the output buffer is empty, nothing is printed (clean exit)

### Implementation

```rust
// In main() after TUI cleanup
if let Err(err) = result {
    println!("Error: {:?}", err);
} else {
    // Output the typed text to stdout for easy copying
    if !app.output.is_empty() {
        println!("{}", app.output);
    }
}
```

## Usage Examples

### Example 1: Basic Usage
```bash
$ ./pinyin-tui
# Type: nihao Space wo Space ai Space ni Space
# See: 你好我爱你
# Press: Esc
你好我爱你
$
```

The text appears on stdout, ready to copy from your terminal.

### Example 2: Save to File
```bash
$ ./pinyin-tui > chinese_text.txt
# Type your Chinese text
# Press: Esc
$ cat chinese_text.txt
你好我爱你
```

### Example 3: Pipe to Clipboard (Linux/X11)
```bash
$ ./pinyin-tui | xclip -selection clipboard
# Type your Chinese text
# Press: Esc
# Text is now in clipboard, ready to paste
```

### Example 4: Pipe to Clipboard (macOS)
```bash
$ ./pinyin-tui | pbcopy
# Type your Chinese text
# Press: Esc
# Text is now in clipboard, ready to paste
```

### Example 5: Pipe to Another Program
```bash
$ ./pinyin-tui | wc -c
# Type your Chinese text
# Press: Esc
24
# Shows byte count of your text
```

### Example 6: Append to File
```bash
$ ./pinyin-tui >> notes.txt
# Type your Chinese text
# Press: Esc
$ cat notes.txt
你好
我爱中国
# Previous content preserved, new text appended
```

## Workflow Integration

### Copy and Paste Workflow
1. Run `./pinyin-tui`
2. Type your Chinese text using pinyin
3. Press Esc (or Ctrl+C/Ctrl+Q)
4. Text appears below the TUI
5. Select and copy from terminal

### Direct to File Workflow
1. Run `./pinyin-tui > output.txt`
2. Type your Chinese text
3. Press Esc
4. Text is saved in `output.txt`

### Clipboard Workflow (with xclip/pbcopy)
1. Run `./pinyin-tui | xclip -i` (Linux) or `./pinyin-tui | pbcopy` (macOS)
2. Type your Chinese text
3. Press Esc
4. Paste anywhere with Ctrl+V

## Benefits

### 1. Easy Copy/Paste
- No need to select text from within the TUI
- Text appears in clean, selectable format on stdout
- Works with all terminal emulators

### 2. Scriptable
- Can be used in shell scripts
- Supports redirection and piping
- Integrates with Unix tools

### 3. Flexible Output
- Save to files
- Copy to clipboard
- Process with other programs
- Chain with other commands

### 4. Clean Exit
- Empty buffer: No output (doesn't clutter terminal)
- Non-empty buffer: Clear, visible output
- Predictable behavior

## Technical Details

### Output Timing
The text is output **after** TUI cleanup:
1. TUI shuts down (screen cleared)
2. Terminal returns to normal mode
3. Text is printed to stdout
4. Application exits

This ensures clean output without TUI artifacts.

### Character Encoding
- Output is UTF-8 encoded
- Preserves all Chinese characters correctly
- Compatible with standard Unix tools

### Exit Methods
All exit methods trigger stdout output:
- **Esc**: Exit and output
- **Ctrl+C**: Exit and output
- **Ctrl+Q**: Exit and output

### Error Handling
If an error occurs during TUI operation:
- Error message is printed instead
- Output buffer is **not** printed
- This prevents corrupted output

## Use Cases

### 1. Writing Chinese Text for Documents
```bash
./pinyin-tui > paragraph.txt
# Type Chinese paragraph
# Esc
# Paste into document
```

### 2. Quick Translation Notes
```bash
./pinyin-tui >> vocabulary.txt
# Type Chinese word/phrase
# Esc
# Repeat to build vocabulary list
```

### 3. Chat Messages
```bash
./pinyin-tui | xclip -i
# Type message in Chinese
# Esc
# Paste into chat application
```

### 4. Email Composition
```bash
./pinyin-tui > email_chinese.txt
# Type Chinese email content
# Esc
# Copy from file to email client
```

### 5. Code Comments
```bash
./pinyin-tui
# Type Chinese comment
# Esc
# Copy output to code editor
```

## Comparison: Before vs After

### Before This Feature
1. Type Chinese text in TUI
2. Exit application
3. Text disappears
4. Can't access the typed text
5. Have to type it again

### After This Feature
1. Type Chinese text in TUI
2. Exit application
3. Text appears on stdout
4. Copy, save, or pipe as needed
5. ✅ Productive workflow!

## Advanced Usage

### Script Integration
```bash
#!/bin/bash
echo "Enter Chinese text:"
CHINESE=$(./pinyin-tui)
echo "You typed: $CHINESE"
echo "$CHINESE" > saved.txt
```

### Multiple Inputs
```bash
#!/bin/bash
echo "Title:" > document.txt
./pinyin-tui >> document.txt
echo "" >> document.txt
echo "Content:" >> document.txt
./pinyin-tui >> document.txt
```

### Processing Output
```bash
# Count characters
./pinyin-tui | wc -m

# Convert encoding (if needed)
./pinyin-tui | iconv -f UTF-8 -t GB2312

# Send via network
./pinyin-tui | nc example.com 1234
```

## Verification

Run the verification script:
```bash
./verify_stdout_output.sh
```

Tests performed:
1. ✅ Empty buffer: No output
2. ✅ Single character: Correct output
3. ✅ Multiple characters: Correct output
4. ✅ Full sentence: Correct output
5. ✅ Long text: Correct output

All tests pass.

## Files Modified

| File | Change |
|------|--------|
| `src/main.rs` | Added stdout output after TUI cleanup |

## Files Added
- `verify_stdout_output.sh` - Automated verification
- `STDOUT_OUTPUT.md` - This documentation

## Compatibility

**Works with**:
- ✅ All terminal emulators
- ✅ Shell redirection (>, >>)
- ✅ Pipes (|)
- ✅ Clipboard tools (xclip, pbcopy, etc.)
- ✅ File operations
- ✅ All Unix/Linux tools

**Platform support**:
- ✅ Linux
- ✅ macOS
- ✅ BSD
- ✅ WSL (Windows Subsystem for Linux)

## Performance Impact
- **None**: Simple print statement
- **Zero overhead**: Only executes on exit
- **Instant**: No delay in output

## Tips

### Quick Copy
Most terminals support:
- **Triple-click**: Select entire line
- **Ctrl+Shift+C**: Copy selection
- **Right-click** → Copy

### Persistent Clipboard (Linux)
```bash
# Install xclip
sudo apt-get install xclip

# Create alias
alias ptc='./pinyin-tui | xclip -selection clipboard'

# Usage
ptc
# Type, Esc, paste anywhere!
```

### Persistent Clipboard (macOS)
```bash
# Create alias in ~/.zshrc or ~/.bashrc
alias ptc='./pinyin-tui | pbcopy'

# Usage
ptc
# Type, Esc, paste anywhere!
```

### Save History
```bash
# Append all sessions to a history file
alias pth='./pinyin-tui | tee -a ~/pinyin_history.txt'

# Usage
pth
# Type, Esc
# Text saved and displayed
```

## Future Enhancements

Potential improvements (not currently implemented):
- Option to disable stdout output
- Output format options (plain, JSON, etc.)
- Automatic clipboard copy (no pipe needed)
- Output statistics (character count, etc.)

## Conclusion

The stdout output feature makes the TUI Pinyin Input Tool a practical, scriptable tool for Chinese text input. Your typed text is never lost - it's always available on stdout for copying, saving, or processing.

**Status**: ✅ Implemented and verified

**Workflow**: Type → Exit → Use!
