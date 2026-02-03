use std::fs;
use std::io::{self, Write};
use std::process::Command;

use crossterm::cursor::{Hide, Show};
use crossterm::terminal::{
    disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen,
};
use crossterm::ExecutableCommand;
use tempfile::NamedTempFile;

// Temporarily leaves alt-screen and raw mode so external editors can run normally.
pub struct TerminalSuspend;

impl TerminalSuspend {
    pub fn new() -> io::Result<Self> {
        disable_raw_mode()?;
        let mut stdout = io::stdout();
        stdout.execute(LeaveAlternateScreen)?;
        stdout.execute(Show)?;
        Ok(Self)
    }
}

impl Drop for TerminalSuspend {
    fn drop(&mut self) {
        let _ = enable_raw_mode();
        let mut stdout = io::stdout();
        let _ = stdout.execute(EnterAlternateScreen);
        let _ = stdout.execute(Hide);
    }
}

pub fn edit_in_external_editor(current_text: &str) -> Result<Option<String>, String> {
    let mut tmp = NamedTempFile::new().map_err(|e| e.to_string())?;
    tmp.write_all(current_text.as_bytes())
        .map_err(|e| e.to_string())?;
    let path = tmp.path().to_path_buf();

    let _suspend = TerminalSuspend::new().map_err(|e| e.to_string())?;

    let editor = std::env::var("EDITOR").unwrap_or_else(|_| "vim".to_string());
    let status = Command::new(editor)
        .arg(&path)
        .status()
        .map_err(|e| e.to_string())?;

    if !status.success() {
        return Err("Editor error".to_string());
    }

    let content = fs::read_to_string(&path).map_err(|e| e.to_string())?;
    let trimmed = content.trim().to_string();
    if trimmed.is_empty() {
        return Ok(None);
    }

    Ok(Some(trimmed))
}
