# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

todui is a terminal-based todo list manager for markdown checkbox files. It provides a TUI (Terminal User Interface) using the Bubble Tea framework to interactively manage tasks in markdown files that use the `- [ ]` / `- [x]` checkbox format.

## Build and Run Commands

```bash
# Build the binary
go build -o todui .

# Run the application
./todui <path-to-todo.md>

# Run tests (none currently exist)
go test ./...

# Format code
go fmt ./...

# Run linter
go vet ./...
```

## Architecture

This is a single-file Go application (`main.go`) using the Elm Architecture via Bubble Tea:

- **model struct**: Central application state including file path, tasks, cursor position, edit mode state, selection state, and the glamour markdown renderer
- **task struct**: Represents a single checkbox item with indent, bullet style, completion status, and text
- **mode enum**: `modeNormal` for navigation, `modeEdit` for text input
- **editIntent enum**: Tracks whether editing is for updating an existing task (`editIntentUpdate`) or inserting a new one (`editIntentInsert`)

Key patterns:
- File watching via `tea.Tick` that checks modification time every second
- Visual line selection (Vim-style `V`) for bulk toggle operations
- Markdown rendering of task text using glamour with dynamic width adjustment
- Tasks are parsed from markdown using regex: `^(\s*)([-*])\s+\[([ xX])\]\s*(.*)$`

## Key Bindings (Normal Mode)

- `j/k` or arrows: Navigate
- `Space`/`Enter`: Toggle task completion (works with visual selection)
- `dd`: Delete current task
- `u`: Undo (10-level history)
- `Ctrl+r`: Redo
- `e`: Edit current task in external editor (vim or $EDITOR)
- `i`: Edit current task inline
- `o/O`: Insert new task below/above
- `V`: Start visual line selection
- `g/G`: Jump to first/last task
- `r`: Reload file
- `q`: Quit

## Key Bindings (Edit Mode - inline with `i`)

- `Tab`: Indent task (3 levels max)
- `Shift+Tab`: Unindent task
- `Enter`: Save
- `Esc`: Cancel
