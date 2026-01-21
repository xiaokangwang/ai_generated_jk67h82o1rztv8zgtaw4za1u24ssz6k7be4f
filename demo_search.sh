#!/bin/bash

# Demonstration of search functionality

echo "╔════════════════════════════════════════════════════════════╗"
echo "║           Pinyin TUI - Search Demonstration               ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo

cat > /tmp/demo_search.rs << 'EOF'
use pinyin_tui::PinyinDatabase;

fn print_results(query: &str, description: &str, db: &PinyinDatabase) {
    println!("┌─ {} (query: '{}')", description, query);
    let results = db.search(query);
    if results.is_empty() {
        println!("│  No results found");
    } else {
        println!("│  Found {} matches:", results.len());
        for (i, result) in results.iter().take(10).enumerate() {
            println!("│    {}. {}", i + 1, result);
        }
        if results.len() > 10 {
            println!("│    ... and {} more", results.len() - 10);
        }
    }
    println!("└─");
    println!();
}

fn main() {
    println!("Loading database...");
    let db = PinyinDatabase::load_from_file("data/filtered_db.json").unwrap();
    println!("✓ Loaded {} entries\n", db.entries.len());

    // Basic single character searches
    println!("━━━ BASIC SINGLE CHARACTERS ━━━");
    print_results("ni3", "Hello - 'you' (你)", &db);
    print_results("wo3", "I/Me (我)", &db);
    print_results("ta1", "He/She/It (他/她/它)", &db);
    print_results("hao3", "Good (好)", &db);

    // Two character phrases
    println!("━━━ TWO CHARACTER PHRASES ━━━");
    print_results("ni3hao3", "Hello (你好)", &db);
    print_results("xie4xie5", "Thank you (谢谢)", &db);
    print_results("zai4jian4", "Goodbye (再见)", &db);

    // Abbreviated pinyin
    println!("━━━ ABBREVIATED PINYIN ━━━");
    print_results("n3h3", "Hello abbreviated (n3h3 = ni3hao3)", &db);
    print_results("x4x5", "Thank you abbreviated", &db);

    // Common words
    println!("━━━ COMMON WORDS ━━━");
    print_results("zhong1guo2", "China (中国)", &db);
    print_results("mei3guo2", "America (美国)", &db);
    print_results("ying1guo2", "England (英国)", &db);

    // Numbers
    println!("━━━ NUMBERS ━━━");
    print_results("yi1", "One (一)", &db);
    print_results("er4", "Two (二)", &db);
    print_results("san1", "Three (三)", &db);

    // Days and time
    println!("━━━ TIME WORDS ━━━");
    print_results("jin1tian1", "Today (今天)", &db);
    print_results("ming2tian1", "Tomorrow (明天)", &db);
    print_results("zuo2tian1", "Yesterday (昨天)", &db);

    println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
    println!("Demonstration complete!");
}
EOF

echo "Compiling demonstration..."
rustc --edition 2021 /tmp/demo_search.rs -L target/release/deps --extern pinyin_tui=target/release/libpinyin_tui.rlib -o /tmp/demo_search 2>&1 | head -5

if [ -f /tmp/demo_search ]; then
    echo "Running search demonstrations..."
    echo
    /tmp/demo_search
else
    echo "Failed to compile demonstration"
    exit 1
fi
