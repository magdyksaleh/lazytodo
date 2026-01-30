# lazytodo

lazytodo is a terminal todo manager for Markdown checkbox lists. It lets you navigate, toggle, and edit tasks inside a TUI powered by Bubble Tea.

![Screenshot of lazytodo TUI](example.png)

## Install

Grab the latest binary release with the installer script:

```bash
curl -fsSL https://raw.githubusercontent.com/magdyksaleh/lazytodo/main/install.sh | sh
```

Pass `--version vX.Y.Z` to lock to a specific tag or `--install-dir` for a custom destination.

## Build

```bash
go build -o lazytodo .
```

## Run

```bash
./lazytodo path/to/todo.md
```

The application edits the file in place and supports both inline and external editing.
