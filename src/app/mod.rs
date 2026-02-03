use std::io::{self, Write};
use std::path::PathBuf;
use std::time::{Duration, Instant, SystemTime};

use crossterm::cursor::{Hide, MoveTo, Show};
use crossterm::event::{self, Event};
use crossterm::terminal::{
    disable_raw_mode, enable_raw_mode, Clear, ClearType, EnterAlternateScreen, LeaveAlternateScreen,
};
use crossterm::ExecutableCommand;
use log::debug;

use crate::edit::clamp_cursor;
use crate::external_edit::edit_in_external_editor;
use crate::io::{load_lines, save_lines};
use crate::keys::{map_key, Key};
use crate::model::{
    App, EditIntent, EditTarget, LineItem, Mode, Task, UndoState, MAX_UNDO_HISTORY,
};
use crate::text_input::TextInput;

const FILE_CHECK_INTERVAL: Duration = Duration::from_secs(1);
const DEFAULT_WINDOW_WIDTH: u16 = 80;

impl App {
    pub fn new(path: PathBuf) -> Result<Self, String> {
        let (lines, mod_time) = load_lines(&path).map_err(|e| e.to_string())?;
        let template = default_task_template(&lines);

        Ok(Self {
            file_path: path,
            lines,
            cursor: 0,
            mode: Mode::Normal,
            text_input: TextInput::new(),
            search_input: TextInput::new(),
            input_placeholder: "Describe the task".to_string(),
            edit_intent: EditIntent::None,
            edit_target: EditTarget::Task,
            edit_index: None,
            insert_index: None,
            edit_template: template,
            status_message: String::new(),
            error: None,
            last_modified: mod_time,
            pending_reload: false,
            selection_active: false,
            selection_anchor: 0,
            window_width: DEFAULT_WINDOW_WIDTH,
            window_height: 0,
            renderer_width: 0,
            external_edit_idx: None,
            undo_stack: Vec::new(),
            redo_stack: Vec::new(),
            pending_d: false,
            scroll_offset: 0,
            should_quit: false,
        })
    }

    pub fn run(mut self) -> Result<(), String> {
        let _terminal = TerminalGuard::new().map_err(|e| e.to_string())?;
        self.refresh_window_size();
        self.ensure_renderer_width(self.window_width);
        self.render_to_terminal()?;

        let mut last_file_check = Instant::now();
        let mut dirty = false;

        // Main loop: poll for input, check file changes, and re-render on updates.
        while !self.should_quit {
            let now = Instant::now();
            if now.duration_since(last_file_check) >= FILE_CHECK_INTERVAL {
                self.handle_file_check();
                last_file_check = now;
                dirty = true;
            }

            let timeout = FILE_CHECK_INTERVAL
                .saturating_sub(now.duration_since(last_file_check))
                .min(Duration::from_millis(250));

            if event::poll(timeout).map_err(|e| e.to_string())? {
                match event::read().map_err(|e| e.to_string())? {
                    Event::Key(key_event) => {
                        let key = map_key(key_event);
                        self.handle_key(key);
                        dirty = true;
                    }
                    Event::Resize(w, h) => {
                        self.window_width = w;
                        self.window_height = h;
                        self.ensure_renderer_width(w);
                        dirty = true;
                    }
                    _ => {}
                }
            }

            if dirty {
                self.render_to_terminal()?;
                dirty = false;
            }
        }

        Ok(())
    }

    fn render_to_terminal(&mut self) -> Result<(), String> {
        // Use CRLF so each line starts at column 0 even in raw mode.
        let view = self.render().replace('\n', "\r\n");
        let mut stdout = io::stdout();
        stdout.execute(MoveTo(0, 0)).map_err(|e| e.to_string())?;
        stdout
            .execute(Clear(ClearType::All))
            .map_err(|e| e.to_string())?;
        stdout
            .write_all(view.as_bytes())
            .map_err(|e| e.to_string())?;
        stdout.flush().map_err(|e| e.to_string())
    }

    fn refresh_window_size(&mut self) {
        if let Ok((w, h)) = crossterm::terminal::size() {
            self.window_width = w;
            self.window_height = h;
        }
    }

    fn handle_key(&mut self, key: Key) {
        debug!("Key: {:?}", key);
        match self.mode {
            Mode::Edit => self.handle_edit_key(key),
            Mode::Normal => self.handle_normal_key(key),
            Mode::Search => self.handle_search_key(key),
        }
    }

    fn handle_normal_key(&mut self, key: Key) {
        if self.pending_d {
            self.pending_d = false;
            if key == Key::Char('d') {
                self.delete_current_line();
                return;
            }
        }

        if key == Key::Char('/') {
            self.pending_d = false;
            self.clear_selection();
            self.mode = Mode::Search;
            if self.search_active() {
                self.search_input.insert_char('/');
            } else {
                self.search_input.reset();
            }
            self.ensure_cursor_visible_for_search();
            return;
        }

        if key == Key::Esc {
            let mut cleared = false;
            if self.search_active() {
                self.search_input.reset();
                cleared = true;
            }
            if self.selection_active {
                self.selection_active = false;
                self.status_message = "Selection canceled".to_string();
            }
            if cleared {
                self.status_message = "Search cleared".to_string();
            }
            return;
        }

        match key {
            Key::Ctrl('c') => self.should_quit = true,
            Key::Char('q') => self.should_quit = true,
            Key::Char('j') | Key::Down => self.move_cursor_visible(1),
            Key::Char('k') | Key::Up => self.move_cursor_visible(-1),
            Key::Char('g') => self.move_cursor_to_visible_first(),
            Key::Char('G') => self.move_cursor_to_visible_last(),
            Key::Char('d') => {
                self.pending_d = true;
                self.status_message = "d-".to_string();
            }
            Key::Char('u') => {
                self.undo();
                if self.status_message == "Undo" {
                    self.save_and_set_status("Undo");
                }
            }
            Key::Ctrl('r') => {
                self.redo();
                if self.status_message == "Redo" {
                    self.save_and_set_status("Redo");
                }
            }
            Key::Enter | Key::Char(' ') => self.toggle_tasks(),
            Key::Char('V') | Key::Char('v') => {
                if self.search_active() {
                    self.status_message = "Selection disabled while searching".to_string();
                    return;
                }
                if self.lines.is_empty() {
                    return;
                }
                if self.selection_active {
                    self.selection_active = false;
                    self.status_message = "Selection cleared".to_string();
                } else {
                    self.selection_active = true;
                    self.selection_anchor = self.cursor;
                    self.status_message = "Visual line selection".to_string();
                }
            }
            Key::Char('e') => {
                let _ = self.start_external_edit();
            }
            Key::Char('i') => self.start_edit_current(),
            Key::Char('o') => self.start_insert_task_at(self.cursor + 1),
            Key::Char('O') => self.start_insert_task_at(self.cursor),
            Key::Char('S') => self.start_insert_section_at(self.cursor + 1),
            Key::Char('r') => match load_lines(&self.file_path) {
                Ok((lines, mod_time)) => {
                    self.lines = lines;
                    self.cursor = clamp_cursor(self.cursor, self.lines.len());
                    self.normalize_selection();
                    self.last_modified = mod_time;
                    self.edit_template = default_task_template(&self.lines);
                    self.status_message = "Reloaded".to_string();
                    self.error = None;
                }
                Err(err) => self.error = Some(err.to_string()),
            },
            _ => {}
        }
    }

    fn handle_edit_key(&mut self, key: Key) {
        match key {
            Key::Esc => {
                let raw = self.text_input.value().to_string();
                if !raw.trim().is_empty() {
                    self.apply_current_edit(&raw);
                    self.save_and_set_status("Saved");
                }
                self.exit_edit_mode();
            }
            Key::Enter => {
                let raw = self.text_input.value().to_string();
                if raw.trim().is_empty() {
                    self.exit_edit_mode();
                    return;
                }

                self.apply_current_edit(&raw);
                self.save_and_set_status("");

                if self.edit_target == EditTarget::Task {
                    self.insert_index = Some(self.cursor + 1);
                    self.edit_index = self.insert_index;
                    self.edit_intent = EditIntent::Insert;
                    if let Some(idx) = self.edit_index {
                        self.cursor = idx;
                    }
                    self.text_input.reset();
                    return;
                }

                self.exit_edit_mode();
            }
            Key::Tab => {
                if self.edit_target == EditTarget::Task {
                    self.change_indent(1);
                }
            }
            Key::BackTab => {
                if self.edit_target == EditTarget::Task {
                    self.change_indent(-1);
                }
            }
            Key::Char(c) => self.text_input.insert_char(c),
            Key::Backspace => self.text_input.backspace(),
            Key::Delete => self.text_input.delete(),
            Key::Left => self.text_input.move_left(),
            Key::Right => self.text_input.move_right(),
            Key::Home => self.text_input.move_home(),
            Key::End => self.text_input.move_end(),
            _ => {}
        }
    }

    fn handle_search_key(&mut self, key: Key) {
        match key {
            Key::Esc => {
                self.search_input.reset();
                self.mode = Mode::Normal;
                self.status_message = "Search cleared".to_string();
                self.clear_selection();
            }
            Key::Enter => {
                self.mode = Mode::Normal;
                self.toggle_tasks();
            }
            Key::Char(c) => {
                self.search_input.insert_char(c);
                self.ensure_cursor_visible_for_search();
            }
            Key::Backspace => {
                let remaining = self.search_input.value().chars().count();
                if remaining <= 1 {
                    self.search_input.reset();
                    self.mode = Mode::Normal;
                    self.status_message = "Search cleared".to_string();
                    return;
                }
                self.search_input.backspace();
                self.ensure_cursor_visible_for_search();
            }
            Key::Delete => {
                self.search_input.delete();
                self.ensure_cursor_visible_for_search();
            }
            Key::Left => self.search_input.move_left(),
            Key::Right => self.search_input.move_right(),
            Key::Home => self.search_input.move_home(),
            Key::End => self.search_input.move_end(),
            Key::Up => self.move_cursor_visible(-1),
            Key::Down => self.move_cursor_visible(1),
            _ => {}
        }
    }

    fn toggle_tasks(&mut self) {
        if self.lines.is_empty() {
            return;
        }
        if self.search_active() && self.mode != Mode::Edit {
            let indices = self.visible_indices();
            if indices.is_empty() {
                self.status_message = "No matches".to_string();
                return;
            }
            if !indices.contains(&self.cursor) {
                self.ensure_cursor_visible_in(&indices);
            }
        }
        let mut count = 0;
        let mut last_toggled: Option<bool> = None;

        if self.selection_active {
            if let Some((start, end)) = self.selection_range() {
                for i in start..=end {
                    if let Some(LineItem::Task(task)) = self.lines.get_mut(i) {
                        task.completed = !task.completed;
                        count += 1;
                        last_toggled = Some(task.completed);
                    }
                }
            }
            self.selection_active = false;
        } else if let Some(LineItem::Task(task)) = self.lines.get_mut(self.cursor) {
            task.completed = !task.completed;
            count = 1;
            last_toggled = Some(task.completed);
        }

        if count == 0 {
            return;
        }
        if count == 1 {
            let state = if last_toggled.unwrap_or(false) {
                "Completed"
            } else {
                "Incomplete"
            };
            self.save_and_set_status(&format!("Marked {}", state));
        } else {
            self.save_and_set_status(&format!("Toggled {} tasks", count));
        }
    }

    // Run external editor synchronously while suspending the TUI.
    fn start_external_edit(&mut self) -> Result<(), String> {
        if self.lines.is_empty() {
            return Ok(());
        }
        let task_text = match self.lines.get(self.cursor) {
            Some(LineItem::Task(task)) => task.text.clone(),
            _ => return Ok(()),
        };

        self.clear_selection();
        self.external_edit_idx = Some(self.cursor);

        match edit_in_external_editor(&task_text) {
            Ok(Some(new_text)) => {
                if let Some(idx) = self.external_edit_idx {
                    if let Some(LineItem::Task(task)) = self.lines.get_mut(idx) {
                        task.text = new_text;
                        self.save_and_set_status("Saved");
                    }
                }
            }
            Ok(None) => {
                self.status_message = "Cannot save empty task".to_string();
            }
            Err(err) => {
                self.error = Some(err);
                self.status_message = "Editor error".to_string();
            }
        }

        self.external_edit_idx = None;
        Ok(())
    }

    fn delete_current_line(&mut self) {
        if self.lines.is_empty() {
            self.status_message = "Nothing to delete".to_string();
            return;
        }
        if self.lines[self.cursor].is_section() {
            self.delete_current_section();
        } else {
            self.delete_current_task();
        }
    }

    fn delete_current_task(&mut self) {
        if self.lines.is_empty() || !self.lines[self.cursor].is_task() {
            self.status_message = "No task to delete".to_string();
            return;
        }
        self.save_undo_state();
        self.clear_selection();
        self.lines.remove(self.cursor);
        self.cursor = clamp_cursor(self.cursor, self.lines.len());
        self.save_and_set_status("Deleted task");
    }

    fn delete_current_section(&mut self) {
        if self.lines.is_empty() || !self.lines[self.cursor].is_section() {
            self.status_message = "No section to delete".to_string();
            return;
        }
        self.save_undo_state();
        self.clear_selection();
        self.lines.remove(self.cursor);
        self.cursor = clamp_cursor(self.cursor.saturating_sub(1), self.lines.len());
        self.save_and_set_status("Deleted section");
    }

    fn save_and_set_status(&mut self, msg: &str) {
        match save_lines(&self.file_path, &self.lines) {
            Ok(mod_time) => {
                self.last_modified = mod_time;
                self.status_message = msg.to_string();
                self.error = None;
            }
            Err(err) => self.error = Some(err.to_string()),
        }
    }

    // Poll the file's modification time; reload unless currently editing.
    fn handle_file_check(&mut self) {
        let meta = match std::fs::metadata(&self.file_path) {
            Ok(meta) => meta,
            Err(err) if err.kind() == io::ErrorKind::NotFound => return,
            Err(err) => {
                self.error = Some(err.to_string());
                return;
            }
        };
        let mod_time = match meta.modified() {
            Ok(time) => time,
            Err(err) => {
                self.error = Some(err.to_string());
                return;
            }
        };

        if !is_modified(mod_time, self.last_modified) {
            return;
        }

        if self.mode == Mode::Edit {
            self.pending_reload = true;
            return;
        }

        match load_lines(&self.file_path) {
            Ok((lines, mod_time)) => {
                self.lines = lines;
                self.cursor = clamp_cursor(self.cursor, self.lines.len());
                self.normalize_selection();
                self.last_modified = mod_time;
                self.status_message = "Reloaded from disk".to_string();
                self.error = None;
                self.edit_template = default_task_template(&self.lines);
            }
            Err(err) => self.error = Some(err.to_string()),
        }
    }

    pub fn count_tasks(&self) -> usize {
        self.lines.iter().filter(|l| l.is_task()).count()
    }

    pub fn selection_range(&self) -> Option<(usize, usize)> {
        if !self.selection_active || self.lines.is_empty() {
            return None;
        }
        let anchor = clamp_cursor(self.selection_anchor, self.lines.len());
        let cursor = clamp_cursor(self.cursor, self.lines.len());
        if anchor <= cursor {
            Some((anchor, cursor))
        } else {
            Some((cursor, anchor))
        }
    }

    pub fn is_selected(&self, index: usize) -> bool {
        if let Some((start, end)) = self.selection_range() {
            return index >= start && index <= end;
        }
        false
    }

    pub fn clear_selection(&mut self) {
        if self.selection_active {
            self.selection_active = false;
        }
    }

    pub fn normalize_selection(&mut self) {
        if !self.selection_active {
            return;
        }
        if self.lines.is_empty() {
            self.selection_active = false;
            return;
        }
        if self.selection_anchor >= self.lines.len() {
            self.selection_anchor = self.lines.len() - 1;
        }
    }

    pub(crate) fn search_query(&self) -> &str {
        self.search_input.value()
    }

    pub(crate) fn search_active(&self) -> bool {
        !self.search_input.value().is_empty()
    }

    pub(crate) fn visible_indices(&self) -> Vec<usize> {
        if self.mode == Mode::Edit || !self.search_active() {
            return (0..self.lines.len()).collect();
        }
        let query = self.search_query();
        let mut indices = Vec::new();
        let mut current_section: Option<usize> = None;
        let mut section_included = false;
        for (idx, line) in self.lines.iter().enumerate() {
            match line {
                LineItem::Section { .. } => {
                    current_section = Some(idx);
                    section_included = false;
                }
                LineItem::Task(task) => {
                    if task.text.contains(query) {
                        if let Some(section_idx) = current_section {
                            if !section_included {
                                indices.push(section_idx);
                                section_included = true;
                            }
                        }
                        indices.push(idx);
                    }
                }
            }
        }
        indices
    }

    pub(crate) fn ensure_cursor_visible_for_search(&mut self) {
        if self.mode == Mode::Edit || !self.search_active() {
            return;
        }
        let indices = self.visible_indices();
        self.ensure_cursor_visible_in(&indices);
    }

    pub(crate) fn ensure_cursor_visible_in(&mut self, indices: &[usize]) {
        if indices.is_empty() {
            self.cursor = 0;
            return;
        }
        if indices.contains(&self.cursor) {
            return;
        }
        if let Some(&task_idx) = indices.iter().find(|&&i| self.lines[i].is_task()) {
            self.cursor = task_idx;
        } else {
            self.cursor = indices[0];
        }
    }

    fn move_cursor_visible(&mut self, delta: isize) {
        let indices = self.visible_indices();
        if indices.is_empty() {
            return;
        }
        self.ensure_cursor_visible_in(&indices);
        let Some(pos) = indices.iter().position(|&i| i == self.cursor) else {
            return;
        };
        let new_pos = if delta < 0 {
            pos.saturating_sub((-delta) as usize)
        } else {
            (pos + delta as usize).min(indices.len() - 1)
        };
        self.cursor = indices[new_pos];
    }

    fn move_cursor_to_visible_first(&mut self) {
        let indices = self.visible_indices();
        if let Some(&first) = indices.first() {
            self.cursor = first;
        }
    }

    fn move_cursor_to_visible_last(&mut self) {
        let indices = self.visible_indices();
        if let Some(&last) = indices.last() {
            self.cursor = last;
        }
    }

    pub(crate) fn save_undo_state(&mut self) {
        let state = UndoState {
            lines: self.lines.clone(),
            cursor: self.cursor,
        };
        self.undo_stack.push(state);
        if self.undo_stack.len() > MAX_UNDO_HISTORY {
            self.undo_stack.remove(0);
        }
        self.redo_stack.clear();
    }

    fn undo(&mut self) {
        if self.undo_stack.is_empty() {
            self.status_message = "Nothing to undo".to_string();
            return;
        }
        let redo_state = UndoState {
            lines: self.lines.clone(),
            cursor: self.cursor,
        };
        self.redo_stack.push(redo_state);

        if let Some(state) = self.undo_stack.pop() {
            self.lines = state.lines;
            self.cursor = clamp_cursor(state.cursor, self.lines.len());
            self.status_message = "Undo".to_string();
        }
    }

    fn redo(&mut self) {
        if self.redo_stack.is_empty() {
            self.status_message = "Nothing to redo".to_string();
            return;
        }
        let undo_state = UndoState {
            lines: self.lines.clone(),
            cursor: self.cursor,
        };
        self.undo_stack.push(undo_state);

        if let Some(state) = self.redo_stack.pop() {
            self.lines = state.lines;
            self.cursor = clamp_cursor(state.cursor, self.lines.len());
            self.status_message = "Redo".to_string();
        }
    }
}

fn default_task_template(lines: &[LineItem]) -> Task {
    for line in lines {
        if let LineItem::Task(task) = line {
            return Task {
                indent: task.indent.clone(),
                bullet: task.bullet.clone(),
                completed: false,
                text: String::new(),
            };
        }
    }
    Task {
        indent: String::new(),
        bullet: "-".to_string(),
        completed: false,
        text: String::new(),
    }
}

fn is_modified(current: SystemTime, last: SystemTime) -> bool {
    current.duration_since(last).is_ok()
}

struct TerminalGuard;

impl TerminalGuard {
    fn new() -> io::Result<Self> {
        enable_raw_mode()?;
        let mut stdout = io::stdout();
        stdout.execute(EnterAlternateScreen)?;
        stdout.execute(Hide)?;
        Ok(Self)
    }
}

impl Drop for TerminalGuard {
    fn drop(&mut self) {
        let _ = disable_raw_mode();
        let mut stdout = io::stdout();
        let _ = stdout.execute(LeaveAlternateScreen);
        let _ = stdout.execute(Show);
    }
}
