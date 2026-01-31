# lazytodo

lazytodo is a terminal todo manager for Markdown checkbox lists. It lets you navigate, toggle, and edit tasks inside a TUI powered by Bubble Tea.

It's inspired by Obsidian to work over a markdown file in your repo that you can check in.

Uses basic vim keybindings for navigation. Space / Enter to check/uncheck. dd to delete the line. 

![Screenshot of lazytodo TUI](example.png)

## Install

Grab the latest binary release with the installer script:

```bash
curl -fsSL https://raw.githubusercontent.com/magdyksaleh/lazytodo/main/install.sh | sh
```

Pass `--version vX.Y.Z` to lock to a specific tag or `--install-dir` for a custom destination.

## Build from source

```bash
go build -o lazytodo .
```

## Run

```bash
# Run with a specific file
./lazytodo path/to/todo.md

# Run without arguments - creates/opens todo.md in current directory
./lazytodo
```

If you run `lazytodo` without arguments, it will automatically create a `todo.md` file in the current directory if one doesn't already exist.

The application edits the file in place and supports both inline and external editing.
