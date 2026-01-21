# Candidate Display Paging Fix

## Problem
The candidate display was not working properly when navigating beyond the first 20 suggestions. The display always showed items 0-19, but users could navigate to items beyond position 19 using arrow keys. This caused the selected item to become invisible.

## Root Cause
In `src/main.rs`, the UI rendering code used `.take(20)` to display only the first 20 suggestions, regardless of which item was selected:

```rust
// OLD CODE - BROKEN
let items: Vec<ListItem> = app
    .suggestions
    .iter()
    .enumerate()
    .take(20)  // ← Always shows first 20 items only!
    .map(|(i, s)| {
        // ... rendering code
    })
    .collect();
```

**Issue**: If `selected_suggestion = 25`, the selected item was at position 25, but the display only showed positions 0-19. The highlighted item was not visible!

## Solution
Implemented a **scrolling window** that follows the selected item. The visible window of 20 items now dynamically adjusts based on the selected position.

### Scrolling Logic

```rust
let page_size = 20;
let total_items = app.suggestions.len();

// Calculate the start of the visible window
let visible_start = if app.selected_suggestion < page_size {
    // If selected is in first page, show from start
    0
} else if app.selected_suggestion >= total_items.saturating_sub(page_size) {
    // If selected is in last page, show the last page
    total_items.saturating_sub(page_size)
} else {
    // Otherwise, keep selected item in the middle of the window
    app.selected_suggestion.saturating_sub(page_size / 2)
};

let visible_end = (visible_start + page_size).min(total_items);
```

### Behavior

1. **First page** (selected 0-19): Display items 0-19
2. **Middle range** (selected 20-29 for 50 items): Display window centered on selected item
3. **Last page** (selected 30-49 for 50 items): Display last 20 items (30-49)

### Examples

With 50 total suggestions:

| Selected Position | Visible Window | Selected Position in Window |
|-------------------|----------------|----------------------------|
| 0                 | [0, 20)        | 0 (first)                  |
| 10                | [0, 20)        | 10 (middle)                |
| 19                | [0, 20)        | 19 (last)                  |
| 20                | [10, 30)       | 10 (middle)                |
| 25                | [15, 35)       | 10 (middle)                |
| 30                | [30, 50)       | 0 (first)                  |
| 40                | [30, 50)       | 10 (middle)                |
| 49                | [30, 50)       | 19 (last)                  |

## Implementation Details

### Modified Code

```rust
// NEW CODE - FIXED
let items: Vec<ListItem> = app
    .suggestions
    .iter()
    .enumerate()
    .skip(visible_start)      // ← Skip to visible window start
    .take(visible_end - visible_start)  // ← Take only visible items
    .map(|(i, s)| {
        let style = if i == app.selected_suggestion {
            Style::default()
                .fg(Color::Black)
                .bg(Color::Cyan)
                .add_modifier(Modifier::BOLD)
        } else {
            Style::default().fg(Color::White)
        };
        let number = if i < 9 { format!("{}. ", i + 1) } else { "   ".to_string() };
        let content = format!("{}{}", number, s);
        ListItem::new(content).style(style)
    })
    .collect();
```

### Key Changes

1. **`skip(visible_start)`**: Skip items before the visible window
2. **`take(visible_end - visible_start)`**: Take only items in the visible window
3. **Window calculation**: Dynamically computed based on `selected_suggestion`

## Edge Cases Handled

### 1. Fewer Items Than Page Size
If there are only 10 suggestions:
- Always show all 10 items
- No scrolling needed
- Window: [0, 10)

### 2. Exactly Page Size Items
If there are exactly 20 suggestions:
- Show all 20 items
- No scrolling needed
- Window: [0, 20)

### 3. More Items Than Page Size
If there are 50 suggestions:
- Show 20-item sliding window
- Window scrolls to keep selected item visible
- Centered when in middle range

### 4. Navigation at Boundaries
- **At start** (position 0): Window starts at 0
- **At end** (position 49 of 50): Window shows last 20 items
- **Wrapping** (Down at last item): Wraps to position 0, window resets

## Testing

### Verification Script
Run `./verify_paging.sh` to test the paging logic:

```bash
./verify_paging.sh
```

**Tests performed**:
1. ✅ Paging logic with 50 items
2. ✅ Real search with navigation
3. ✅ Edge cases (fewer items, exact page size, boundaries)
4. ✅ Window centering behavior

### Manual Testing
1. Search for "ni" (returns 50 results)
2. Press ↓ repeatedly to navigate through all 50 items
3. Verify the highlighted item is always visible
4. Press → to jump by 5 positions
5. Verify scrolling works correctly

## User Experience Improvements

### Before Fix
- ❌ Selected item invisible when navigating beyond position 19
- ❌ Confusing: pressing ↓ seemed to do nothing
- ❌ Users couldn't access items beyond the first page

### After Fix
- ✅ Selected item always visible in the display
- ✅ Smooth scrolling experience
- ✅ Can navigate through all suggestions (up to 50)
- ✅ Window centers on selected item for optimal viewing

## Performance Impact
- **Minimal**: Added a few arithmetic operations for window calculation
- **No database/search impact**: Only affects UI rendering
- **Same memory usage**: Still showing 20 items at a time

## Related Files Modified
- `src/main.rs` (lines 252-275): UI rendering function
- Added: `verify_paging.sh` - Automated verification script
- Added: `PAGING_FIX.md` - This documentation

## Compatibility
- ✅ All existing features unchanged
- ✅ All 10 unit tests still pass
- ✅ No breaking changes
- ✅ Works with fuzzy search, exact search, and abbreviated search

## Technical Notes

### Why Center the Selected Item?
Centering provides the best user experience:
- Users can see both items above and below the selection
- Easier to browse through a long list
- Standard behavior in most UI frameworks

### Why Cap at 50 Results?
The search function already limits results to 50 (in `src/lib.rs`):
```rust
if exact_matches.len() + abbreviated_matches.len() + fuzzy_matches.len() >= 50 {
    break;
}
```

This keeps the UI responsive and prevents overwhelming users with too many choices.

### Window Calculation Strategy
The three-case logic ensures:
1. **First page**: No jumping when starting navigation
2. **Middle**: Smooth centered scrolling
3. **Last page**: Anchor to bottom to prevent jitter

## Future Enhancements
Potential improvements (not currently implemented):
- Add page indicator (e.g., "Page 2/3" or "20-40 of 50")
- Support PgUp/PgDown keys for faster navigation
- Configurable page size
- Smooth scrolling animation

## Verification Results

```
━━━ TEST 1: Paging Logic with 50 Items ━━━
  ✓ First item: selected=0, window=[0, 20)
  ✓ Middle of first page: selected=10, window=[0, 20)
  ✓ Last item of first page: selected=19, window=[0, 20)
  ✓ First item of second page: selected=20, window=[10, 30)
  ✓ Middle item: selected=25, window=[15, 35)
  ✓ Item 30: selected=30, window=[30, 50)
  ✓ Item 40: selected=40, window=[30, 50)
  ✓ Last item: selected=49, window=[30, 50)

━━━ TEST 2: Real Search with Navigation ━━━
  ✓ Position 0: window=[0, 20), item='㞙'
  ✓ Position 10: window=[0, 20), item='儞'
  ✓ Position 19: window=[0, 20), item='凝固'
  ✓ Position 20: window=[10, 30), item='凝块'
  ✓ Position 25: window=[15, 35), item='凝神'
  ✓ Position 30: window=[30, 50), item='凝胶'
  ✓ Position 49: window=[30, 50), item='凝集素'

✓ ALL PAGING TESTS PASSED!
```

## Conclusion
The candidate display paging now works correctly, allowing users to navigate through all search results with the selected item always visible. The implementation is efficient, handles all edge cases, and provides a smooth user experience.
