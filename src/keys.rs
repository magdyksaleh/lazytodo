use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};

// Simplified key mapping for parity with the Go key handling.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Key {
    Char(char),
    Enter,
    Esc,
    Up,
    Down,
    Left,
    Right,
    Ctrl(char),
    Backspace,
    Delete,
    Tab,
    BackTab,
    Home,
    End,
    Unknown,
}

pub fn map_key(event: KeyEvent) -> Key {
    match event.code {
        KeyCode::Char(c) => {
            if event.modifiers.contains(KeyModifiers::CONTROL) {
                Key::Ctrl(c.to_ascii_lowercase())
            } else {
                Key::Char(c)
            }
        }
        KeyCode::Enter => Key::Enter,
        KeyCode::Esc => Key::Esc,
        KeyCode::Up => Key::Up,
        KeyCode::Down => Key::Down,
        KeyCode::Left => Key::Left,
        KeyCode::Right => Key::Right,
        KeyCode::Backspace => Key::Backspace,
        KeyCode::Delete => Key::Delete,
        KeyCode::Tab => Key::Tab,
        KeyCode::BackTab => Key::BackTab,
        KeyCode::Home => Key::Home,
        KeyCode::End => Key::End,
        _ => Key::Unknown,
    }
}
