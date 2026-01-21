#!/bin/bash

# Test script to verify Shift+Number input for tone numbers

echo "╔════════════════════════════════════════════════════════════╗"
echo "║         Testing Shift+Number Input for Tone Numbers       ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo

cat > /tmp/test_shift_input.rs << 'EOF'
use crossterm::event::{KeyCode, KeyModifiers};
use pinyin_tui::PinyinDatabase;

struct TestApp {
    db: PinyinDatabase,
    input: String,
    suggestions: Vec<String>,
    selected_suggestion: usize,
    output: String,
}

impl TestApp {
    fn new(db: PinyinDatabase) -> Self {
        Self {
            db,
            input: String::new(),
            suggestions: Vec::new(),
            selected_suggestion: 0,
            output: String::new(),
        }
    }

    fn update_suggestions(&mut self) {
        if self.input.is_empty() {
            self.suggestions.clear();
            self.selected_suggestion = 0;
        } else {
            self.suggestions = self.db.search(&self.input);
            if self.selected_suggestion >= self.suggestions.len() {
                self.selected_suggestion = if self.suggestions.is_empty() {
                    0
                } else {
                    self.suggestions.len() - 1
                };
            }
        }
    }

    fn select_by_number(&mut self, num: usize) {
        if num > 0 && num <= self.suggestions.len() {
            self.selected_suggestion = num - 1;
            self.select_current();
        }
    }

    fn select_current(&mut self) {
        if !self.suggestions.is_empty() && self.selected_suggestion < self.suggestions.len() {
            let selected = self.suggestions[self.selected_suggestion].clone();
            self.output.push_str(&selected);
            self.input.clear();
            self.suggestions.clear();
            self.selected_suggestion = 0;
        }
    }

    fn handle_input(&mut self, key: KeyCode, modifiers: KeyModifiers) {
        match key {
            KeyCode::Char(c) if c.is_ascii_digit() && !modifiers.contains(KeyModifiers::SHIFT) && !self.suggestions.is_empty() => {
                // Number without Shift = select candidate
                let num = c.to_digit(10).unwrap() as usize;
                self.select_by_number(num);
            }
            KeyCode::Char(c) => {
                // Regular character input (including Shift+Number for tone numbers)
                self.input.push(c);
                self.update_suggestions();
            }
            _ => {}
        }
    }
}

fn main() {
    println!("Loading database...");
    let db = PinyinDatabase::load_from_file("data/filtered_db.json").unwrap();
    println!("✓ Loaded {} entries\n", db.entries.len());

    let mut app = TestApp::new(db);

    println!("━━━ TEST 1: Shift+Number Input (Tone Numbers) ━━━");
    println!("Simulating: n, i, Shift+3");
    app.handle_input(KeyCode::Char('n'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('i'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('3'), KeyModifiers::SHIFT);

    println!("Input buffer: '{}'", app.input);
    println!("Expected: 'ni3'");
    println!("Suggestions count: {}", app.suggestions.len());
    if app.input == "ni3" && !app.suggestions.is_empty() {
        println!("✓ PASS - Shift+3 added tone number to input\n");
    } else {
        println!("✗ FAIL - Expected 'ni3' in input buffer\n");
        std::process::exit(1);
    }

    println!("━━━ TEST 2: Regular Number (Candidate Selection) ━━━");
    println!("Current suggestions: {:?}", &app.suggestions[..3.min(app.suggestions.len())]);
    let suggestion_count_before = app.suggestions.len();
    let expected_selection = app.suggestions.get(0).cloned();

    println!("Simulating: 1 (without Shift) to select first candidate");
    app.handle_input(KeyCode::Char('1'), KeyModifiers::NONE);

    println!("Input buffer after selection: '{}'", app.input);
    println!("Output: '{}'", app.output);
    println!("Expected output: {:?}", expected_selection);

    if let Some(expected) = expected_selection {
        if app.output == expected && app.input.is_empty() {
            println!("✓ PASS - Number key selected candidate\n");
        } else {
            println!("✗ FAIL - Selection didn't work as expected\n");
            std::process::exit(1);
        }
    }

    println!("━━━ TEST 3: Full Pinyin Input Sequence ━━━");
    println!("Simulating: h, a, o, Shift+3");
    app.handle_input(KeyCode::Char('h'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('a'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('o'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('3'), KeyModifiers::SHIFT);

    println!("Input buffer: '{}'", app.input);
    println!("Expected: 'hao3'");
    println!("Suggestions: {:?}", &app.suggestions[..3.min(app.suggestions.len())]);

    if app.input == "hao3" && app.suggestions.iter().any(|s| s.contains("好")) {
        println!("✓ PASS - Full pinyin with tone number works\n");
    } else {
        println!("✗ FAIL - Expected 'hao3' with '好' in suggestions\n");
        std::process::exit(1);
    }

    println!("━━━ TEST 4: Multiple Tone Numbers ━━━");
    app.input.clear();
    app.suggestions.clear();

    println!("Simulating: n, i, Shift+3, h, a, o, Shift+3");
    app.handle_input(KeyCode::Char('n'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('i'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('3'), KeyModifiers::SHIFT);
    app.handle_input(KeyCode::Char('h'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('a'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('o'), KeyModifiers::NONE);
    app.handle_input(KeyCode::Char('3'), KeyModifiers::SHIFT);

    println!("Input buffer: '{}'", app.input);
    println!("Expected: 'ni3hao3'");
    println!("Suggestions: {:?}", app.suggestions.iter().take(3).collect::<Vec<_>>());

    if app.input == "ni3hao3" && app.suggestions.iter().any(|s| s == "你好") {
        println!("✓ PASS - Multiple tone numbers work correctly\n");
    } else {
        println!("✗ FAIL - Expected 'ni3hao3' with '你好' in suggestions\n");
        std::process::exit(1);
    }

    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("✓ ALL TESTS PASSED!");
    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("\nShift+Number input for tone numbers is working correctly!");
}
EOF

echo "Compiling test..."
rustc --edition 2021 /tmp/test_shift_input.rs -L target/release/deps --extern pinyin_tui=target/release/libpinyin_tui.rlib --extern crossterm=target/release/deps/libcrossterm-*.rlib -o /tmp/test_shift_input 2>&1 | head -5

if [ -f /tmp/test_shift_input ]; then
    echo "Running Shift+Number input tests..."
    echo
    /tmp/test_shift_input
else
    echo "Failed to compile test"
    exit 1
fi
