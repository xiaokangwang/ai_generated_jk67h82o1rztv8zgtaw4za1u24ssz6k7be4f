// Test to verify database loading and search functionality
use std::fs::File;
use std::io::{BufRead, BufReader};
use std::collections::HashMap;

type PinyinEntry = Vec<Vec<String>>;

struct PinyinDatabase {
    entries: Vec<(String, PinyinEntry)>,
}

impl PinyinDatabase {
    fn load_from_file(path: &str) -> Result<Self, Box<dyn std::error::Error>> {
        let file = File::open(path)?;
        let reader = BufReader::new(file);
        let mut entries = Vec::new();

        for line in reader.lines() {
            let line = line?;
            if line.trim().is_empty() {
                continue;
            }

            let obj: HashMap<String, serde_json::Value> = serde_json::from_str(&line)?;
            for (hanzi, pinyin_data) in obj {
                if let Some(pinyin_array) = pinyin_data.as_array() {
                    let mut pinyin_entry = Vec::new();
                    for syllable in pinyin_array {
                        if let Some(parts) = syllable.as_array() {
                            let parts: Vec<String> = parts
                                .iter()
                                .filter_map(|v| v.as_str().map(String::from))
                                .collect();
                            if parts.len() == 3 {
                                pinyin_entry.push(parts);
                            }
                        }
                    }
                    entries.push((hanzi, pinyin_entry));
                }
            }
        }

        Ok(PinyinDatabase { entries })
    }

    fn pinyin_to_string(pinyin: &[Vec<String>]) -> String {
        pinyin
            .iter()
            .map(|parts| format!("{}{}{}", parts[0], parts[1], parts[2]))
            .collect::<Vec<_>>()
            .join("")
    }

    fn matches_query(&self, pinyin: &[Vec<String>], query: &str) -> bool {
        let full = Self::pinyin_to_string(pinyin);

        if full.starts_with(query) {
            return true;
        }

        let abbreviated = pinyin
            .iter()
            .map(|parts| {
                let initial = if parts[0].is_empty() {
                    parts[1].chars().next().unwrap_or('_').to_string()
                } else {
                    parts[0].clone()
                };
                format!("{}{}", initial, parts[2])
            })
            .collect::<Vec<_>>()
            .join("");

        abbreviated.starts_with(query)
    }

    fn search(&self, query: &str) -> Vec<String> {
        if query.is_empty() {
            return Vec::new();
        }

        let mut results = Vec::new();
        let query_lower = query.to_lowercase();

        for (hanzi, pinyin) in &self.entries {
            if self.matches_query(pinyin, &query_lower) {
                results.push(hanzi.clone());
                if results.len() >= 50 {
                    break;
                }
            }
        }

        results.sort_by_key(|s| s.len());
        results
    }
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    println!("Loading database...");
    let db = PinyinDatabase::load_from_file("data/filtered_db.json")?;
    println!("✓ Loaded {} entries\n", db.entries.len());

    // Test cases
    let test_cases = vec![
        ("ni3", "你"),
        ("ni3hao3", "你好"),
        ("n3h3", "你好"),
        ("zhong1guo2", "中国"),
        ("z1g2", "中国"),
        ("wo3", "我"),
        ("ta1", "他"),
        ("shi4", "是"),
        ("hao3", "好"),
        ("ren2", "人"),
    ];

    println!("Running search tests:");
    println!("{:<15} {:<10} {:?}", "Query", "Expected", "Results");
    println!("{}", "-".repeat(60));

    let mut passed = 0;
    let mut failed = 0;

    for (query, expected) in test_cases {
        let results = db.search(query);
        let found = results.iter().any(|r| r.contains(expected));

        let status = if found { "✓" } else { "✗" };
        let display_results: Vec<&str> = results.iter().take(5).map(|s| s.as_str()).collect();

        println!("{} {:<13} {:<10} {:?}", status, query, expected, display_results);

        if found {
            passed += 1;
        } else {
            failed += 1;
        }
    }

    println!("\n{}", "=".repeat(60));
    println!("Test Results: {} passed, {} failed", passed, failed);

    if failed == 0 {
        println!("✓ All tests passed!");
    } else {
        println!("✗ Some tests failed");
    }

    Ok(())
}
