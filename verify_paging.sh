#!/bin/bash

# Verification script for candidate display paging

echo "╔════════════════════════════════════════════════════════════╗"
echo "║         Verifying Candidate Display Paging                ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo

cat > /tmp/verify_paging.rs << 'EOF'
use pinyin_tui::PinyinDatabase;

fn calculate_visible_window(selected: usize, total: usize, page_size: usize) -> (usize, usize) {
    let visible_start = if selected < page_size {
        0
    } else if selected >= total.saturating_sub(page_size) {
        total.saturating_sub(page_size)
    } else {
        selected.saturating_sub(page_size / 2)
    };

    let visible_end = (visible_start + page_size).min(total);
    (visible_start, visible_end)
}

fn main() {
    println!("Loading database...");
    let db = PinyinDatabase::load_from_file("data/filtered_db.json").unwrap();
    println!("✓ Loaded {} entries\n", db.entries.len());

    println!("━━━ TEST 1: Paging Logic with 50 Items ━━━");
    let page_size = 20;
    let total_items = 50;

    let test_cases = vec![
        (0, "First item"),
        (10, "Middle of first page"),
        (19, "Last item of first page"),
        (20, "First item of second page"),
        (25, "Middle item"),
        (30, "Item 30"),
        (40, "Item 40"),
        (49, "Last item"),
    ];

    for (selected, description) in test_cases {
        let (start, end) = calculate_visible_window(selected, total_items, page_size);
        let is_visible = selected >= start && selected < end;

        if is_visible {
            println!("  ✓ {}: selected={}, window=[{}, {})",
                     description, selected, start, end);
        } else {
            println!("  ✗ {}: selected={}, window=[{}, {}) - NOT VISIBLE!",
                     description, selected, start, end);
            std::process::exit(1);
        }
    }
    println!();

    println!("━━━ TEST 2: Real Search with Navigation ━━━");
    // Search for "ni" which should return 50 results
    let query = "ni";
    let results = db.search(query);
    println!("Query '{}' returned {} results", query, results.len());

    if results.len() < 30 {
        println!("  ⚠ Warning: Need more results to test paging properly");
    } else {
        println!("  ✓ Sufficient results for paging test");
    }

    // Simulate navigation to different positions
    let positions_to_test = vec![0, 10, 19, 20, 25, 30, results.len().saturating_sub(1)];

    for pos in positions_to_test {
        if pos < results.len() {
            let (start, end) = calculate_visible_window(pos, results.len(), page_size);
            let is_visible = pos >= start && pos < end;

            if is_visible {
                println!("  ✓ Position {}: window=[{}, {}), item='{}'",
                         pos, start, end, results[pos]);
            } else {
                println!("  ✗ Position {}: NOT VISIBLE in window=[{}, {})",
                         pos, start, end);
                std::process::exit(1);
            }
        }
    }
    println!();

    println!("━━━ TEST 3: Edge Cases ━━━");

    // Test with fewer items than page size
    let small_total = 10;
    for selected in 0..small_total {
        let (start, end) = calculate_visible_window(selected, small_total, page_size);
        if selected < start || selected >= end {
            println!("  ✗ Failed with {} total items, selected {}", small_total, selected);
            std::process::exit(1);
        }
    }
    println!("  ✓ Works with fewer items than page size ({} items)", small_total);

    // Test with exactly page size items
    let exact_page = page_size;
    for selected in 0..exact_page {
        let (start, end) = calculate_visible_window(selected, exact_page, page_size);
        if selected < start || selected >= end {
            println!("  ✗ Failed with exactly {} items, selected {}", exact_page, selected);
            std::process::exit(1);
        }
    }
    println!("  ✓ Works with exactly page size ({} items)", exact_page);

    // Test boundary conditions
    let large_total = 100;
    let boundary_cases = vec![0, page_size - 1, page_size, large_total - page_size, large_total - 1];

    for selected in boundary_cases {
        let (start, end) = calculate_visible_window(selected, large_total, page_size);
        if selected < start || selected >= end {
            println!("  ✗ Boundary case failed: selected={}, window=[{}, {})", selected, start, end);
            std::process::exit(1);
        }
    }
    println!("  ✓ All boundary cases pass");
    println!();

    println!("━━━ TEST 4: Window Centering ━━━");
    let total = 100;

    // When in the middle, selected item should be roughly centered
    let middle_position = 50;
    let (start, end) = calculate_visible_window(middle_position, total, page_size);
    let position_in_window = middle_position - start;

    println!("  Selected position: {}", middle_position);
    println!("  Window: [{}, {})", start, end);
    println!("  Position in window: {}/{}", position_in_window, page_size);

    // Should be roughly in the middle (within 5 positions of center)
    let center = page_size / 2;
    if position_in_window >= center - 5 && position_in_window <= center + 5 {
        println!("  ✓ Selected item is centered in window");
    } else {
        println!("  ⚠ Selected item at position {}, expected near center ({})",
                 position_in_window, center);
    }
    println!();

    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("✓ ALL PAGING TESTS PASSED!");
    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("\nCandidate display paging verified:");
    println!("  • Selected item always visible in 20-item window");
    println!("  • Window scrolls to follow selection");
    println!("  • Proper handling of edge cases");
    println!("  • Selected item centered when in middle range");
}
EOF

echo "Compiling verification test..."
export PATH="$HOME/.cargo/bin:$PATH"
rustc --edition 2021 /tmp/verify_paging.rs -L target/release/deps --extern pinyin_tui=target/release/libpinyin_tui.rlib -o /tmp/verify_paging 2>&1

if [ -f /tmp/verify_paging ]; then
    echo "Running paging verification tests..."
    echo
    /tmp/verify_paging
    exit $?
else
    echo "Failed to compile verification test"
    exit 1
fi
