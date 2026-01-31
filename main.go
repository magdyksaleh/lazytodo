package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
)

var logger *slog.Logger

func initLogger() error {
	f, err := os.OpenFile("lazytodo.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %v", err)
	}
	logger = slog.New(slog.NewJSONHandler(f, nil))
	logger.Info("Logger initialized")
	return nil
}

var checkboxPattern = regexp.MustCompile(`^(\s*)([-*])\s+\[([ xX])\]\s*(.*)$`)

const fileCheckInterval = time.Second

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

type undoState struct {
	tasks  []task
	cursor int
}

const maxUndoHistory = 10

// Model is the central application state
type model struct {
	filePath        string
	tasks           []task
	cursor          int
	mode            mode
	textInput       textinput.Model
	editIntent      editIntent
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
	t, modTime, err := loadTasks(path)
	if err != nil {
		return model{}, err
	}
	ti := textinput.New()
	ti.CharLimit = 0
	ti.Placeholder = "Describe the task"
	ti.Prompt = ""
	ti.Width = 60
	rend, err := newMarkdownRenderer(0)
	if err != nil {
		return model{}, err
	}
	return model{
		filePath:        path,
		tasks:           t,
		cursor:          0,
		mode:            modeNormal,
		textInput:       ti,
		editIntent:      editIntentNone,
		editIndex:       -1,
		editTemplate:    defaultTaskTemplate(t),
		statusMessage:   "",
		err:             nil,
		lastModifiedAt:  modTime,
		selectionActive: false,
		selectionAnchor: 0,
		renderer:        rend,
		rendererWidth:   0,
		windowWidth:     80,
		windowHeight:    0,
		externalEditIdx: -1,
	}, nil
}

func defaultTaskTemplate(tasks []task) task {
	if len(tasks) == 0 {
		return task{Indent: "", Bullet: "-", Completed: false}
	}
	return task{Indent: tasks[0].Indent, Bullet: tasks[0].Bullet, Completed: false}
}

func loadTasks(path string) ([]task, time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n")
	var tasks []task
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		matches := checkboxPattern.FindStringSubmatch(line)
		if len(matches) == 0 {
			continue
		}
		tasks = append(tasks, task{
			Indent:    matches[1],
			Bullet:    matches[2],
			Completed: strings.EqualFold(matches[3], "x"),
			Text:      matches[4],
		})
	}
	info, err := os.Stat(path)
	if err != nil {
		return tasks, time.Time{}, err
	}
	return tasks, info.ModTime(), nil
}

func saveTasks(path string, tasks []task) (time.Time, error) {
	var builder strings.Builder
	for i, task := range tasks {
		builder.WriteString(task.line())
		if i < len(tasks)-1 {
			builder.WriteString("\n")
		}
	}
	if len(tasks) > 0 {
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
	tasks, modTime, err := loadTasks(m.filePath)
	if err != nil {
		m.err = err
		return m
	}
	m.tasks = tasks
	m.cursor = clampCursor(m.cursor, len(m.tasks))
	m = m.normalizeSelection()
	m.lastModifiedAt = modTime
	m.statusMessage = "Reloaded from disk"
	m.err = nil
	m.editTemplate = defaultTaskTemplate(m.tasks)
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
			return m.deleteCurrentTask()
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
		if len(m.tasks) > 0 && m.cursor < len(m.tasks)-1 {
			m.cursor++
		}
	case "k", "up":
		if len(m.tasks) > 0 && m.cursor > 0 {
			m.cursor--
		}
	case "g":
		m.cursor = 0
	case "G":
		if len(m.tasks) > 0 {
			m.cursor = len(m.tasks) - 1
		}
	case "d":
		m.pendingD = true
		m.statusMessage = "d-"
	case "u":
		m = m.undo()
		if m.statusMessage == "Undo" {
			modTime, err := saveTasks(m.filePath, m.tasks)
			if err != nil {
				m.err = err
			} else {
				m.lastModifiedAt = modTime
				m.err = nil
			}
		}
	case "ctrl+r":
		m = m.redo()
		if m.statusMessage == "Redo" {
			modTime, err := saveTasks(m.filePath, m.tasks)
			if err != nil {
				m.err = err
			} else {
				m.lastModifiedAt = modTime
				m.err = nil
			}
		}
	case "enter", " ":
		if len(m.tasks) == 0 {
			break
		}
		count := 0
		if m.selectionActive {
			start, end, ok := m.selectionRange()
			if ok {
				for i := start; i <= end; i++ {
					m.tasks[i].Completed = !m.tasks[i].Completed
				}
				count = end - start + 1
			}
			m.selectionActive = false
		} else {
			m.tasks[m.cursor].Completed = !m.tasks[m.cursor].Completed
			count = 1
		}
		modTime, err := saveTasks(m.filePath, m.tasks)
		if err != nil {
			m.err = err
		} else {
			m.lastModifiedAt = modTime
			if count == 1 {
				state := "Incomplete"
				if m.tasks[m.cursor].Completed {
					state = "Completed"
				}
				m.statusMessage = fmt.Sprintf("Marked %s", state)
			} else {
				m.statusMessage = fmt.Sprintf("Toggled %d tasks", count)
			}
			m.err = nil
		}
	case "V", "shift+v":
		if len(m.tasks) == 0 {
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
		m = m.startEditExisting()
	case "o":
		m = m.startInsertAt(m.cursor + 1)
	case "O":
		m = m.startInsertAt(m.cursor)
	case "r":
		tasks, modTime, err := loadTasks(m.filePath)
		if err != nil {
			m.err = err
		} else {
			m.tasks = tasks
			m.cursor = clampCursor(m.cursor, len(m.tasks))
			m = m.normalizeSelection()
			m.lastModifiedAt = modTime
			m.editTemplate = defaultTaskTemplate(m.tasks)
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
		m.mode = modeNormal
		m.statusMessage = "Edit canceled"
		m.editIntent = editIntentNone
		m.pendingReload = false
		m.editIndex = -1
		m.textInput.Reset()
		m.textInput.Blur()
		return m, nil
	case tea.KeyEnter:
		return m.finishEdit()
	case tea.KeyTab:
		m = m.changeIndent(1)
		return m, nil
	case tea.KeyShiftTab:
		m = m.changeIndent(-1)
		return m, nil
	}
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m model) changeIndent(delta int) model {
	// Get the task being edited
	var currentIndent string
	if m.editIntent == editIntentUpdate && m.editIndex >= 0 && m.editIndex < len(m.tasks) {
		currentIndent = m.tasks[m.editIndex].Indent
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
		m.tasks[m.editIndex].Indent = newIndent
	} else if m.editIntent == editIntentInsert {
		m.editTemplate.Indent = newIndent
	}

	return m
}

func (m model) deleteCurrentTask() (tea.Model, tea.Cmd) {
	if len(m.tasks) == 0 {
		m.statusMessage = "No tasks to delete"
		return m, nil
	}

	m = m.saveUndoState()
	m = m.clearSelection()

	// Remove the task at cursor
	m.tasks = append(m.tasks[:m.cursor], m.tasks[m.cursor+1:]...)

	// Adjust cursor if needed
	m.cursor = clampCursor(m.cursor, len(m.tasks))

	// Save to file
	modTime, err := saveTasks(m.filePath, m.tasks)
	if err != nil {
		m.err = err
	} else {
		m.lastModifiedAt = modTime
		m.statusMessage = "Deleted task"
		m.err = nil
	}

	return m, nil
}

func (m model) finishEdit() (tea.Model, tea.Cmd) {
	rawValue := m.textInput.Value()
	if strings.TrimSpace(rawValue) == "" {
		m.statusMessage = "Cannot save empty task"
		return m, nil
	}
	value := rawValue
	switch m.editIntent {
	case editIntentUpdate:
		if len(m.tasks) == 0 {
			break
		}
		m.tasks[m.cursor].Text = value
	case editIntentInsert:
		newTask := task{
			Indent:    m.editTemplate.Indent,
			Bullet:    m.editTemplate.Bullet,
			Completed: false,
			Text:      value,
		}
		if m.insertIndex < 0 || m.insertIndex > len(m.tasks) {
			m.insertIndex = len(m.tasks)
		}
		if m.insertIndex < 0 {
			m.insertIndex = 0
		}
		if m.insertIndex > len(m.tasks) {
			m.insertIndex = len(m.tasks)
		}
		m.tasks = append(m.tasks[:m.insertIndex], append([]task{newTask}, m.tasks[m.insertIndex:]...)...)
		m.cursor = m.insertIndex
	}
	modTime, err := saveTasks(m.filePath, m.tasks)
	if err != nil {
		m.err = err
	} else {
		m.lastModifiedAt = modTime
		m.statusMessage = "Saved"
		m.err = nil
	}
	m.mode = modeNormal
	m.editIntent = editIntentNone
	m.textInput.Reset()
	m.textInput.Blur()
	m.editIndex = -1
	if m.pendingReload {
		m.pendingReload = false
	}
	m = m.normalizeSelection()
	return m, nil
}

func (m model) startExternalEdit() (tea.Model, tea.Cmd) {
	if len(m.tasks) == 0 {
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
	if _, err := tmpFile.WriteString(m.tasks[m.cursor].Text); err != nil {
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
	if m.externalEditIdx >= 0 && m.externalEditIdx < len(m.tasks) {
		m.tasks[m.externalEditIdx].Text = newText
		modTime, err := saveTasks(m.filePath, m.tasks)
		if err != nil {
			m.err = err
		} else {
			m.lastModifiedAt = modTime
			m.statusMessage = "Saved"
			m.err = nil
		}
	}

	m.externalEditIdx = -1
	return m, nil
}

func (m model) startEditExisting() model {
	if len(m.tasks) == 0 {
		return m
	}
	m = m.clearSelection()
	m.mode = modeEdit
	m.editIntent = editIntentUpdate
	m.textInput.SetValue(m.tasks[m.cursor].Text)
	m.textInput.CursorEnd()
	m.textInput.Focus()
	m = m.applyEditorWidth()
	m.editIndex = m.cursor
	m.statusMessage = "Editing current task"
	return m
}

func (m model) startInsertAt(index int) model {
	template := m.editTemplate
	if len(m.tasks) > 0 && m.cursor >= 0 && m.cursor < len(m.tasks) {
		template = task{Indent: m.tasks[m.cursor].Indent, Bullet: m.tasks[m.cursor].Bullet}
	}
	m = m.clearSelection()
	m.mode = modeEdit
	m.editIntent = editIntentInsert
	if len(m.tasks) == 0 {
		m.insertIndex = 0
	} else {
		m.insertIndex = clampIndex(index, len(m.tasks))
	}
	m.editIndex = m.insertIndex
	m.textInput.SetValue("")
	m.textInput.Focus()
	m = m.applyEditorWidth()
	m.statusMessage = "New task"
	m.editTemplate = template
	return m
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	showEmptyState := len(m.tasks) == 0 && !(m.mode == modeEdit && m.editIntent == editIntentInsert)
	if showEmptyState {
		b.WriteString("No tasks found. Press 'o' to create one.\n")
	}
	total := len(m.tasks)
	for i := 0; i <= total; i++ {
		if m.mode == modeEdit && m.editIntent == editIntentInsert && i == m.editIndex {
			b.WriteString(m.renderEditorLine(m.editTemplate, i))
			continue
		}
		if i == total {
			break
		}
		if m.mode == modeEdit && m.editIntent == editIntentUpdate && i == m.editIndex {
			b.WriteString(m.renderEditorLine(m.tasks[i], i))
			continue
		}
		b.WriteString(m.renderTaskLine(m.tasks[i], i))
	}
	b.WriteString(m.renderFooter())
	return m.padViewToWindow(b.String())
}

func (m model) renderTaskLine(t task, index int) string {
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
	return m.formatLine(index, false, rendered.String())
}

func (m model) renderEditorLine(t task, index int) string {
	indent := strings.ReplaceAll(t.Indent, "\t", "    ")
	prefix := fmt.Sprintf("%s%s ", indent, m.checkboxSymbol(t.Completed))
	content := prefix + m.textInput.View()
	return m.formatLine(index, true, content)
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

func (m model) formatLine(index int, editing bool, body string) string {
	cursorChar := " "
	if editing {
		cursorChar = ">"
	} else if index == m.cursor {
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

// stripANSI removes ANSI escape codes from a string
func stripANSI(s string) string {
	ansiEscape := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansiEscape.ReplaceAllString(s, "")
}

func (m model) renderHeader() string {
	return fmt.Sprintf("Managing %s\n\n", filepath.Base(m.filePath))
}

func (m model) renderFooter() string {
	// Count completed and open tasks
	completed := 0
	for _, t := range m.tasks {
		if t.Completed {
			completed++
		}
	}
	open := len(m.tasks) - completed

	var parts []string
	if m.mode == modeEdit {
		parts = append(parts, "Tab/S-Tab indent", "Esc cancel", "Enter save")
	} else {
		parts = append(parts, "j/k move", "space toggle", "dd del", "u undo", "^r redo", "e vim", "i inline", "o/O new", "q quit")
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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

func copyTasks(tasks []task) []task {
	cp := make([]task, len(tasks))
	copy(cp, tasks)
	return cp
}

func (m model) saveUndoState() model {
	state := undoState{
		tasks:  copyTasks(m.tasks),
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
		tasks:  copyTasks(m.tasks),
		cursor: m.cursor,
	}
	m.redoStack = append(m.redoStack, redoState)

	// Pop from undo stack
	lastIdx := len(m.undoStack) - 1
	state := m.undoStack[lastIdx]
	m.undoStack = m.undoStack[:lastIdx]

	m.tasks = state.tasks
	m.cursor = clampCursor(state.cursor, len(m.tasks))
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
		tasks:  copyTasks(m.tasks),
		cursor: m.cursor,
	}
	m.undoStack = append(m.undoStack, undoState)

	// Pop from redo stack
	lastIdx := len(m.redoStack) - 1
	state := m.redoStack[lastIdx]
	m.redoStack = m.redoStack[:lastIdx]

	m.tasks = state.tasks
	m.cursor = clampCursor(state.cursor, len(m.tasks))
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
	wrap := totalWidth - 6
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
	width := m.windowWidth - 10
	if width < 20 {
		width = 20
	}
	m.textInput.Width = width
	return m
}

func (m model) selectionRange() (int, int, bool) {
	if !m.selectionActive || len(m.tasks) == 0 {
		return 0, 0, false
	}
	anchor := clampCursor(m.selectionAnchor, len(m.tasks))
	cursor := clampCursor(m.cursor, len(m.tasks))
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
	if len(m.tasks) == 0 {
		m.selectionActive = false
		return m
	}
	if m.selectionAnchor < 0 {
		m.selectionAnchor = 0
	}
	if m.selectionAnchor >= len(m.tasks) {
		m.selectionAnchor = len(m.tasks) - 1
	}
	return m
}

func main() {
	if err := initLogger(); err != nil {
		fmt.Println("warning: failed to init logger:", err)
	}
	flag.Parse()
	path := ""
	if flag.NArg() == 0 {
		// Create a new file called todo.md in the current directory or find an existing one
		path = "todo.md"
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_, err := os.Create(path)
			if err != nil {
				fmt.Println("error: failed to create todo.md:", err)
				os.Exit(1)
			}
		}
	} else {
		path = flag.Arg(0)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Printf("file %s does not exist\n", path)
		os.Exit(1)
	} else if err != nil {
		fmt.Println("error: ", err)
		os.Exit(1)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	m, err := newModel(abs)
	if err != nil {
		fmt.Println("failed to load tasks:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}
