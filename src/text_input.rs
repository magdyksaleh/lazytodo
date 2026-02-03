use unicode_width::UnicodeWidthStr;

// Minimal text input model for inline editing.
#[derive(Debug, Clone)]
pub struct TextInput {
    value: String,
    cursor: usize, // byte index
}

impl TextInput {
    pub fn new() -> Self {
        Self {
            value: String::new(),
            cursor: 0,
        }
    }

    pub fn value(&self) -> &str {
        &self.value
    }

    pub fn set_value(&mut self, value: String) {
        self.value = value;
        self.cursor = self.value.len();
    }

    pub fn reset(&mut self) {
        self.value.clear();
        self.cursor = 0;
    }

    pub fn move_left(&mut self) {
        if self.cursor == 0 {
            return;
        }
        self.cursor = prev_char_boundary(&self.value, self.cursor);
    }

    pub fn move_right(&mut self) {
        if self.cursor >= self.value.len() {
            return;
        }
        self.cursor = next_char_boundary(&self.value, self.cursor);
    }

    pub fn move_home(&mut self) {
        self.cursor = 0;
    }

    pub fn move_end(&mut self) {
        self.cursor = self.value.len();
    }

    pub fn insert_char(&mut self, ch: char) {
        self.value.insert(self.cursor, ch);
        self.cursor = next_char_boundary(&self.value, self.cursor);
    }

    pub fn backspace(&mut self) {
        if self.cursor == 0 {
            return;
        }
        let prev = prev_char_boundary(&self.value, self.cursor);
        self.value.replace_range(prev..self.cursor, "");
        self.cursor = prev;
    }

    pub fn delete(&mut self) {
        if self.cursor >= self.value.len() {
            return;
        }
        let next = next_char_boundary(&self.value, self.cursor);
        self.value.replace_range(self.cursor..next, "");
    }

    // Render the input with a block cursor and optional placeholder.
    pub fn view(&self, placeholder: &str, width: usize) -> String {
        let content = if self.value.is_empty() {
            placeholder.to_string()
        } else {
            self.value.clone()
        };
        let cursor_pos = if self.value.is_empty() {
            0
        } else {
            self.cursor
        };

        if width == 0 {
            return content;
        }

        let char_count = content.chars().count();
        let cursor_char_idx = content[..cursor_pos.min(content.len())].chars().count();

        let (start, end) = if char_count <= width {
            (0, char_count)
        } else {
            let start = cursor_char_idx.saturating_sub(width);
            let end = (start + width).min(char_count);
            (start, end)
        };

        let mut visible = slice_by_char_range(&content, start, end);
        let cursor_in_visible = cursor_char_idx
            .saturating_sub(start)
            .min(visible.chars().count());
        let at_end = cursor_char_idx >= char_count;
        visible = apply_block_cursor(&visible, cursor_in_visible, at_end);

        // Ensure width doesn't exceed constraints (best-effort for ASCII + ANSI).
        trim_to_width(&visible, width)
    }
}

fn prev_char_boundary(s: &str, idx: usize) -> usize {
    let mut prev = 0;
    for (i, _) in s.char_indices() {
        if i >= idx {
            break;
        }
        prev = i;
    }
    prev
}

fn next_char_boundary(s: &str, idx: usize) -> usize {
    for (i, _) in s.char_indices() {
        if i > idx {
            return i;
        }
    }
    s.len()
}

fn slice_by_char_range(s: &str, start: usize, end: usize) -> String {
    s.chars()
        .skip(start)
        .take(end.saturating_sub(start))
        .collect()
}

fn apply_block_cursor(s: &str, cursor_pos: usize, at_end: bool) -> String {
    let mut out = String::new();
    let total = s.chars().count();
    for (i, ch) in s.chars().enumerate() {
        if i == cursor_pos {
            // Reverse video for a block cursor that covers the character.
            out.push_str("\x1b[7m");
            out.push(ch);
            out.push_str("\x1b[0m");
        } else {
            out.push(ch);
        }
    }
    if at_end && cursor_pos >= total {
        out.push_str("\x1b[7m \x1b[0m");
    }
    out
}

fn trim_to_width(s: &str, width: usize) -> String {
    if UnicodeWidthStr::width(s) <= width {
        return s.to_string();
    }
    let mut out = String::new();
    let mut w = 0;
    for ch in s.chars() {
        let cw = UnicodeWidthStr::width(ch.to_string().as_str());
        if w + cw > width {
            break;
        }
        out.push(ch);
        w += cw;
    }
    out
}
