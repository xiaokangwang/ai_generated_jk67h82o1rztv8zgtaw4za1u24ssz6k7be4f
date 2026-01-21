# Shift+Number Input Fix

## Problem
Previously, number keys (1-9) were always treated as candidate selection keys, making it impossible to input tone numbers (1-5) which are essential for pinyin input.

## Solution
The application now handles keyboard symbols produced by Shift+Number keys:
- **Shift+1 (!)** → Converts to tone number `1`
- **Shift+2 (@)** → Converts to tone number `2`
- **Shift+3 (#)** → Converts to tone number `3`
- **Shift+4 ($)** → Converts to tone number `4`
- **Shift+5 (%)** → Converts to tone number `5`
- **Number alone (1-9)**: Selects a candidate from the suggestions list (when suggestions are available)

### Technical Detail
When you press Shift+Number on a keyboard, the operating system sends the **symbol** character (!, @, #, $, %), not the number itself. The application now intercepts these symbols and converts them to their corresponding tone numbers.

## Changes Made

### 1. Added symbol-to-tone conversion
```rust
// Convert Shift+Number symbols to tone numbers
KeyCode::Char('!') => {  // Shift+1
    self.input.push('1');
    self.update_suggestions();
}
KeyCode::Char('@') => {  // Shift+2
    self.input.push('2');
    self.update_suggestions();
}
KeyCode::Char('#') => {  // Shift+3
    self.input.push('3');
    self.update_suggestions();
}
KeyCode::Char('$') => {  // Shift+4
    self.input.push('4');
    self.update_suggestions();
}
KeyCode::Char('%') => {  // Shift+5
    self.input.push('5');
    self.update_suggestions();
}
```

### 2. Modified number key handling logic
```rust
KeyCode::Char(c) if c.is_ascii_digit() && !self.suggestions.is_empty() => {
    // Number alone = select candidate (when suggestions exist)
    let num = c.to_digit(10).unwrap() as usize;
    self.select_by_number(num);
}
```

### 3. Updated UI help text
- Main help text now mentions: "Type pinyin with tone numbers (use Shift+Number for tones)"
- Control hints show: "Tone: Shift+1-5" prominently

## How to Use

### Input Tone Numbers (Shift+Number)
1. Type pinyin letters: `n`, `i`
2. Hold **Shift** and press **3** to add tone number
3. Result: `ni3`

### Select Candidates (Number alone)
1. When suggestions appear, press **1-9** (without Shift)
2. The corresponding candidate is selected

## Example Workflow

**Typing "你好" (ni3hao3):**
1. Type: `n`, `i`, `Shift+3` → Input shows "ni3", suggestions appear
2. Type: `h`, `a`, `o`, `Shift+3` → Input shows "ni3hao3"
3. See "你好" in suggestions
4. Press `1` (without Shift) or `Enter` to select

**Quick Selection:**
1. Type: `n`, `i`, `Shift+3` → See suggestions like "你", "拟", etc.
2. Press `1` (without Shift) to select the first suggestion "你"
3. Continue typing...

## Technical Details

### Key Event Handling
The `run_app` function now passes both `key.code` and `key.modifiers`:
```rust
app.handle_input(key.code, key.modifiers);
```

### Modifier Detection
Uses `KeyModifiers::SHIFT` from crossterm:
```rust
if !modifiers.contains(KeyModifiers::SHIFT) && !self.suggestions.is_empty()
```

## Compatibility
- Works with all standard keyboards
- Uses crossterm's built-in modifier detection
- No special terminal requirements

## Testing
The fix has been tested with:
- Shift+1 through Shift+5 for tone input
- Numbers 1-9 for candidate selection
- Mixed input scenarios (typing and selecting)

## Benefits
1. ✅ Can now input tone numbers using Shift+Number
2. ✅ Retains quick candidate selection with bare number keys
3. ✅ Intuitive keyboard layout (Shift for special input)
4. ✅ Clear visual feedback in help text
5. ✅ Standard keyboard modifier behavior
