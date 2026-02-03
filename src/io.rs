use std::fs;
use std::path::Path;
use std::time::SystemTime;

use once_cell::sync::Lazy;
use regex::Regex;

use crate::model::{LineItem, Task};

static CHECKBOX_RE: Lazy<Regex> =
    Lazy::new(|| Regex::new(r"^(\s*)([-*])\s+\[([ xX])\]\s*(.*)$").expect("valid checkbox regex"));

static SECTION_RE: Lazy<Regex> =
    Lazy::new(|| Regex::new(r"^##\s+(.*)$").expect("valid section regex"));

pub fn load_lines(path: &Path) -> Result<(Vec<LineItem>, SystemTime), std::io::Error> {
    let data = match fs::read_to_string(path) {
        Ok(contents) => contents,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            return Ok((Vec::new(), SystemTime::UNIX_EPOCH))
        }
        Err(err) => return Err(err),
    };

    // Destructive parsing: only section headers and checkbox tasks are retained.
    let normalized = data.replace('\r', "");
    let mut items = Vec::new();
    for line in normalized.split('\n') {
        if line.trim().is_empty() {
            continue;
        }
        if let Some(caps) = SECTION_RE.captures(line) {
            let title = caps.get(1).map(|m| m.as_str()).unwrap_or("").to_string();
            items.push(LineItem::Section { title });
            continue;
        }
        if let Some(caps) = CHECKBOX_RE.captures(line) {
            let indent = caps.get(1).map(|m| m.as_str()).unwrap_or("").to_string();
            let bullet = caps.get(2).map(|m| m.as_str()).unwrap_or("-").to_string();
            let mark = caps.get(3).map(|m| m.as_str()).unwrap_or(" ");
            let text = caps.get(4).map(|m| m.as_str()).unwrap_or("").to_string();
            items.push(LineItem::Task(Task {
                indent,
                bullet,
                completed: mark.eq_ignore_ascii_case("x"),
                text,
            }));
        }
    }

    let mod_time = fs::metadata(path)
        .and_then(|meta| meta.modified())
        .unwrap_or(SystemTime::UNIX_EPOCH);

    Ok((items, mod_time))
}

pub fn save_lines(path: &Path, lines: &[LineItem]) -> Result<SystemTime, std::io::Error> {
    let mut out = String::new();
    for (i, line) in lines.iter().enumerate() {
        out.push_str(&line.line());
        if i < lines.len() - 1 {
            out.push('\n');
        }
    }
    // Match Go behavior: always end with a newline when non-empty.
    if !lines.is_empty() {
        out.push('\n');
    }
    fs::write(path, out)?;
    let mod_time = fs::metadata(path)?.modified()?;
    Ok(mod_time)
}
