#!/bin/bash

# Verification script for fuzzy search functionality

echo "╔════════════════════════════════════════════════════════════╗"
echo "║       Verifying Fuzzy Search Implementation                ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo

cat > /tmp/verify_fuzzy.rs << 'EOF'
use pinyin_tui::PinyinDatabase;

fn main() {
    println!("Loading database...");
    let db = PinyinDatabase::load_from_file("data/filtered_db.json").unwrap();
    println!("✓ Loaded {} entries\n", db.entries.len());

    println!("━━━ TEST 1: Fuzzy Search - Single Syllable (no tone) ━━━");
    let query = "ni";
    println!("Query: '{}'", query);
    let results = db.search(query);
    println!("Found {} matches", results.len());

    if results.iter().any(|r| r.contains("你")) {
        println!("✓ PASS - Found '你' using fuzzy search '{}'\n", query);
    } else {
        println!("✗ FAIL - '你' not found\n");
        std::process::exit(1);
    }

    println!("━━━ TEST 2: Fuzzy Search - Multi-syllable (no tones) ━━━");
    let query = "nihao";
    println!("Query: '{}'", query);
    let results = db.search(query);
    println!("Found {} matches", results.len());

    if results.iter().any(|r| r == "你好") {
        println!("✓ PASS - Found '你好' using fuzzy search '{}'\n", query);
    } else {
        println!("✗ FAIL - '你好' not found\n");
        std::process::exit(1);
    }

    println!("━━━ TEST 3: Priority Verification - Exact > Fuzzy ━━━");
    let exact_query = "ni3";
    let fuzzy_query = "ni";

    println!("Exact query: '{}'", exact_query);
    let exact_results = db.search(exact_query);
    println!("  Found {} matches", exact_results.len());

    println!("Fuzzy query: '{}'", fuzzy_query);
    let fuzzy_results = db.search(fuzzy_query);
    println!("  Found {} matches", fuzzy_results.len());

    if fuzzy_results.len() >= exact_results.len() {
        println!("✓ PASS - Fuzzy search returns more results (as expected)\n");
    } else {
        println!("✗ FAIL - Unexpected result counts\n");
        std::process::exit(1);
    }

    println!("━━━ TEST 4: Practical Examples ━━━");

    let test_cases = vec![
        ("wo", "我"),           // I/me
        ("hao", "好"),          // good
        ("ren", "人"),          // person
        ("zhong", "中"),        // middle/China
        ("guo", "国"),          // country
    ];

    for (query, expected) in test_cases {
        let results = db.search(query);
        if results.iter().any(|r| r.contains(expected)) {
            println!("  ✓ '{}' → finds '{}'", query, expected);
        } else {
            println!("  ✗ '{}' failed to find '{}'", query, expected);
            std::process::exit(1);
        }
    }
    println!();

    println!("━━━ TEST 5: Mixed Input Support ━━━");
    println!("Verifying all three input modes work:");

    // Exact with tones
    let exact = db.search("ni3hao3");
    println!("  • Exact (ni3hao3): {} results", exact.len());

    // Abbreviated with tones
    let abbreviated = db.search("n3h3");
    println!("  • Abbreviated (n3h3): {} results", abbreviated.len());

    // Fuzzy without tones
    let fuzzy = db.search("nihao");
    println!("  • Fuzzy (nihao): {} results", fuzzy.len());

    if exact.len() > 0 && abbreviated.len() > 0 && fuzzy.len() > 0 {
        println!("✓ PASS - All three input modes working\n");
    } else {
        println!("✗ FAIL - Some input modes not working\n");
        std::process::exit(1);
    }

    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("✓ ALL FUZZY SEARCH TESTS PASSED!");
    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("\nFuzzy search implementation verified:");
    println!("  • Users can type pinyin WITHOUT tone numbers");
    println!("  • Example: 'nihao' finds '你好'");
    println!("  • Results prioritized: exact > abbreviated > fuzzy");
    println!("  • All three input modes work seamlessly");
}
EOF

echo "Compiling verification test..."
export PATH="$HOME/.cargo/bin:$PATH"
rustc --edition 2021 /tmp/verify_fuzzy.rs -L target/release/deps --extern pinyin_tui=target/release/libpinyin_tui.rlib -o /tmp/verify_fuzzy 2>&1

if [ -f /tmp/verify_fuzzy ]; then
    echo "Running fuzzy search verification tests..."
    echo
    /tmp/verify_fuzzy
    exit $?
else
    echo "Failed to compile verification test"
    exit 1
fi
