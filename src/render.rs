use std::path::Path;

use once_cell::sync::Lazy;
use regex::Regex;

use crate::markdown::render_markdown_line;
use crate::model::{App, EditIntent, EditTarget, LineItem, Mode, Task};

const WRAP_MARGIN: usize = 6;

// Bright background highlight for visual selection (rough parity with Go).
const HIGHLIGHT_ON: &str = "\x1b[48;5;226m\x1b[30m";
const HIGHLIGHT_OFF: &str = "\x1b[0m";
const MATCH_ON: &str = "\x1b[48;5;24m\x1b[38;5;15m";
const MATCH_OFF: &str = "\x1b[49m\x1b[39m";
const CLEAR_TO_EOL: &str = "\x1b[K";

static ANSI_ESCAPE_RE: Lazy<Regex> =
    Lazy::new(|| Regex::new(r"\x1b\[[0-9;]*m").expect("valid ansi regex"));

impl App {
    pub fn render(&mut self) -> String {
        let mut out = String::new();
        let header = render_header(&self.file_path);
        out.push_str(&header);

        let filter_active = self.search_active() && self.mode != Mode::Edit;
        let visible_indices = self.visible_indices();
        if filter_active {
            self.ensure_cursor_visible_in(&visible_indices);
        }

        let show_empty_state = self.count_tasks() == 0
            && !(self.mode == Mode::Edit
                && self.edit_intent == EditIntent::Insert
                && self.edit_target == EditTarget::Task);
        let show_no_matches = filter_active && visible_indices.is_empty() && !show_empty_state;
        if show_empty_state {
            out.push_str("No tasks found. Press 'o' to create one.\n");
        } else if show_no_matches {
            out.push_str("No matches. Press Esc to clear search.\n");
        }

        let footer = self.render_footer();
        let header_lines = count_lines(&header);
        let empty_lines = if show_empty_state || show_no_matches { 1 } else { 0 };
        let footer_lines = count_lines(&footer);
        let available_items = if self.window_height == 0 {
            usize::MAX
        } else {
            self.window_height as usize
        }
        .saturating_sub(header_lines + empty_lines + footer_lines);

        let mut editor_pos = None;
        if self.mode == Mode::Edit && self.edit_intent == EditIntent::Insert {
            let insert_idx = self.edit_index.unwrap_or(visible_indices.len());
            let pos = visible_indices
                .iter()
                .position(|&i| i >= insert_idx)
                .unwrap_or(visible_indices.len());
            editor_pos = Some(pos);
        }

        let total_items = visible_indices.len() + editor_pos.map(|_| 1).unwrap_or(0);
        let cursor_pos = if total_items == 0 {
            0
        } else if let Some(pos) = editor_pos {
            pos
        } else {
            visible_indices
                .iter()
                .position(|&i| i == self.cursor)
                .unwrap_or(0)
        };

        self.ensure_scroll(total_items, available_items, cursor_pos);
        let start = self.scroll_offset.min(total_items);
        let end = if available_items == usize::MAX {
            total_items
        } else {
            (start + available_items).min(total_items)
        };

        for view_pos in start..end {
            if let Some(edit_pos) = editor_pos {
                if view_pos == edit_pos {
                    if self.edit_target == EditTarget::Section {
                        out.push_str(&self.render_section_editor_line(view_pos));
                    } else {
                        out.push_str(&self.render_editor_line(&self.edit_template, view_pos));
                    }
                    continue;
                }
            }

            let idx = if let Some(edit_pos) = editor_pos {
                if view_pos > edit_pos {
                    visible_indices[view_pos - 1]
                } else {
                    visible_indices[view_pos]
                }
            } else {
                visible_indices[view_pos]
            };

            if self.mode == Mode::Edit
                && self.edit_intent == EditIntent::Update
                && self.edit_index == Some(idx)
            {
                if self.edit_target == EditTarget::Section {
                    out.push_str(&self.render_section_editor_line(idx));
                } else if let LineItem::Task(task) = &self.lines[idx] {
                    out.push_str(&self.render_editor_line(task, idx));
                }
                continue;
            }

            let suppress_cursor = self.mode == Mode::Edit
                && self.edit_intent == EditIntent::Insert
                && self.edit_index == Some(idx);

            match &self.lines[idx] {
                LineItem::Section { title } => {
                    out.push_str(&self.render_section_line(title, idx, suppress_cursor));
                }
                LineItem::Task(task) => {
                    out.push_str(&self.render_task_line(task, idx, suppress_cursor));
                }
            }
        }

        out.push_str(&footer);
        pad_view_to_window(out, self.window_height)
    }

    fn render_task_line(&self, task: &Task, index: usize, suppress_cursor: bool) -> String {
        let mut body = render_markdown_line(&task.text, self.renderer_width);
        if self.search_active() && self.mode != Mode::Edit {
            body = highlight_matches(&body, self.search_query());
        }
        let indent = task.indent.replace('\t', "    ");
        let checkbox = checkbox_symbol(task.completed);

        let mut lines = body.split('\n').collect::<Vec<_>>();
        if lines.is_empty() {
            lines.push("");
        }

        let prefix = format!("{}{} ", indent, checkbox);
        let cont_prefix = format!("{}{}", indent, " ".repeat(checkbox.len() + 1));
        let mut rendered = String::new();
        for (i, line) in lines.iter().enumerate() {
            if i > 0 {
                rendered.push('\n');
            }
            if i == 0 {
                rendered.push_str(&prefix);
            } else {
                rendered.push_str(&cont_prefix);
            }
            rendered.push_str(line);
        }
        format_line(self, index, false, suppress_cursor, &rendered)
    }

    fn render_editor_line(&self, task: &Task, index: usize) -> String {
        let indent = task.indent.replace('\t', "    ");
        let prefix = format!("{}{} ", indent, checkbox_symbol(task.completed));
        let content = format!(
            "{}{}",
            prefix,
            self.text_input
                .view(&self.input_placeholder, self.editor_width())
        );
        format_line(self, index, true, false, &content)
    }

    fn render_section_line(&self, title: &str, index: usize, suppress_cursor: bool) -> String {
        let body = format!("\x1b[1m{}\x1b[0m", title);
        format_section_line(self, index, suppress_cursor, &body)
    }

    fn render_section_editor_line(&self, _index: usize) -> String {
        format!(
            "  >{}\n",
            self.text_input
                .view(&self.input_placeholder, self.editor_width())
        )
    }

    fn render_footer(&self) -> String {
        let mut completed: usize = 0;
        let mut total_tasks: usize = 0;
        for line in &self.lines {
            if let LineItem::Task(task) = line {
                total_tasks += 1;
                if task.completed {
                    completed += 1;
                }
            }
        }
        let open = total_tasks.saturating_sub(completed);

        let mut parts = Vec::new();
        if self.mode == Mode::Edit {
            if self.edit_target == EditTarget::Section {
                parts.extend(["Esc save & exit", "Enter save"]);
            } else {
                parts.extend(["Tab/S-Tab indent", "Esc save & exit", "Enter new below"]);
            }
        } else {
            parts.extend([
                "j/k move",
                "space toggle",
                "dd del",
                "u undo",
                "^r redo",
                "/ search",
                "e vim",
                "i inline",
                "o/O new",
                "S section",
                "q quit",
            ]);
            if self.selection_active {
                parts.push("Esc cancel selection");
            }
            if self.search_active() && self.mode != Mode::Edit {
                parts.push("Esc clear search");
            }
        }

        let mut status = parts.join(" · ");
        status.push_str(&format!("\n{} open · {} completed", open, completed));
        if !self.status_message.is_empty() {
            status.push_str(&format!(" · {}", self.status_message));
        }
        if self.pending_reload {
            status.push_str("\nFile changed on disk; finish editing to reload.");
        }
        if let Some(err) = &self.error {
            status.push_str(&format!("\nError: {}", err));
        }

        let search_line = if self.mode == Mode::Search {
            format!(
                "/{}",
                self.search_input.view("search", self.editor_width())
            )
        } else if self.search_active() && self.mode != Mode::Edit {
            format!("/{}", self.search_query())
        } else {
            String::new()
        };
        if !search_line.is_empty() {
            status = format!("{}\n{}", search_line, status);
        }

        format!("\n{}\n", status)
    }

    pub fn ensure_renderer_width(&mut self, total_width: u16) {
        let wrap = (total_width as usize).saturating_sub(WRAP_MARGIN);
        if wrap == self.renderer_width {
            return;
        }
        self.renderer_width = wrap;
    }

    pub fn editor_width(&self) -> usize {
        let width = self.window_width.saturating_sub(10) as usize;
        width.max(20)
    }

    fn ensure_scroll(&mut self, total_items: usize, visible_items: usize, cursor_pos: usize) {
        if total_items == 0 {
            self.scroll_offset = 0;
            return;
        }
        let visible = visible_items.max(1);
        if cursor_pos < self.scroll_offset {
            self.scroll_offset = cursor_pos;
        } else if cursor_pos >= self.scroll_offset + visible {
            self.scroll_offset = cursor_pos + 1 - visible;
        }
        if self.scroll_offset > total_items.saturating_sub(1) {
            self.scroll_offset = total_items.saturating_sub(1);
        }
    }
}

pub fn render_header(path: &Path) -> String {
    let name = path
        .file_name()
        .and_then(|s| s.to_str())
        .unwrap_or("todo.md");
    format!("Managing {}\n\n", name)
}

fn checkbox_symbol(done: bool) -> &'static str {
    if done {
        "[x]"
    } else {
        "[ ]"
    }
}

fn format_line(
    app: &App,
    index: usize,
    editing: bool,
    suppress_cursor: bool,
    body: &str,
) -> String {
    let cursor_char = if editing || (!suppress_cursor && index == app.cursor) {
        ">"
    } else {
        " "
    };
    let is_selected = !editing && app.is_selected(index);
    let lines: Vec<&str> = body.split('\n').collect();

    let prefix = format!("{}  ", cursor_char);
    let cont_prefix = "   ";
    let mut out = String::new();
    for (i, line) in lines.iter().enumerate() {
        if i == 0 {
            if is_selected {
                // Strip ANSI so the highlight background is uniform.
                out.push_str(HIGHLIGHT_ON);
                out.push_str(&prefix);
                out.push_str(&strip_ansi(line));
                out.push_str(CLEAR_TO_EOL);
                out.push_str(HIGHLIGHT_OFF);
                out.push('\n');
            } else {
                out.push_str(&prefix);
                out.push_str(line);
                out.push('\n');
            }
        } else if is_selected {
            out.push_str(HIGHLIGHT_ON);
            out.push_str(cont_prefix);
            out.push_str(&strip_ansi(line));
            out.push_str(CLEAR_TO_EOL);
            out.push_str(HIGHLIGHT_OFF);
            out.push('\n');
        } else {
            out.push_str(cont_prefix);
            out.push_str(line);
            out.push('\n');
        }
    }
    out
}

fn format_section_line(app: &App, index: usize, suppress_cursor: bool, body: &str) -> String {
    let cursor_char = if !suppress_cursor && index == app.cursor {
        ">"
    } else {
        " "
    };
    let is_selected = app.is_selected(index);
    let prefix = format!("{}  ", cursor_char);
    if is_selected {
        format!(
            "{}{}{}{}{}\n",
            HIGHLIGHT_ON,
            prefix,
            strip_ansi(body),
            CLEAR_TO_EOL,
            HIGHLIGHT_OFF
        )
    } else {
        format!("{}{}\n", prefix, body)
    }
}

fn strip_ansi(input: &str) -> String {
    ANSI_ESCAPE_RE.replace_all(input, "").to_string()
}

fn highlight_matches(rendered: &str, query: &str) -> String {
    if query.is_empty() || rendered.is_empty() {
        return rendered.to_string();
    }

    let plain = strip_ansi(rendered);
    if plain.is_empty() {
        return rendered.to_string();
    }

    let ranges: Vec<(usize, usize)> = plain
        .match_indices(query)
        .map(|(start, _)| (start, start + query.len()))
        .collect();
    if ranges.is_empty() {
        return rendered.to_string();
    }

    let bytes = rendered.as_bytes();
    let mut out = String::with_capacity(rendered.len() + ranges.len() * 12);
    let mut i = 0usize;
    let mut plain_idx = 0usize;
    let mut range_idx = 0usize;
    let mut active = false;

    while i < bytes.len() {
        if bytes[i] == 0x1b && i + 1 < bytes.len() && bytes[i + 1] == b'[' {
            let mut j = i + 2;
            while j < bytes.len() && bytes[j] != b'm' {
                j += 1;
            }
            if j < bytes.len() {
                out.push_str(&rendered[i..=j]);
                i = j + 1;
                if active {
                    out.push_str(MATCH_ON);
                }
                continue;
            }
        }

        let ch = rendered[i..].chars().next().unwrap();
        if range_idx < ranges.len() && plain_idx == ranges[range_idx].0 && !active {
            out.push_str(MATCH_ON);
            active = true;
        }

        out.push(ch);
        plain_idx += ch.len_utf8();

        if active && range_idx < ranges.len() && plain_idx >= ranges[range_idx].1 {
            out.push_str(MATCH_OFF);
            active = false;
            range_idx += 1;
            if range_idx < ranges.len() && plain_idx == ranges[range_idx].0 {
                out.push_str(MATCH_ON);
                active = true;
            }
        }

        i += ch.len_utf8();
    }

    if active {
        out.push_str(MATCH_OFF);
    }

    out
}

fn pad_view_to_window(view: String, window_height: u16) -> String {
    if window_height == 0 {
        return view;
    }
    let mut lines = view.matches('\n').count();
    if !view.ends_with('\n') {
        lines += 1;
    }
    if lines >= window_height as usize {
        return view;
    }
    let mut out = view;
    out.push_str(&"\n".repeat(window_height as usize - lines));
    out
}

fn count_lines(input: &str) -> usize {
    let mut lines = input.matches('\n').count();
    if !input.ends_with('\n') {
        lines += 1;
    }
    lines
}
