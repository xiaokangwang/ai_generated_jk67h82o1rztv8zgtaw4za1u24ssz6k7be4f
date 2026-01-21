// Library module for testing core functionality
use serde_json::Value;
use std::{
    collections::HashMap,
    error::Error,
    fs::File,
    io::BufReader,
};

pub type PinyinEntry = Vec<Vec<String>>;

#[derive(Debug)]
pub struct EntryWithFrequency {
    pub hanzi: String,
    pub pinyin: PinyinEntry,
    pub frequency: u32,
}

pub struct PinyinDatabase {
    pub entries: Vec<EntryWithFrequency>,
}

impl PinyinDatabase {
    pub fn load_from_file(path: &str) -> Result<Self, Box<dyn Error>> {
        let file = File::open(path)?;
        let reader = BufReader::new(file);

        // Read entire file as JSON object
        let data: HashMap<String, Value> = serde_json::from_reader(reader)?;

        let mut entries = Vec::new();

        for (hanzi, entry_data) in data {
            if let Some(obj) = entry_data.as_object() {
                // Extract pinyin array
                if let Some(pinyin_array) = obj.get("pinyin").and_then(|v| v.as_array()) {
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

                    // Extract frequency (default to high number if missing)
                    let frequency = obj
                        .get("frequency")
                        .and_then(|v| v.as_u64())
                        .unwrap_or(10000) as u32;

                    entries.push(EntryWithFrequency {
                        hanzi,
                        pinyin: pinyin_entry,
                        frequency,
                    });
                }
            }
        }

        // Sort entries by frequency (lower = more common)
        entries.sort_by_key(|e| e.frequency);

        Ok(PinyinDatabase { entries })
    }

    pub fn pinyin_to_string(pinyin: &[Vec<String>]) -> String {
        pinyin
            .iter()
            .map(|parts| format!("{}{}{}", parts[0], parts[1], parts[2]))
            .collect::<Vec<_>>()
            .join("")
    }

    pub fn matches_query(&self, pinyin: &[Vec<String>], query: &str) -> bool {
        // 1. Full pinyin with tones (e.g., ni3hao3)
        let full = Self::pinyin_to_string(pinyin);
        if full.starts_with(query) {
            return true;
        }

        // 2. Abbreviated with tones (e.g., n3h3)
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

        if abbreviated.starts_with(query) {
            return true;
        }

        // 3. Fuzzy match without tones (e.g., nihao)
        let fuzzy = pinyin
            .iter()
            .map(|parts| format!("{}{}", parts[0], parts[1]))
            .collect::<Vec<_>>()
            .join("");

        fuzzy.starts_with(query)
    }

    fn match_type(&self, pinyin: &[Vec<String>], query: &str) -> Option<u8> {
        // Returns priority: 1 = exact, 2 = abbreviated, 3 = fuzzy, None = no match

        // 1. Full pinyin with tones (highest priority)
        let full = Self::pinyin_to_string(pinyin);
        if full.starts_with(query) {
            return Some(1);
        }

        // 2. Abbreviated with tones
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

        if abbreviated.starts_with(query) {
            return Some(2);
        }

        // 3. Fuzzy match without tones (lowest priority)
        let fuzzy = pinyin
            .iter()
            .map(|parts| format!("{}{}", parts[0], parts[1]))
            .collect::<Vec<_>>()
            .join("");

        if fuzzy.starts_with(query) {
            return Some(3);
        }

        None
    }

    pub fn search(&self, query: &str) -> Vec<String> {
        if query.is_empty() {
            return Vec::new();
        }

        let mut exact_matches = Vec::new();
        let mut abbreviated_matches = Vec::new();
        let mut fuzzy_matches = Vec::new();
        let query_lower = query.to_lowercase();

        for entry in &self.entries {
            match self.match_type(&entry.pinyin, &query_lower) {
                Some(1) => exact_matches.push(entry),
                Some(2) => abbreviated_matches.push(entry),
                Some(3) => fuzzy_matches.push(entry),
                _ => {}
            }

            // Limit total results
            if exact_matches.len() + abbreviated_matches.len() + fuzzy_matches.len() >= 50 {
                break;
            }
        }

        // Sort each group by frequency (lower = more common), then by length
        exact_matches.sort_by_key(|e| (e.frequency, e.hanzi.len()));
        abbreviated_matches.sort_by_key(|e| (e.frequency, e.hanzi.len()));
        fuzzy_matches.sort_by_key(|e| (e.frequency, e.hanzi.len()));

        // Combine: exact first, then abbreviated, then fuzzy
        let mut results: Vec<String> = exact_matches.iter().map(|e| e.hanzi.clone()).collect();
        results.extend(abbreviated_matches.iter().map(|e| e.hanzi.clone()));
        results.extend(fuzzy_matches.iter().map(|e| e.hanzi.clone()));

        results
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_database_loads() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        assert!(db.entries.len() > 100000, "Database should have loaded many entries");
    }

    #[test]
    fn test_search_full_pinyin() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        let results = db.search("ni3");
        assert!(!results.is_empty(), "Should find results for 'ni3'");
        assert!(results.iter().any(|r| r.contains("你")), "Should find 你");
    }

    #[test]
    fn test_search_abbreviated_pinyin() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        let results = db.search("n3");
        assert!(!results.is_empty(), "Should find results for 'n3'");
    }

    #[test]
    fn test_search_multi_character() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        let results = db.search("ni3hao3");
        assert!(results.iter().any(|r| r.contains("你好")), "Should find 你好");
    }

    #[test]
    fn test_search_abbreviated_multi() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        let results = db.search("n3h3");
        assert!(!results.is_empty(), "Should find results for 'n3h3'");
    }

    #[test]
    fn test_empty_search() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        let results = db.search("");
        assert!(results.is_empty(), "Empty query should return no results");
    }

    #[test]
    fn test_common_characters() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();

        let test_cases = vec![
            ("wo3", "我"),
            ("ta1", "他"),
            ("shi4", "是"),  // 是 now exists in the larger database
            ("hao3", "好"),
            ("ren2", "人"),
        ];

        for (query, expected) in test_cases {
            let results = db.search(query);
            assert!(
                results.iter().any(|r| r.contains(expected)),
                "Should find {} for query {}",
                expected,
                query
            );
        }
    }

    #[test]
    fn test_fuzzy_search_single() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        // Search without tone numbers
        let results = db.search("ni");
        assert!(!results.is_empty(), "Should find results for 'ni' (fuzzy)");
        assert!(results.iter().any(|r| r.contains("你")), "Should find 你 with fuzzy search 'ni'");
    }

    #[test]
    fn test_fuzzy_search_multi() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        // Search for "nihao" without tones
        let results = db.search("nihao");
        assert!(!results.is_empty(), "Should find results for 'nihao' (fuzzy)");
        assert!(results.iter().any(|r| r == "你好"), "Should find 你好 with fuzzy search 'nihao'");
    }

    #[test]
    fn test_fuzzy_vs_exact_priority() {
        let db = PinyinDatabase::load_from_file("data/frequency_db.json").unwrap();
        // When searching with tones, exact matches should come first
        let results_exact = db.search("ni3");
        let results_fuzzy = db.search("ni");

        assert!(!results_exact.is_empty(), "Should find results for 'ni3'");
        assert!(!results_fuzzy.is_empty(), "Should find results for 'ni'");

        // Both should find results, fuzzy might have more
        assert!(results_fuzzy.len() >= results_exact.len(), "Fuzzy search may return more results");
    }
}
