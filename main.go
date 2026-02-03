package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
)

var logger *slog.Logger
var loggingOn bool

func initLogger() {
	if !loggingOn {
		// No-op logger that discards all output
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		return
	}
	f, err := os.OpenFile("lazytodo.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Println("warning: failed to open log file:", err)
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		return
	}
	logger = slog.New(slog.NewJSONHandler(f, nil))
	logger.Info("Logger initialized")
}

var checkboxPattern = regexp.MustCompile(`^(\s*)([-*])\s+\[([ xX])\]\s*(.*)$`)
var sectionPattern = regexp.MustCompile(`^##\s+(.*)$`)
var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

const (
	fileCheckInterval  = time.Second
	defaultInputWidth  = 60
	minInputWidth      = 20
	wrapMargin         = 6
	editorWidthPadding = 10
	defaultWindowWidth = 80
)

type mode int

const (
	modeNormal mode = iota
	modeEdit
)

type editIntent int

const (
	editIntentNone editIntent = iota
	editIntentUpdate
	editIntentInsert
)

type editTarget int

const (
	editTargetTask editTarget = iota
	editTargetSection
)

type fileCheckMsg time.Time

type editorFinishedMsg struct {
	tempFile string
	err      error
}

func getEditor() string {
	if editor := os.Getenv("EDITOR"); editor != "" {
		return editor
	}
	return "vim"
}

type task struct {
	Indent    string
	Bullet    string
	Completed bool
	Text      string
}

func (t task) line() string {
	mark := " "
	if t.Completed {
		mark = "x"
	}
	return fmt.Sprintf("%s%s [%s] %s", t.Indent, t.Bullet, mark, t.Text)
}

type lineKind int

const (
	lineTask lineKind = iota
	lineSection
)

type lineItem struct {
	Kind         lineKind
	Task         task
	SectionTitle string
}

func (l lineItem) line() string {
	switch l.Kind {
	case lineSection:
		return "## " + l.SectionTitle
	default:
		return l.Task.line()
	}
}

type undoState struct {
	lines  []lineItem
	cursor int
}

const maxUndoHistory = 10

// Model is the central application state
type model struct {
	filePath        string
	lines           []lineItem
	cursor          int
	mode            mode
	textInput       textinput.Model
	editIntent      editIntent
	editTarget      editTarget
	editIndex       int
	insertIndex     int
	editTemplate    task
	statusMessage   string
	err             error
	lastModifiedAt  time.Time
	pendingReload   bool
	selectionActive bool
	selectionAnchor int
	renderer        *glamour.TermRenderer
	rendererWidth   int
	windowWidth     int
	windowHeight    int
	externalEditIdx int
	undoStack       []undoState
	redoStack       []undoState
	pendingD        bool
}

func newModel(path string) (model, error) {
	lines, modTime, err := loadLines(path)
	if err != nil {
		return model{}, err
	}
	ti := textinput.New()
	ti.CharLimit = 0
	ti.Placeholder = "Describe the task"
	ti.Prompt = ""
	ti.Width = defaultInputWidth
	rend, err := newMarkdownRenderer(0)
	if err != nil {
		return model{}, err
	}
	return model{
		filePath:        path,
		lines:           lines,
		cursor:          0,
		mode:            modeNormal,
		textInput:       ti,
		editIntent:      editIntentNone,
		editTarget:      editTargetTask,
		editIndex:       -1,
		editTemplate:    defaultTaskTemplate(lines),
		statusMessage:   "",
		err:             nil,
		lastModifiedAt:  modTime,
		selectionActive: false,
		selectionAnchor: 0,
		renderer:        rend,
		rendererWidth:   0,
		windowWidth:     defaultWindowWidth,
		windowHeight:    0,
		externalEditIdx: -1,
	}, nil
}

func defaultTaskTemplate(lines []lineItem) task {
	if len(lines) == 0 {
		return task{Indent: "", Bullet: "-", Completed: false}
	}
	for _, line := range lines {
		if line.Kind == lineTask {
			return task{Indent: line.Task.Indent, Bullet: line.Task.Bullet, Completed: false}
		}
	}
	return task{Indent: "", Bullet: "-", Completed: false}
}

func loadLines(path string) ([]lineItem, time.Time, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// File doesn't exist yet - return empty lines, will be created on first save
		return []lineItem{}, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n")
	var items []lineItem
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sectionMatches := sectionPattern.FindStringSubmatch(line)
		if len(sectionMatches) > 0 {
			items = append(items, lineItem{
				Kind:         lineSection,
				SectionTitle: sectionMatches[1],
			})
			continue
		}
		matches := checkboxPattern.FindStringSubmatch(line)
		if len(matches) == 0 {
			continue
		}
		items = append(items, lineItem{
			Kind: lineTask,
			Task: task{
				Indent:    matches[1],
				Bullet:    matches[2],
				Completed: strings.EqualFold(matches[3], "x"),
				Text:      matches[4],
			},
		})
	}
	info, err := os.Stat(path)
	if err != nil {
		return items, time.Time{}, err
	}
	return items, info.ModTime(), nil
}

func saveLines(path string, lines []lineItem) (time.Time, error) {
	var builder strings.Builder
	for i, line := range lines {
		builder.WriteString(line.line())
		if i < len(lines)-1 {
			builder.WriteString("\n")
		}
	}
	if len(lines) > 0 {
		builder.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return time.Time{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// saveAndSetStatus saves lines to disk and updates model status accordingly
func (m *model) saveAndSetStatus(msg string) {
	modTime, err := saveLines(m.filePath, m.lines)
	if err != nil {
		m.err = err
		return
	}
	m.lastModifiedAt = modTime
	m.statusMessage = msg
	m.err = nil
}

func watchFileCmd() tea.Cmd {
	return tea.Tick(fileCheckInterval, func(t time.Time) tea.Msg {
		return fileCheckMsg(t)
	})
}

func (m model) Init() tea.Cmd {
	return watchFileCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	logger.Debug("Update called", "msg_type", fmt.Sprintf("%T", msg))
	switch msg := msg.(type) {
	case tea.KeyMsg:
		logger.Debug("KeyMsg received", "key", msg.String())
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		logger.Debug("WindowSizeMsg received", "width", msg.Width, "height", msg.Height)
		if msg.Width > 0 {
			m.windowWidth = msg.Width
		}
		if msg.Height > 0 {
			m.windowHeight = msg.Height
		}
		if msg.Width > 0 {
			m = m.applyEditorWidth()
			m = m.ensureRendererWidth(msg.Width)
		}
		return m, nil
	case fileCheckMsg:
		logger.Debug("FileCheckMsg received", "time", time.Time(msg))
		var cmds []tea.Cmd
		m = m.handleFileCheck()
		cmds = append(cmds, watchFileCmd())
		return m, tea.Batch(cmds...)
	case editorFinishedMsg:
		logger.Debug("EditorFinishedMsg received", "error", msg.err)
		return m.handleEditorFinished(msg)
	}
	logger.Debug("Update returning nil")
	return m, nil
}

func (m model) handleFileCheck() model {
	info, err := os.Stat(m.filePath)
	if errors.Is(err, os.ErrNotExist) {
		// File hasn't been created yet, nothing to reload
		return m
	}
	if err != nil {
		m.err = err
		return m
	}
	if !info.ModTime().After(m.lastModifiedAt) {
		return m
	}
	if m.mode == modeEdit {
		m.pendingReload = true
		return m
	}
	lines, modTime, err := loadLines(m.filePath)
	if err != nil {
		m.err = err
		return m
	}
	m.lines = lines
	m.cursor = clampCursor(m.cursor, len(m.lines))
	m = m.normalizeSelection()
	m.lastModifiedAt = modTime
	m.statusMessage = "Reloaded from disk"
	m.err = nil
	m.editTemplate = defaultTaskTemplate(m.lines)
	return m
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeEdit:
		return m.handleEditKey(msg)
	default:
		return m.handleNormalKey(msg)
	}
}

func (m model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Handle 'd' for dd command
	if m.pendingD {
		m.pendingD = false
		if key == "d" {
			return m.deleteCurrentLine()
		}
		// Any other key cancels the pending d
	}

	if msg.Type == tea.KeyEsc {
		if m.selectionActive {
			m.selectionActive = false
			m.statusMessage = "Selection canceled"
		}
		return m, nil
	}
	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "j", "down":
		if len(m.lines) > 0 && m.cursor < len(m.lines)-1 {
			m.cursor++
		}
	case "k", "up":
		if len(m.lines) > 0 && m.cursor > 0 {
			m.cursor--
		}
	case "g":
		m.cursor = 0
	case "G":
		if len(m.lines) > 0 {
			m.cursor = len(m.lines) - 1
		}
	case "d":
		m.pendingD = true
		m.statusMessage = "d-"
	case "u":
		m = m.undo()
		if m.statusMessage == "Undo" {
			m.saveAndSetStatus("Undo")
		}
	case "ctrl+r":
		m = m.redo()
		if m.statusMessage == "Redo" {
			m.saveAndSetStatus("Redo")
		}
	case "enter", " ":
		if len(m.lines) == 0 {
			break
		}
		count := 0
		var lastToggled *task
		if m.selectionActive {
			start, end, ok := m.selectionRange()
			if ok {
				for i := start; i <= end; i++ {
					if m.lines[i].Kind != lineTask {
						continue
					}
					m.lines[i].Task.Completed = !m.lines[i].Task.Completed
					count++
					lastToggled = &m.lines[i].Task
				}
			}
			m.selectionActive = false
		} else {
			if m.lines[m.cursor].Kind == lineTask {
				m.lines[m.cursor].Task.Completed = !m.lines[m.cursor].Task.Completed
				count = 1
				lastToggled = &m.lines[m.cursor].Task
			}
		}
		if count == 0 {
			break
		}
		if count == 1 {
			state := "Incomplete"
			if lastToggled != nil && lastToggled.Completed {
				state = "Completed"
			}
			m.saveAndSetStatus(fmt.Sprintf("Marked %s", state))
		} else {
			m.saveAndSetStatus(fmt.Sprintf("Toggled %d tasks", count))
		}
	case "V", "shift+v":
		if len(m.lines) == 0 {
			break
		}
		if m.selectionActive {
			m.selectionActive = false
			m.statusMessage = "Selection cleared"
		} else {
			m.selectionActive = true
			m.selectionAnchor = m.cursor
			m.statusMessage = "Visual line selection"
		}
	case "e":
		return m.startExternalEdit()
	case "i":
		m = m.startEditCurrent()
	case "o":
		m = m.startInsertTaskAt(m.cursor + 1)
	case "O":
		m = m.startInsertTaskAt(m.cursor)
	case "S":
		m = m.startInsertSectionAt(m.cursor + 1)
	case "r":
		lines, modTime, err := loadLines(m.filePath)
		if err != nil {
			m.err = err
		} else {
			m.lines = lines
			m.cursor = clampCursor(m.cursor, len(m.lines))
			m = m.normalizeSelection()
			m.lastModifiedAt = modTime
			m.editTemplate = defaultTaskTemplate(m.lines)
			m.statusMessage = "Reloaded"
			m.err = nil
		}
	}
	return m, nil
}

// Indentation levels (4 states: none, 4, 8, 12 spaces)
var indentLevels = []string{"", "    ", "        ", "            "}

func getIndentLevel(indent string) int {
	// Normalize tabs to 4 spaces for comparison
	normalized := strings.ReplaceAll(indent, "\t", "    ")
	spaces := len(normalized)
	level := spaces / 4
	if level >= len(indentLevels) {
		level = len(indentLevels) - 1
	}
	return level
}

func (m model) handleEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		// Save current edit and exit edit mode (Obsidian-style)
		rawValue := m.textInput.Value()
		if strings.TrimSpace(rawValue) != "" {
			m = m.applyCurrentEdit(rawValue)
			m.saveAndSetStatus("Saved")
		}
		m = m.exitEditMode()
		return m, nil
	case tea.KeyEnter:
		// Save current edit and optionally create a new task below (Obsidian-style)
		rawValue := m.textInput.Value()
		if strings.TrimSpace(rawValue) == "" {
			// Empty task - just exit edit mode, don't save empty
			m = m.exitEditMode()
			return m, nil
		}

		// Apply the current edit
		m = m.applyCurrentEdit(rawValue)

		// Save to disk
		m.saveAndSetStatus("")

		if m.editTarget == editTargetTask {
			// Start new task below current position
			m.insertIndex = m.cursor + 1
			m.editIndex = m.insertIndex
			m.editIntent = editIntentInsert
			// Keep cursor aligned with the insert line.
			m.cursor = m.editIndex
			m.textInput.SetValue("")
			// Keep focus, stay in edit mode
			return m, nil
		}

		m = m.exitEditMode()
		return m, nil
	case tea.KeyTab:
		if m.editTarget == editTargetTask {
			m = m.changeIndent(1)
			return m, nil
		}
	case tea.KeyShiftTab:
		if m.editTarget == editTargetTask {
			m = m.changeIndent(-1)
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m model) changeIndent(delta int) model {
	// Get the task being edited
	var currentIndent string
	if m.editIntent == editIntentUpdate && m.editIndex >= 0 && m.editIndex < len(m.lines) {
		if m.lines[m.editIndex].Kind != lineTask {
			return m
		}
		currentIndent = m.lines[m.editIndex].Task.Indent
	} else if m.editIntent == editIntentInsert {
		currentIndent = m.editTemplate.Indent
	} else {
		return m
	}

	// Calculate new level
	currentLevel := getIndentLevel(currentIndent)
	newLevel := currentLevel + delta
	if newLevel < 0 {
		newLevel = 0
	}
	if newLevel >= len(indentLevels) {
		newLevel = len(indentLevels) - 1
	}

	newIndent := indentLevels[newLevel]

	// Apply the new indent
	if m.editIntent == editIntentUpdate {
		m.lines[m.editIndex].Task.Indent = newIndent
	} else if m.editIntent == editIntentInsert {
		m.editTemplate.Indent = newIndent
	}

	return m
}

func (m model) deleteCurrentLine() (tea.Model, tea.Cmd) {
	if len(m.lines) == 0 {
		m.statusMessage = "Nothing to delete"
		return m, nil
	}
	if m.lines[m.cursor].Kind == lineSection {
		return m.deleteCurrentSection()
	}
	return m.deleteCurrentTask()
}

func (m model) deleteCurrentTask() (tea.Model, tea.Cmd) {
	if len(m.lines) == 0 || m.lines[m.cursor].Kind != lineTask {
		m.statusMessage = "No task to delete"
		return m, nil
	}

	m = m.saveUndoState()
	m = m.clearSelection()

	// Remove the task at cursor
	m.lines = append(m.lines[:m.cursor], m.lines[m.cursor+1:]...)

	// Adjust cursor if needed
	m.cursor = clampCursor(m.cursor, len(m.lines))

	// Save to file
	m.saveAndSetStatus("Deleted task")

	return m, nil
}

func (m model) deleteCurrentSection() (tea.Model, tea.Cmd) {
	if len(m.lines) == 0 || m.lines[m.cursor].Kind != lineSection {
		m.statusMessage = "No section to delete"
		return m, nil
	}

	m = m.saveUndoState()
	m = m.clearSelection()

	// Remove the section header only.
	m.lines = append(m.lines[:m.cursor], m.lines[m.cursor+1:]...)

	// Move cursor to previous line, if possible.
	m.cursor = clampCursor(m.cursor-1, len(m.lines))

	m.saveAndSetStatus("Deleted section")
	return m, nil
}

// applyCurrentEdit applies the current edit to the model without saving or exiting edit mode
func (m model) applyCurrentEdit(value string) model {
	switch m.editTarget {
	case editTargetSection:
		switch m.editIntent {
		case editIntentUpdate:
			if len(m.lines) == 0 {
				return m
			}
			if m.editIndex >= 0 && m.editIndex < len(m.lines) && m.lines[m.editIndex].Kind == lineSection {
				m = m.saveUndoState()
				m.lines[m.editIndex].SectionTitle = value
			}
		case editIntentInsert:
			newSection := lineItem{
				Kind:         lineSection,
				SectionTitle: value,
			}
			m.insertIndex = clampIndex(m.insertIndex, len(m.lines))
			m = m.saveUndoState()
			m.lines = slices.Insert(m.lines, m.insertIndex, newSection)
			m.cursor = m.insertIndex
		}
	default:
		switch m.editIntent {
		case editIntentUpdate:
			if len(m.lines) == 0 {
				return m
			}
			if m.editIndex >= 0 && m.editIndex < len(m.lines) && m.lines[m.editIndex].Kind == lineTask {
				m.lines[m.editIndex].Task.Text = value
			}
		case editIntentInsert:
			newTask := lineItem{
				Kind: lineTask,
				Task: task{
					Indent:    m.editTemplate.Indent,
					Bullet:    m.editTemplate.Bullet,
					Completed: false,
					Text:      value,
				},
			}
			m.insertIndex = clampIndex(m.insertIndex, len(m.lines))
			m.lines = slices.Insert(m.lines, m.insertIndex, newTask)
			m.cursor = m.insertIndex
		}
	}
	return m
}

// exitEditMode resets transient edit state without saving.
func (m model) exitEditMode() model {
	m.mode = modeNormal
	m.editIntent = editIntentNone
	m.editTarget = editTargetTask
	m.pendingReload = false
	m.editIndex = -1
	// Clamp cursor in case we were inserting at the end and cancelled.
	m.cursor = clampCursor(m.cursor, len(m.lines))
	m.textInput.Reset()
	m.textInput.Blur()
	m = m.normalizeSelection()
	return m
}

func (m model) finishEdit() (tea.Model, tea.Cmd) {
	rawValue := m.textInput.Value()
	if strings.TrimSpace(rawValue) == "" {
		if m.editTarget == editTargetSection {
			m.statusMessage = "Cannot save empty section"
		} else {
			m.statusMessage = "Cannot save empty task"
		}
		return m, nil
	}
	m = m.applyCurrentEdit(rawValue)
	m.saveAndSetStatus("Saved")
	m = m.exitEditMode()
	if m.pendingReload {
		m.pendingReload = false
	}
	return m, nil
}

func (m model) startExternalEdit() (tea.Model, tea.Cmd) {
	if len(m.lines) == 0 || m.lines[m.cursor].Kind != lineTask {
		return m, nil
	}
	m = m.clearSelection()
	m.externalEditIdx = m.cursor

	// Create temp file with the task text
	tmpFile, err := os.CreateTemp("", "lazytodo-edit-*.txt")
	if err != nil {
		m.err = err
		return m, nil
	}
	tmpPath := tmpFile.Name()

	// Write current task text to the temp file
	if _, err := tmpFile.WriteString(m.lines[m.cursor].Task.Text); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		m.err = err
		return m, nil
	}
	tmpFile.Close()

	// Spawn the editor
	editor := getEditor()
	c := exec.Command(editor, tmpPath)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		return editorFinishedMsg{tempFile: tmpPath, err: err}
	})
}

func (m model) handleEditorFinished(msg editorFinishedMsg) (tea.Model, tea.Cmd) {
	defer os.Remove(msg.tempFile)

	if msg.err != nil {
		m.err = msg.err
		m.externalEditIdx = -1
		m.statusMessage = "Editor error"
		return m, nil
	}

	// Read the edited content
	content, err := os.ReadFile(msg.tempFile)
	if err != nil {
		m.err = err
		m.externalEditIdx = -1
		return m, nil
	}

	// Trim whitespace and newlines
	newText := strings.TrimSpace(string(content))

	if newText == "" {
		m.statusMessage = "Cannot save empty task"
		m.externalEditIdx = -1
		return m, nil
	}

	// Update the task
	if m.externalEditIdx >= 0 && m.externalEditIdx < len(m.lines) && m.lines[m.externalEditIdx].Kind == lineTask {
		m.lines[m.externalEditIdx].Task.Text = newText
		m.saveAndSetStatus("Saved")
	}

	m.externalEditIdx = -1
	return m, nil
}

func (m model) startEditCurrent() model {
	if len(m.lines) == 0 {
		return m
	}
	if m.lines[m.cursor].Kind == lineSection {
		return m.startEditSection()
	}
	return m.startEditTask()
}

func (m model) startEditTask() model {
	if len(m.lines) == 0 || m.lines[m.cursor].Kind != lineTask {
		return m
	}
	m = m.clearSelection()
	m.mode = modeEdit
	m.editIntent = editIntentUpdate
	m.editTarget = editTargetTask
	m.textInput.Placeholder = "Describe the task"
	m.textInput.SetValue(m.lines[m.cursor].Task.Text)
	m.textInput.CursorEnd()
	m.textInput.Focus()
	m = m.applyEditorWidth()
	m.editIndex = m.cursor
	m.statusMessage = "Editing current task"
	return m
}

func (m model) startEditSection() model {
	if len(m.lines) == 0 || m.lines[m.cursor].Kind != lineSection {
		return m
	}
	m = m.clearSelection()
	m.mode = modeEdit
	m.editIntent = editIntentUpdate
	m.editTarget = editTargetSection
	m.textInput.Placeholder = "Section title"
	m.textInput.SetValue(m.lines[m.cursor].SectionTitle)
	m.textInput.CursorEnd()
	m.textInput.Focus()
	m = m.applyEditorWidth()
	m.editIndex = m.cursor
	m.statusMessage = "Editing section"
	return m
}

func (m model) startInsertTaskAt(index int) model {
	template := m.editTemplate
	if len(m.lines) > 0 && m.cursor >= 0 && m.cursor < len(m.lines) && m.lines[m.cursor].Kind == lineTask {
		template = task{Indent: m.lines[m.cursor].Task.Indent, Bullet: m.lines[m.cursor].Task.Bullet}
	}
	m = m.clearSelection()
	m.mode = modeEdit
	m.editIntent = editIntentInsert
	m.editTarget = editTargetTask
	if len(m.lines) == 0 {
		m.insertIndex = 0
	} else {
		m.insertIndex = clampIndex(index, len(m.lines))
	}
	m.editIndex = m.insertIndex
	// Move cursor to the insert line so the highlight reflects the edit focus.
	m.cursor = m.editIndex
	m.textInput.Placeholder = "Describe the task"
	m.textInput.SetValue("")
	m.textInput.Focus()
	m = m.applyEditorWidth()
	m.statusMessage = "New task"
	m.editTemplate = template
	return m
}

func (m model) startInsertSectionAt(index int) model {
	m = m.clearSelection()
	m.mode = modeEdit
	m.editIntent = editIntentInsert
	m.editTarget = editTargetSection
	if len(m.lines) == 0 {
		m.insertIndex = 0
	} else {
		m.insertIndex = clampIndex(index, len(m.lines))
	}
	m.editIndex = m.insertIndex
	m.cursor = m.editIndex
	m.textInput.Placeholder = "Section title"
	m.textInput.SetValue("")
	m.textInput.Focus()
	m = m.applyEditorWidth()
	m.statusMessage = "New section"
	return m
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	showEmptyState := m.countTasks() == 0 && !(m.mode == modeEdit && m.editIntent == editIntentInsert && m.editTarget == editTargetTask)
	if showEmptyState {
		b.WriteString("No tasks found. Press 'o' to create one.\n")
	}
	total := len(m.lines)
	for i := 0; i < total; i++ {
		// Render a phantom insert row before the real task so it doesn't get hidden.
		if m.mode == modeEdit && m.editIntent == editIntentInsert && i == m.editIndex {
			if m.editTarget == editTargetSection {
				b.WriteString(m.renderSectionEditorLine(i))
			} else {
				b.WriteString(m.renderEditorLine(m.editTemplate, i))
			}
		}
		if m.mode == modeEdit && m.editIntent == editIntentUpdate && i == m.editIndex {
			if m.editTarget == editTargetSection {
				b.WriteString(m.renderSectionEditorLine(i))
			} else {
				b.WriteString(m.renderEditorLine(m.lines[i].Task, i))
			}
			continue
		}
		suppressCursor := m.mode == modeEdit && m.editIntent == editIntentInsert && i == m.editIndex
		switch m.lines[i].Kind {
		case lineSection:
			b.WriteString(m.renderSectionLine(m.lines[i], i, suppressCursor))
		default:
			b.WriteString(m.renderTaskLine(m.lines[i].Task, i, suppressCursor))
		}
	}
	if m.mode == modeEdit && m.editIntent == editIntentInsert && m.editIndex == total {
		if m.editTarget == editTargetSection {
			b.WriteString(m.renderSectionEditorLine(m.editIndex))
		} else {
			b.WriteString(m.renderEditorLine(m.editTemplate, m.editIndex))
		}
	}
	b.WriteString(m.renderFooter())
	return m.padViewToWindow(b.String())
}

func (m model) renderTaskLine(t task, index int, suppressCursor bool) string {
	body := m.renderMarkdownLine(t.Text)
	indent := strings.ReplaceAll(t.Indent, "\t", "    ")
	checkbox := m.checkboxSymbol(t.Completed)
	lines := strings.Split(body, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	prefix := fmt.Sprintf("%s%s ", indent, checkbox)
	contPrefix := fmt.Sprintf("%s%s", indent, strings.Repeat(" ", len(checkbox)+1))
	var rendered strings.Builder
	for i, line := range lines {
		if i > 0 {
			rendered.WriteString("\n")
		}
		if i == 0 {
			rendered.WriteString(prefix)
		} else {
			rendered.WriteString(contPrefix)
		}
		rendered.WriteString(line)
	}
	return m.formatLine(index, false, suppressCursor, rendered.String())
}

func (m model) renderEditorLine(t task, index int) string {
	indent := strings.ReplaceAll(t.Indent, "\t", "    ")
	prefix := fmt.Sprintf("%s%s ", indent, m.checkboxSymbol(t.Completed))
	content := prefix + m.textInput.View()
	return m.formatLine(index, true, false, content)
}

func (m model) renderSectionLine(line lineItem, index int, suppressCursor bool) string {
	body := fmt.Sprintf("\033[1m%s\033[0m", line.SectionTitle)
	return m.formatSectionLine(index, suppressCursor, body)
}

func (m model) renderSectionEditorLine(index int) string {
	prefix := "  >"
	return prefix + m.textInput.View() + "\n"
}
func (m model) renderMarkdownLine(raw string) string {
	if m.renderer == nil {
		return raw
	}
	rendered, err := m.renderer.Render(raw + "\n")
	if err != nil {
		return raw
	}
	// Glamour adds leading/trailing whitespace and newlines; trim them for alignment
	rendered = strings.Trim(rendered, " \n\t")
	return rendered
}

func (m model) checkboxSymbol(done bool) string {
	if done {
		return "[x]"
	}
	return "[ ]"
}

func (m model) padViewToWindow(view string) string {
	if m.windowHeight <= 0 {
		return view
	}
	lines := strings.Count(view, "\n")
	if !strings.HasSuffix(view, "\n") {
		lines++
	}
	if lines >= m.windowHeight {
		return view
	}
	var b strings.Builder
	b.WriteString(view)
	b.WriteString(strings.Repeat("\n", m.windowHeight-lines))
	return b.String()
}

const (
	// ANSI escape codes for neon yellow background highlighting
	highlightOn  = "\033[48;5;226m\033[30m" // Neon yellow background + black text
	highlightOff = "\033[0m"                // Reset all attributes
	clearToEOL   = "\033[K"                 // Clear from cursor to end of line (fills with current bg)
)

func (m model) formatLine(index int, editing bool, suppressCursor bool, body string) string {
	cursorChar := " "
	if editing {
		cursorChar = ">"
	} else if !suppressCursor && index == m.cursor {
		cursorChar = ">"
	}
	isSelected := !editing && m.isSelected(index)
	lines := strings.Split(body, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	var b strings.Builder
	prefix := fmt.Sprintf("%s  ", cursorChar)
	contPrefix := "   "
	for i, line := range lines {
		if i == 0 {
			if isSelected {
				b.WriteString(highlightOn)
				b.WriteString(prefix)
				b.WriteString(stripANSI(line))
				b.WriteString(clearToEOL)
				b.WriteString(highlightOff)
				b.WriteString("\n")
			} else {
				b.WriteString(prefix)
				b.WriteString(line)
				b.WriteString("\n")
			}
			continue
		}
		if isSelected {
			b.WriteString(highlightOn)
			b.WriteString(contPrefix)
			b.WriteString(stripANSI(line))
			b.WriteString(clearToEOL)
			b.WriteString(highlightOff)
			b.WriteString("\n")
		} else {
			b.WriteString(contPrefix)
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m model) formatSectionLine(index int, suppressCursor bool, body string) string {
	cursorChar := " "
	if !suppressCursor && index == m.cursor {
		cursorChar = ">"
	}
	isSelected := m.isSelected(index)
	prefix := fmt.Sprintf("%s  ", cursorChar)
	if isSelected {
		return highlightOn + prefix + stripANSI(body) + clearToEOL + highlightOff + "\n"
	}
	return prefix + body + "\n"
}

// stripANSI removes ANSI escape codes from a string
func stripANSI(s string) string {
	return ansiEscapePattern.ReplaceAllString(s, "")
}

func (m model) renderHeader() string {
	return fmt.Sprintf("Managing %s\n\n", filepath.Base(m.filePath))
}

func (m model) renderFooter() string {
	// Count completed and open tasks
	completed := 0
	totalTasks := 0
	for _, line := range m.lines {
		if line.Kind != lineTask {
			continue
		}
		totalTasks++
		if line.Task.Completed {
			completed++
		}
	}
	open := totalTasks - completed

	var parts []string
	if m.mode == modeEdit {
		if m.editTarget == editTargetSection {
			parts = append(parts, "Esc save & exit", "Enter save")
		} else {
			parts = append(parts, "Tab/S-Tab indent", "Esc save & exit", "Enter new below")
		}
	} else {
		parts = append(parts, "j/k move", "space toggle", "dd del", "u undo", "^r redo", "e vim", "i inline", "o/O new", "S section", "q quit")
		if m.selectionActive {
			parts = append(parts, "Esc cancel selection")
		}
	}
	status := strings.Join(parts, " · ")
	status += fmt.Sprintf("\n%d open · %d completed", open, completed)
	if m.statusMessage != "" {
		status += " · " + m.statusMessage
	}
	if m.pendingReload {
		status += "\nFile changed on disk; finish editing to reload."
	}
	if m.err != nil {
		status += "\nError: " + m.err.Error()
	}
	return "\n" + status + "\n"
}

func (m model) countTasks() int {
	count := 0
	for _, line := range m.lines {
		if line.Kind == lineTask {
			count++
		}
	}
	return count
}

func clampCursor(cursor int, length int) int {
	if length == 0 {
		return 0
	}
	if cursor < 0 {
		return 0
	}
	if cursor >= length {
		return length - 1
	}
	return cursor
}

func clampIndex(index, length int) int {
	if index < 0 {
		return 0
	}
	if index > length {
		return length
	}
	return index
}

func copyLines(lines []lineItem) []lineItem {
	cp := make([]lineItem, len(lines))
	copy(cp, lines)
	return cp
}

func (m model) saveUndoState() model {
	state := undoState{
		lines:  copyLines(m.lines),
		cursor: m.cursor,
	}
	m.undoStack = append(m.undoStack, state)
	if len(m.undoStack) > maxUndoHistory {
		m.undoStack = m.undoStack[1:]
	}
	// Clear redo stack on new action
	m.redoStack = nil
	return m
}

func (m model) undo() model {
	if len(m.undoStack) == 0 {
		m.statusMessage = "Nothing to undo"
		return m
	}
	// Save current state to redo stack
	redoState := undoState{
		lines:  copyLines(m.lines),
		cursor: m.cursor,
	}
	m.redoStack = append(m.redoStack, redoState)

	// Pop from undo stack
	lastIdx := len(m.undoStack) - 1
	state := m.undoStack[lastIdx]
	m.undoStack = m.undoStack[:lastIdx]

	m.lines = state.lines
	m.cursor = clampCursor(state.cursor, len(m.lines))
	m.statusMessage = "Undo"
	return m
}

func (m model) redo() model {
	if len(m.redoStack) == 0 {
		m.statusMessage = "Nothing to redo"
		return m
	}
	// Save current state to undo stack
	undoState := undoState{
		lines:  copyLines(m.lines),
		cursor: m.cursor,
	}
	m.undoStack = append(m.undoStack, undoState)

	// Pop from redo stack
	lastIdx := len(m.redoStack) - 1
	state := m.redoStack[lastIdx]
	m.redoStack = m.redoStack[:lastIdx]

	m.lines = state.lines
	m.cursor = clampCursor(state.cursor, len(m.lines))
	m.statusMessage = "Redo"
	return m
}

func newMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	wrap := width
	if wrap < 0 {
		wrap = 0
	}
	return glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(wrap),
	)
}

func (m model) ensureRendererWidth(totalWidth int) model {
	wrap := totalWidth - wrapMargin
	if wrap < 0 {
		wrap = 0
	}
	if m.renderer != nil && wrap == m.rendererWidth {
		return m
	}
	renderer, err := newMarkdownRenderer(wrap)
	if err != nil {
		m.err = err
		return m
	}
	m.renderer = renderer
	m.rendererWidth = wrap
	return m
}

func (m model) applyEditorWidth() model {
	if m.windowWidth <= 0 {
		return m
	}
	width := m.windowWidth - editorWidthPadding
	if width < minInputWidth {
		width = minInputWidth
	}
	m.textInput.Width = width
	return m
}

func (m model) selectionRange() (int, int, bool) {
	if !m.selectionActive || len(m.lines) == 0 {
		return 0, 0, false
	}
	anchor := clampCursor(m.selectionAnchor, len(m.lines))
	cursor := clampCursor(m.cursor, len(m.lines))
	if anchor <= cursor {
		return anchor, cursor, true
	}
	return cursor, anchor, true
}

func (m model) isSelected(index int) bool {
	start, end, ok := m.selectionRange()
	if !ok {
		return false
	}
	return index >= start && index <= end
}

func (m model) clearSelection() model {
	if m.selectionActive {
		m.selectionActive = false
	}
	return m
}

func (m model) normalizeSelection() model {
	if !m.selectionActive {
		return m
	}
	if len(m.lines) == 0 {
		m.selectionActive = false
		return m
	}
	if m.selectionAnchor < 0 {
		m.selectionAnchor = 0
	}
	if m.selectionAnchor >= len(m.lines) {
		m.selectionAnchor = len(m.lines) - 1
	}
	return m
}

func main() {
	flag.BoolVar(&loggingOn, "logs", false, "enable debug logging to lazytodo.log")
	flag.Parse()
	initLogger()
	path := ""
	if flag.NArg() == 0 {
		// Use todo.md but don't create it yet - will be created lazily on first action
		path = "todo.md"
	} else {
		path = flag.Arg(0)
		// For explicit paths, still error if missing
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			fmt.Printf("file %s does not exist\n", path)
			os.Exit(1)
		} else if err != nil {
			fmt.Println("error: ", err)
			os.Exit(1)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	m, err := newModel(abs)
	if err != nil {
		fmt.Println("failed to load file:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}
