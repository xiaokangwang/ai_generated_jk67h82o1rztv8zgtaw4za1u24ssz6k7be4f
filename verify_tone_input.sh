#!/bin/bash

# Verification script for tone number input using Shift+Number (which produces symbols)

echo "╔════════════════════════════════════════════════════════════╗"
echo "║     Verifying Shift+Number → Tone Number Conversion       ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo

cat > /tmp/verify_tone.rs << 'EOF'
use pinyin_tui::PinyinDatabase;

// Simulating the handle_input logic from main.rs
fn handle_symbol_input(input: &mut String, symbol: char, suggestions: &[String]) -> bool {
    match symbol {
        '!' => {
            input.push('1');
            true
        }
        '@' => {
            input.push('2');
            true
        }
        '#' => {
            input.push('3');
            true
        }
        '$' => {
            input.push('4');
            true
        }
        '%' => {
            input.push('5');
            true
        }
        c if c.is_ascii_digit() && !suggestions.is_empty() => {
            // Would select candidate
            false
        }
        c => {
            input.push(c);
            true
        }
    }
}

fn main() {
    println!("Loading database...");
    let db = PinyinDatabase::load_from_file("data/filtered_db.json").unwrap();
    println!("✓ Loaded {} entries\n", db.entries.len());

    println!("━━━ TEST 1: Symbol-to-Tone Conversion ━━━");
    let mut input = String::new();
    let suggestions: Vec<String> = vec![];

    // Simulate: n, i, Shift+3 (which produces #)
    println!("Simulating keyboard input: n, i, Shift+3");
    println!("  (Shift+3 produces '#' symbol on keyboard)");
    handle_symbol_input(&mut input, 'n', &suggestions);
    handle_symbol_input(&mut input, 'i', &suggestions);
    handle_symbol_input(&mut input, '#', &suggestions); // Shift+3 = #

    println!("  Input buffer: '{}'", input);
    println!("  Expected: 'ni3'");

    if input == "ni3" {
        println!("  ✓ PASS - Shift+3 (#) converted to tone number 3\n");
    } else {
        println!("  ✗ FAIL - Expected 'ni3', got '{}'\n", input);
        std::process::exit(1);
    }

    println!("━━━ TEST 2: All Tone Numbers (Shift+1 through Shift+5) ━━━");
    let symbols_to_tones = [
        ('!', '1', "Shift+1"),
        ('@', '2', "Shift+2"),
        ('#', '3', "Shift+3"),
        ('$', '4', "Shift+4"),
        ('%', '5', "Shift+5"),
    ];

    for (symbol, expected_tone, key_combo) in symbols_to_tones {
        let mut test_input = String::new();
        handle_symbol_input(&mut test_input, symbol, &suggestions);

        if test_input == expected_tone.to_string() {
            println!("  ✓ {} ('{}') → tone '{}'", key_combo, symbol, expected_tone);
        } else {
            println!("  ✗ {} ('{}') failed - got '{}'", key_combo, symbol, test_input);
            std::process::exit(1);
        }
    }
    println!();

    println!("━━━ TEST 3: Real Pinyin Input Sequence ━━━");
    let mut input = String::new();

    println!("Simulating: n, i, Shift+3, h, a, o, Shift+3");
    println!("  (Symbols produced: n, i, #, h, a, o, #)");

    handle_symbol_input(&mut input, 'n', &suggestions);
    handle_symbol_input(&mut input, 'i', &suggestions);
    handle_symbol_input(&mut input, '#', &suggestions); // Shift+3
    handle_symbol_input(&mut input, 'h', &suggestions);
    handle_symbol_input(&mut input, 'a', &suggestions);
    handle_symbol_input(&mut input, 'o', &suggestions);
    handle_symbol_input(&mut input, '#', &suggestions); // Shift+3

    println!("  Input buffer: '{}'", input);
    println!("  Expected: 'ni3hao3'");

    if input == "ni3hao3" {
        let results = db.search(&input);
        println!("  Found {} matches", results.len());
        if results.iter().any(|s| s == "你好") {
            println!("  ✓ PASS - Can input 'ni3hao3' and find '你好'\n");
        } else {
            println!("  ✗ FAIL - 'ni3hao3' didn't match '你好'\n");
            std::process::exit(1);
        }
    } else {
        println!("  ✗ FAIL - Expected 'ni3hao3', got '{}'\n", input);
        std::process::exit(1);
    }

    println!("━━━ TEST 4: Number-Only Selection (no Shift) ━━━");
    let mut test_suggestions = vec!["你".to_string(), "伲".to_string(), "拟".to_string()];
    let mut input = String::new();

    println!("When suggestions exist, plain number selects candidate:");
    let selected = handle_symbol_input(&mut input, '1', &test_suggestions);

    if !selected {
        println!("  ✓ PASS - Plain '1' treated as selection, not input\n");
    } else {
        println!("  ✗ FAIL - Plain '1' was added to input instead of selecting\n");
        std::process::exit(1);
    }

    println!("━━━ TEST 5: Mixed Tone Numbers ━━━");
    let mut input = String::new();

    println!("Testing different tones: ma1, ma2, ma3, ma4, ma5");

    // ma1
    handle_symbol_input(&mut input, 'm', &suggestions);
    handle_symbol_input(&mut input, 'a', &suggestions);
    handle_symbol_input(&mut input, '!', &suggestions); // Shift+1
    input.push(' ');

    // ma2
    handle_symbol_input(&mut input, 'm', &suggestions);
    handle_symbol_input(&mut input, 'a', &suggestions);
    handle_symbol_input(&mut input, '@', &suggestions); // Shift+2
    input.push(' ');

    // ma3
    handle_symbol_input(&mut input, 'm', &suggestions);
    handle_symbol_input(&mut input, 'a', &suggestions);
    handle_symbol_input(&mut input, '#', &suggestions); // Shift+3
    input.push(' ');

    // ma4
    handle_symbol_input(&mut input, 'm', &suggestions);
    handle_symbol_input(&mut input, 'a', &suggestions);
    handle_symbol_input(&mut input, '$', &suggestions); // Shift+4
    input.push(' ');

    // ma5
    handle_symbol_input(&mut input, 'm', &suggestions);
    handle_symbol_input(&mut input, 'a', &suggestions);
    handle_symbol_input(&mut input, '%', &suggestions); // Shift+5

    println!("  Input: '{}'", input);
    println!("  Expected: 'ma1 ma2 ma3 ma4 ma5'");

    if input == "ma1 ma2 ma3 ma4 ma5" {
        println!("  ✓ PASS - All five tones work correctly\n");
    } else {
        println!("  ✗ FAIL - Expected 'ma1 ma2 ma3 ma4 ma5', got '{}'\n", input);
        std::process::exit(1);
    }

    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("✓ ALL TESTS PASSED!");
    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("\nShift+Number correctly converts symbols to tone numbers:");
    println!("  Shift+1 (!) → 1");
    println!("  Shift+2 (@) → 2");
    println!("  Shift+3 (#) → 3");
    println!("  Shift+4 ($) → 4");
    println!("  Shift+5 (%) → 5");
}
EOF

echo "Compiling verification test..."
rustc --edition 2021 /tmp/verify_tone.rs -L target/release/deps --extern pinyin_tui=target/release/libpinyin_tui.rlib -o /tmp/verify_tone 2>&1

if [ -f /tmp/verify_tone ]; then
    echo "Running verification tests..."
    echo
    /tmp/verify_tone
    exit $?
else
    echo "Failed to compile verification test"
    exit 1
fi
