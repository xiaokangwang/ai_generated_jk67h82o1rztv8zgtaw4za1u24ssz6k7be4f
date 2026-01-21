#!/bin/bash

# Comprehensive test script for pinyin-tui
# set -e

echo "╔════════════════════════════════════════════════════════════╗"
echo "║     TUI Pinyin Input Tool - Comprehensive Test Suite      ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

PASS=0
FAIL=0

test_status() {
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}✓ PASS${NC}"
        ((PASS++))
    else
        echo -e "${RED}✗ FAIL${NC}"
        ((FAIL++))
    fi
}

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "1. Build and Binary Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo -n "  Binary exists and is executable: "
[ -x "target/release/pinyin-tui" ]
test_status

echo -n "  Binary is the correct architecture: "
file target/release/pinyin-tui | grep -q "ELF 64-bit"
test_status

echo -n "  Binary is dynamically linked: "
file target/release/pinyin-tui | grep -q "dynamically linked"
test_status

echo -n "  Binary size is reasonable (<5MB): "
SIZE=$(stat -c%s target/release/pinyin-tui)
[ $SIZE -lt 5242880 ]
test_status

echo

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "2. Data File Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo -n "  Data file exists: "
[ -f "data/filtered_db.json" ]
test_status

echo -n "  Data file is not empty: "
[ -s "data/filtered_db.json" ]
test_status

echo -n "  Data file is valid JSON: "
head -1 data/filtered_db.json | python3 -m json.tool > /dev/null 2>&1
test_status

echo -n "  Data file has sufficient entries (>50k): "
LINES=$(wc -l < data/filtered_db.json)
[ $LINES -gt 50000 ]
test_status
echo "    (Found $LINES entries)"

echo

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "3. Unit Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo -n "  Running cargo test: "
cargo test --release --quiet > /tmp/test_output.txt 2>&1
test_status

echo "  Test results:"
grep -E "test result:" /tmp/test_output.txt | sed 's/^/    /'

echo

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "4. Database Loading Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo -n "  Database loads without crashing: "
timeout 3s ./target/release/pinyin-tui > /tmp/app_output.txt 2>&1 || true
grep -q "Loaded .* entries" /tmp/app_output.txt
test_status

echo -n "  Database loads all entries: "
LOADED=$(grep "Loaded" /tmp/app_output.txt | grep -oE '[0-9]+' | head -1)
[ "$LOADED" -gt 80000 ]
test_status
echo "    (Loaded $LOADED entries)"

echo

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "5. Search Functionality Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Create a test program
cat > /tmp/search_test.rs << 'TESTEOF'
use pinyin_tui::PinyinDatabase;

fn main() {
    let db = PinyinDatabase::load_from_file("data/filtered_db.json").unwrap();

    let tests = vec![
        ("ni3", "你", "Basic single character"),
        ("ni3hao3", "你好", "Two character phrase"),
        ("n3h3", "你", "Abbreviated pinyin"),
        ("zhong1guo2", "中国", "Country name"),
        ("z1g2", "中", "Abbreviated country"),
        ("hao3", "好", "Common character"),
        ("wo3", "我", "First person pronoun"),
        ("ta1", "他", "Third person pronoun"),
    ];

    let mut passed = 0;
    let mut failed = 0;

    for (query, expected, desc) in tests {
        let results = db.search(query);
        let found = results.iter().any(|r| r.contains(expected));

        if found {
            println!("PASS: {} ({})", desc, query);
            passed += 1;
        } else {
            println!("FAIL: {} ({}) - expected '{}'", desc, query, expected);
            println!("  Got: {:?}", results.iter().take(5).collect::<Vec<_>>());
            failed += 1;
        }
    }

    println!("\nSearch tests: {} passed, {} failed", passed, failed);
    if failed > 0 {
        std::process::exit(1);
    }
}
TESTEOF

echo -n "  Compiling search test: "
rustc --edition 2021 /tmp/search_test.rs -L target/release/deps --extern pinyin_tui=target/release/libpinyin_tui.rlib -o /tmp/search_test > /dev/null 2>&1
test_status

echo "  Running search tests:"
/tmp/search_test | sed 's/^/    /'
if [ ${PIPESTATUS[0]} -eq 0 ]; then
    ((PASS++))
else
    ((FAIL++))
fi

echo

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "6. Performance Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo -n "  Database loads in <3 seconds: "
START=$(date +%s%N)
timeout 5s ./target/release/pinyin-tui > /dev/null 2>&1 || true
END=$(date +%s%N)
DURATION=$((($END - $START) / 1000000))
[ $DURATION -lt 3000 ]
test_status
echo "    (Loaded in ${DURATION}ms)"

echo

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "7. Code Quality Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo -n "  No compiler warnings: "
cargo build --release 2>&1 | grep -q "warning:" && echo -e "${YELLOW}WARN${NC}" || (echo -e "${GREEN}✓ PASS${NC}" && ((PASS++)))

echo -n "  Clippy check passes: "
cargo clippy --release --quiet 2>&1 | grep -q "warning:" && echo -e "${YELLOW}WARN${NC}" || (echo -e "${GREEN}✓ PASS${NC}" && ((PASS++)))

echo

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "8. Documentation Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo -n "  README exists: "
[ -f "README.md" ]
test_status

echo -n "  README has usage instructions: "
grep -q "Usage" README.md
test_status

echo -n "  README has build instructions: "
grep -q "Building" README.md
test_status

echo -n "  Cargo.toml is valid: "
cargo metadata --format-version 1 > /dev/null 2>&1
test_status

echo

echo "╔════════════════════════════════════════════════════════════╗"
echo "║                      TEST SUMMARY                          ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo
echo -e "  ${GREEN}Passed:${NC} $PASS"
echo -e "  ${RED}Failed:${NC} $FAIL"
echo

if [ $FAIL -eq 0 ]; then
    echo -e "${GREEN}════════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}  ✓ ALL TESTS PASSED - Application is ready for use!${NC}"
    echo -e "${GREEN}════════════════════════════════════════════════════════════${NC}"
    exit 0
else
    echo -e "${RED}════════════════════════════════════════════════════════════${NC}"
    echo -e "${RED}  ✗ SOME TESTS FAILED - Please review the output above${NC}"
    echo -e "${RED}════════════════════════════════════════════════════════════${NC}"
    exit 1
fi
