use crate::model::{App, EditIntent, EditTarget, LineItem, Mode, Task, INDENT_LEVELS};

impl App {
    pub fn start_edit_current(&mut self) {
        if self.lines.is_empty() {
            return;
        }
        match self.lines.get(self.cursor) {
            Some(LineItem::Section { .. }) => self.start_edit_section(),
            Some(LineItem::Task(_)) => self.start_edit_task(),
            None => {}
        }
    }

    pub fn start_edit_task(&mut self) {
        if self.lines.is_empty() {
            return;
        }
        let text = match self.lines.get(self.cursor) {
            Some(LineItem::Task(task)) => task.text.clone(),
            _ => return,
        };

        self.clear_selection();
        self.mode = Mode::Edit;
        self.edit_intent = EditIntent::Update;
        self.edit_target = EditTarget::Task;
        self.input_placeholder = "Describe the task".to_string();
        self.text_input.set_value(text);
        self.edit_index = Some(self.cursor);
        self.status_message = "Editing current task".to_string();
    }

    pub fn start_edit_section(&mut self) {
        if self.lines.is_empty() {
            return;
        }
        let title = match self.lines.get(self.cursor) {
            Some(LineItem::Section { title }) => title.clone(),
            _ => return,
        };

        self.clear_selection();
        self.mode = Mode::Edit;
        self.edit_intent = EditIntent::Update;
        self.edit_target = EditTarget::Section;
        self.input_placeholder = "Section title".to_string();
        self.text_input.set_value(title);
        self.edit_index = Some(self.cursor);
        self.status_message = "Editing section".to_string();
    }

    pub fn start_insert_task_at(&mut self, index: usize) {
        let mut template = self.edit_template.clone();
        if let Some(LineItem::Task(task)) = self.lines.get(self.cursor) {
            template.indent = task.indent.clone();
            template.bullet = task.bullet.clone();
        }

        self.clear_selection();
        self.mode = Mode::Edit;
        self.edit_intent = EditIntent::Insert;
        self.edit_target = EditTarget::Task;
        self.insert_index = Some(clamp_index(index, self.lines.len()));
        self.edit_index = self.insert_index;
        self.cursor = self.edit_index.unwrap_or(0);
        self.text_input.reset();
        self.input_placeholder = "Describe the task".to_string();
        self.status_message = "New task".to_string();
        self.edit_template = template;
    }

    pub fn start_insert_section_at(&mut self, index: usize) {
        self.clear_selection();
        self.mode = Mode::Edit;
        self.edit_intent = EditIntent::Insert;
        self.edit_target = EditTarget::Section;
        self.insert_index = Some(clamp_index(index, self.lines.len()));
        self.edit_index = self.insert_index;
        self.cursor = self.edit_index.unwrap_or(0);
        self.text_input.reset();
        self.input_placeholder = "Section title".to_string();
        self.status_message = "New section".to_string();
    }

    pub fn apply_current_edit(&mut self, value: &str) {
        match self.edit_target {
            EditTarget::Section => match self.edit_intent {
                EditIntent::Update => {
                    if let Some(idx) = self.edit_index {
                        if matches!(self.lines.get(idx), Some(LineItem::Section { .. })) {
                            self.save_undo_state();
                            if let Some(LineItem::Section { title }) = self.lines.get_mut(idx) {
                                *title = value.to_string();
                            }
                        }
                    }
                }
                EditIntent::Insert => {
                    let new_section = LineItem::Section {
                        title: value.to_string(),
                    };
                    let idx = clamp_index(self.insert_index.unwrap_or(0), self.lines.len());
                    self.save_undo_state();
                    self.lines.insert(idx, new_section);
                    self.cursor = idx;
                }
                EditIntent::None => {}
            },
            EditTarget::Task => match self.edit_intent {
                EditIntent::Update => {
                    if let Some(idx) = self.edit_index {
                        if let Some(LineItem::Task(task)) = self.lines.get_mut(idx) {
                            task.text = value.to_string();
                        }
                    }
                }
                EditIntent::Insert => {
                    let idx = clamp_index(self.insert_index.unwrap_or(0), self.lines.len());
                    let new_task = LineItem::Task(Task {
                        indent: self.edit_template.indent.clone(),
                        bullet: self.edit_template.bullet.clone(),
                        completed: false,
                        text: value.to_string(),
                    });
                    self.lines.insert(idx, new_task);
                    self.cursor = idx;
                }
                EditIntent::None => {}
            },
        }
    }

    pub fn exit_edit_mode(&mut self) {
        self.mode = Mode::Normal;
        self.edit_intent = EditIntent::None;
        self.edit_target = EditTarget::Task;
        self.pending_reload = false;
        self.edit_index = None;
        self.insert_index = None;
        self.cursor = clamp_cursor(self.cursor, self.lines.len());
        self.text_input.reset();
        self.normalize_selection();
    }

    pub fn change_indent(&mut self, delta: isize) {
        if self.edit_target != EditTarget::Task {
            return;
        }

        let current_indent = if self.edit_intent == EditIntent::Update {
            if let Some(idx) = self.edit_index {
                if let Some(LineItem::Task(task)) = self.lines.get(idx) {
                    task.indent.clone()
                } else {
                    return;
                }
            } else {
                return;
            }
        } else if self.edit_intent == EditIntent::Insert {
            self.edit_template.indent.clone()
        } else {
            return;
        };

        let current_level = get_indent_level(&current_indent);
        let mut new_level = current_level as isize + delta;
        if new_level < 0 {
            new_level = 0;
        }
        if new_level as usize >= INDENT_LEVELS.len() {
            new_level = (INDENT_LEVELS.len() - 1) as isize;
        }

        let new_indent = INDENT_LEVELS[new_level as usize].to_string();
        if self.edit_intent == EditIntent::Update {
            if let Some(idx) = self.edit_index {
                if let Some(LineItem::Task(task)) = self.lines.get_mut(idx) {
                    task.indent = new_indent;
                }
            }
        } else if self.edit_intent == EditIntent::Insert {
            self.edit_template.indent = new_indent;
        }
    }
}

fn get_indent_level(indent: &str) -> usize {
    let normalized = indent.replace('\t', "    ");
    let spaces = normalized.len();
    let mut level = spaces / 4;
    if level >= INDENT_LEVELS.len() {
        level = INDENT_LEVELS.len() - 1;
    }
    level
}

pub fn clamp_cursor(cursor: usize, length: usize) -> usize {
    if length == 0 {
        return 0;
    }
    if cursor >= length {
        return length - 1;
    }
    cursor
}

pub fn clamp_index(index: usize, length: usize) -> usize {
    if index > length {
        length
    } else {
        index
    }
}
