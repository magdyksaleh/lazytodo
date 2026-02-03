# lazytodo

lazytodo is a terminal todo manager for Markdown checkbox lists. It lets you navigate, toggle, and edit tasks inside a TUI built in Rust with crossterm.

It's inspired by Obsidian to work over a markdown file in your repo that you can check in.

Uses basic vim keybindings.

![Screenshot of lazytodo TUI](example.png)

## Install

Grab the latest binary release with the installer script:

```bash
curl -fsSL https://raw.githubusercontent.com/magdyksaleh/lazytodo/main/install.sh | sh
```

Pass `--version vX.Y.Z` to lock to a specific tag or `--install-dir` for a custom destination.

## Build from source

```bash
cargo build --release
```

## Run

```bash
# Run with a specific file
./target/release/lazytodo path/to/todo.md

# Or run via cargo
cargo run -- path/to/todo.md

# Run without arguments - creates/opens todo.md in current directory
./target/release/lazytodo
```

If you run `lazytodo` without arguments, it will automatically create a `todo.md` file in the current directory if one doesn't already exist.

The application edits the file in place and supports both inline and external editing.

## Search

Press `/` to enter search, type a query, and the list filters to matching tasks. Matches are highlighted and their section headers stay visible. Search is a case-sensitive substring match (no regex). Press `Esc` to clear search.

## Key Bindings
- `j/k` or arrows: Navigate
- `Space`/`Enter`: Toggle task completion (works with visual selection)
- `dd`: Delete current task
- `u`: Undo (10-level history)
- `Ctrl+r`: Redo
- `/`: Search (filters tasks as you type)
- `e`: Edit current task in external editor (vim or $EDITOR)
- `i`: Edit current task inline
- `o/O`: Insert new task below/above
- `S`: Insert a new section below
- `V`: Start visual line selection
- `g/G`: Jump to first/last task
- `r`: Reload file
- `q`: Quit

## Key Bindings (Edit Mode - inline with `i`)

- `Tab`: Indent task (3 levels max)
- `Shift+Tab`: Unindent task
- `Enter`: Save (tasks continue with a new task below)
- `Esc`: Save & exit (or cancel if empty)
