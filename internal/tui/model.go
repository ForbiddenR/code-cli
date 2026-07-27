// Package tui provides the first interactive terminal surface for code-cli.
package tui

import (
	"errors"
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"code-cli/internal/core"
	"code-cli/internal/session"
)

var ErrNilSession = errors.New("session is nil")

const (
	defaultWidth         = 80
	maxInputContentLines = 9999
	clawdMark            = " ▐▛███▜▌\n▝▜█████▛▘\n  ▘▘ ▝▝"
)

// Config supplies the visible host metadata used by the condensed source-style header.
type Config struct {
	Version          string
	Model            string
	WorkingDirectory string
	Agent            string
}

// Model is the Bubble Tea model for the local echo TUI.
type Model struct {
	session        *session.Session
	config         Config
	input          textarea.Model
	width          int
	darkBackground bool
	colorProfile   colorprofile.Profile
	palette        palette
	err            error
}

// New constructs a source-style TUI with local default metadata.
func New(sessionState *session.Session) (Model, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		workingDirectory = ""
	}
	return NewWithConfig(sessionState, Config{
		Version:          "dev",
		Model:            "Local echo",
		WorkingDirectory: workingDirectory,
	})
}

// NewWithConfig constructs a TUI with explicit display metadata.
func NewWithConfig(sessionState *session.Session, config Config) (Model, error) {
	if sessionState == nil {
		return Model{}, ErrNilSession
	}
	config = withConfigDefaults(config)

	input := textarea.New()
	input.Prompt = ""
	input.Placeholder = "Type a message…"
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxContentHeight = maxInputContentLines
	input.SetVirtualCursor(true)
	input.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter"),
		key.WithHelp("shift+enter", "new line"),
	)

	model := Model{
		session:        sessionState,
		config:         config,
		input:          input,
		width:          defaultWidth,
		darkBackground: true,
		colorProfile:   colorprofile.ANSI,
	}
	model.palette = newPalette(model.darkBackground, model.colorProfile)
	model.applyInputStyles()
	model.configureInputGeometry()
	model.input.Focus()
	return model, nil
}

// Run starts the inline local echo TUI.
func Run(sessionState *session.Session) error {
	model, err := New(sessionState)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(model).Run()
	return err
}

// Session returns the model's backing session for inspection by a host or test.
func (model Model) Session() *session.Session {
	return model.session
}

// Init initializes cursor blinking and requests the terminal background color.
func (model Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, func() tea.Msg {
		return tea.RequestBackgroundColor()
	})
}

// Update handles terminal events and synchronous local echo responses.
func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyPressMsg:
		if message.Code == 'c' && message.Mod == tea.ModCtrl {
			return model, tea.Quit
		}
		if message.Code == tea.KeyEnter && message.Mod == 0 {
			model.submit()
			return model, nil
		}
	case tea.WindowSizeMsg:
		model.resize(message.Width)
		return model, nil
	case tea.BackgroundColorMsg:
		model.darkBackground = message.IsDark()
		model.updatePalette()
		return model, nil
	case tea.ColorProfileMsg:
		model.colorProfile = message.Profile
		model.updatePalette()
		return model, nil
	}

	var command tea.Cmd
	model.input, command = model.input.Update(message)
	return model, command
}

// View renders the header, transcript, composer, and footer as one inline document.
func (model Model) View() tea.View {
	view := tea.NewView(strings.Join(model.renderLines(), "\n"))
	if summary := model.session.Summary(); summary != "" {
		view.WindowTitle = summary
	} else {
		view.WindowTitle = "Claude Code"
	}
	return view
}

func (model *Model) submit() {
	entry, err := model.session.AppendUser(model.input.Value())
	if err != nil {
		return
	}
	if err := model.session.AppendAssistant(entry.Text); err != nil {
		model.err = err
		return
	}
	model.err = nil
	model.input.Reset()
	model.input.Focus()
	model.configureInputGeometry()
}

func (model *Model) resize(width int) {
	model.width = max(1, width)
	model.configureInputGeometry()
}

func (model *Model) configureInputGeometry() {
	model.input.MaxHeight = 0
	model.input.SetWidth(max(1, model.width-3))
}

func (model *Model) updatePalette() {
	model.palette = newPalette(model.darkBackground, model.colorProfile)
	model.applyInputStyles()
}

func (model *Model) applyInputStyles() {
	base := lipgloss.NewStyle().Foreground(model.palette.text)
	state := textarea.StyleState{
		Base:             base,
		Text:             base,
		LineNumber:       lipgloss.NewStyle(),
		CursorLineNumber: lipgloss.NewStyle(),
		CursorLine:       base,
		EndOfBuffer:      base,
		Placeholder:      base.Faint(true),
		Prompt:           lipgloss.NewStyle(),
	}
	model.input.SetStyles(textarea.Styles{
		Focused: state,
		Blurred: state,
		Cursor: textarea.CursorStyle{
			Color: model.palette.text,
			Shape: tea.CursorBlock,
			Blink: true,
		},
	})
}

func (model Model) renderLines() []string {
	lines := model.renderHeader()
	lines = append(lines, model.renderTranscript()...)
	lines = append(lines, model.renderPrompt()...)
	lines = append(lines, model.renderFooter()...)
	return lines
}

func (model Model) renderHeader() []string {
	if model.width < 36 {
		title := truncateDisplay("Claude Code", model.width)
		return []string{lipgloss.NewStyle().Bold(true).Foreground(model.palette.text).Render(title)}
	}

	infoWidth := max(16, model.width-15)
	modelLabel := truncateDisplay(model.config.Model, infoWidth)
	cwd := truncateDisplay(model.config.WorkingDirectory, infoWidth)
	if model.config.Agent != "" {
		cwd = "@" + truncateDisplay(model.config.Agent, max(1, infoWidth-2)) + " · " + cwd
	}

	info := []string{
		lipgloss.NewStyle().Bold(true).Foreground(model.palette.text).Render("Claude Code v" + model.config.Version),
		lipgloss.NewStyle().Foreground(model.palette.inactive).Render(modelLabel),
		lipgloss.NewStyle().Foreground(model.palette.subtle).Render(cwd),
	}
	markStyle := lipgloss.NewStyle().Foreground(model.palette.claude)
	markLines := strings.Split(clawdMark, "\n")
	lines := make([]string, len(markLines))
	for index, mark := range markLines {
		value := markStyle.Render(mark)
		if index < len(info) {
			value += strings.Repeat(" ", max(2, 11-runewidth.StringWidth(mark))) + info[index]
		}
		lines[index] = value
	}
	return lines
}

func (model Model) renderPrompt() []string {
	inputRows := strings.Split(model.input.View(), "\n")
	if len(inputRows) > model.input.Height() {
		inputRows = inputRows[:model.input.Height()]
	}
	marker := lipgloss.NewStyle().Foreground(model.palette.text).Render("❯ ")
	for index, row := range inputRows {
		if index == 0 {
			row = marker + row
		} else {
			row = "  " + row
		}
		inputRows[index] = padDisplay(row, model.width)
	}
	if model.err != nil {
		status := lipgloss.NewStyle().Foreground(model.palette.inactive).Render("  Error: " + model.err.Error())
		inputRows = append(inputRows, padDisplay(status, model.width))
	}

	content := lipgloss.NewStyle().
		Width(model.width).
		BorderForeground(model.palette.promptBorder).
		Border(lipgloss.RoundedBorder(), true, false, true, false).
		Render(strings.Join(inputRows, "\n"))
	lines := strings.Split(content, "\n")
	return append([]string{strings.Repeat(" ", model.width)}, lines...)
}

func (model Model) renderFooter() []string {
	hint := lipgloss.NewStyle().Foreground(model.palette.inactive).Render("? for shortcuts")
	return []string{padDisplay("  "+hint, model.width)}
}

func (model Model) renderTranscript() []string {
	entries := model.session.Entries()
	if len(entries) == 0 {
		return nil
	}

	var builder strings.Builder
	for index, entry := range entries {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		switch entry.Role {
		case core.RoleUser:
			model.renderUserEntry(&builder, entry.Text)
		case core.RoleAssistant:
			model.renderAssistantEntry(&builder, entry.Text)
		default:
			model.renderOtherEntry(&builder, string(entry.Role)+": "+entry.Text)
		}
	}
	return strings.Split(builder.String(), "\n")
}

func (model Model) renderUserEntry(builder *strings.Builder, text string) {
	style := lipgloss.NewStyle().
		Foreground(model.palette.text).
		Background(model.palette.userMessageBackground).
		Width(model.width)
	model.renderMessageLines(builder, text, "❯ ", style)
}

func (model Model) renderAssistantEntry(builder *strings.Builder, text string) {
	style := lipgloss.NewStyle().Foreground(model.palette.text)
	model.renderMessageLines(builder, text, "● ", style)
}

func (model Model) renderOtherEntry(builder *strings.Builder, text string) {
	style := lipgloss.NewStyle().Foreground(model.palette.inactive)
	model.renderMessageLines(builder, text, "", style)
}

func (model Model) renderMessageLines(builder *strings.Builder, text, firstPrefix string, style lipgloss.Style) {
	gutterWidth := 2
	if model.width < 3 {
		gutterWidth = 0
	}
	contentWidth := max(1, model.width-gutterWidth)
	lines := wrapDisplay(text, contentWidth)
	for index, line := range lines {
		if index > 0 {
			builder.WriteByte('\n')
		}
		prefix := strings.Repeat(" ", gutterWidth)
		if index == 0 && gutterWidth > 0 {
			prefix = firstPrefix
		}
		builder.WriteString(style.Render(prefix + line))
	}
}

func wrapDisplay(text string, width int) []string {
	var lines []string
	for line := range strings.SplitSeq(text, "\n") {
		lines = append(lines, strings.Split(ansi.Hardwrap(line, max(1, width), true), "\n")...)
	}
	return lines
}

func withConfigDefaults(config Config) Config {
	if config.Version == "" {
		config.Version = "dev"
	}
	if config.Model == "" {
		config.Model = "Local echo"
	}
	return config
}

func truncateDisplay(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return runewidth.Truncate(value, width, "…")
}

func padDisplay(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) > width {
		value = ansi.Truncate(value, width, "")
	}
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

var _ tea.Model = Model{}
