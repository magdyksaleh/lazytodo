use std::path::PathBuf;
use std::time::SystemTime;

use crate::text_input::TextInput;

// Represents the current UI mode.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    Normal,
    Edit,
}

// Indicates whether we're updating an existing line or inserting a new one.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EditIntent {
    None,
    Update,
    Insert,
}

// Indicates whether we're editing a task or a section header.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EditTarget {
    Task,
    Section,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Task {
    pub indent: String,
    pub bullet: String,
    pub completed: bool,
    pub text: String,
}

impl Task {
    pub fn line(&self) -> String {
        let mark = if self.completed { "x" } else { " " };
        format!("{}{} [{}] {}", self.indent, self.bullet, mark, self.text)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LineItem {
    Task(Task),
    Section { title: String },
}

impl LineItem {
    pub fn line(&self) -> String {
        match self {
            LineItem::Section { title } => format!("## {}", title),
            LineItem::Task(task) => task.line(),
        }
    }

    pub fn is_task(&self) -> bool {
        matches!(self, LineItem::Task(_))
    }

    pub fn is_section(&self) -> bool {
        matches!(self, LineItem::Section { .. })
    }
}

#[derive(Debug, Clone)]
pub struct UndoState {
    pub lines: Vec<LineItem>,
    pub cursor: usize,
}

pub const MAX_UNDO_HISTORY: usize = 10;

// Indentation levels (4 states: none, 4, 8, 12 spaces)
pub const INDENT_LEVELS: [&str; 4] = ["", "    ", "        ", "            "];

#[derive(Debug)]
pub struct App {
    pub file_path: PathBuf,
    pub lines: Vec<LineItem>,
    pub cursor: usize,
    pub mode: Mode,
    pub text_input: TextInput,
    pub input_placeholder: String,
    pub edit_intent: EditIntent,
    pub edit_target: EditTarget,
    pub edit_index: Option<usize>,
    pub insert_index: Option<usize>,
    pub edit_template: Task,
    pub status_message: String,
    pub error: Option<String>,
    pub last_modified: SystemTime,
    pub pending_reload: bool,
    pub selection_active: bool,
    pub selection_anchor: usize,
    pub window_width: u16,
    pub window_height: u16,
    pub renderer_width: usize,
    pub external_edit_idx: Option<usize>,
    pub undo_stack: Vec<UndoState>,
    pub redo_stack: Vec<UndoState>,
    pub pending_d: bool,
    pub scroll_offset: usize,
    pub should_quit: bool,
}
