// Package tui provides the first interactive terminal surface for code-cli.
package tui

import (
	"context"
	"errors"
	"fmt"
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
	"code-cli/internal/query"
	"code-cli/internal/session"
)

var (
	ErrNilSession     = errors.New("session is nil")
	ErrNilResponder   = errors.New("tui responder is unavailable")
	ErrResponderEnded = errors.New("tui responder ended without completion")
)

const (
	defaultWidth         = 80
	maxInputContentLines = 9999
	queryEventBuffer     = 32

	// Source Clawd default pose segments (9 cols wide).
	clawdR1Left  = " ▐"
	clawdR1Eyes  = "▛███▜"
	clawdR1Right = "▌"
	clawdR2Left  = "▝▜"
	clawdR2Mid   = "█████"
	clawdR2Right = "▛▘"
	clawdR3      = "  ▘▘ ▝▝  "
	clawdWidth   = 9
	// CondensedLogo reserves clawd (11) + gap (2) + padding (2) ≈ 15 columns.
	headerInfoReserve = 15
	headerGap         = 2

	// Source transcript auth failure (AssistantTextMessage / INVALID_API_KEY_ERROR_MESSAGE).
	notLoggedInMessage = "Not logged in · Please run /login"
	// Source footer notification wording (Notifications.tsx) differs slightly.
	notLoggedInFooter = "Not logged in · Run /login"
)

// Responder is the UI-independent asynchronous query boundary used by Model.
type Responder interface {
	SubmitEvents(context.Context, core.Message, int) <-chan query.Event
}

// Config supplies the responder and visible host metadata used by the condensed source-style header.
type Config struct {
	Responder        Responder
	Version          string
	Model            string
	WorkingDirectory string
	Agent            string
}

// Model is the Bubble Tea model for the streamed query TUI.
type Model struct {
	session        *session.Session
	responder      Responder
	config         Config
	input          textarea.Model
	width          int
	darkBackground bool
	colorProfile   colorprofile.Profile
	palette        palette
	busy           bool
	status         string
	transient      string
	cancel         context.CancelFunc
	canceling      bool
	err            error
}

type queryEventMsg struct {
	event  query.Event
	events <-chan query.Event
	ok     bool
}

// New constructs a source-style TUI with local default metadata.
// A host must use NewWithConfig to supply a responder.
func New(sessionState *session.Session) (Model, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		workingDirectory = ""
	}
	return NewWithConfig(sessionState, Config{
		Version:          "dev",
		WorkingDirectory: workingDirectory,
	})
}

// NewWithConfig constructs a TUI with an injected responder and explicit display metadata.
func NewWithConfig(sessionState *session.Session, config Config) (Model, error) {
	if sessionState == nil {
		return Model{}, ErrNilSession
	}
	if config.Responder == nil {
		return Model{}, ErrNilResponder
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
		responder:      config.Responder,
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

// Run starts the inline TUI. It returns ErrNilResponder until a host supplies one.
func Run(sessionState *session.Session) error {
	return RunWithConfig(sessionState, Config{})
}

// RunWithConfig starts the inline TUI with an injected responder and host metadata.
func RunWithConfig(sessionState *session.Session, config Config) error {
	model, err := NewWithConfig(sessionState, config)
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

// Update serializes terminal input and asynchronous responder events.
func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyPressMsg:
		if message.Code == 'c' && message.Mod == tea.ModCtrl {
			if model.busy {
				if model.canceling {
					return model, tea.Quit
				}
				model.canceling = true
				if model.cancel != nil {
					model.cancel()
				}
				model.status = "Canceling…"
				return model, nil
			}
			return model, tea.Quit
		}
		if model.busy {
			return model, nil
		}
		if message.Code == tea.KeyEnter && message.Mod == 0 {
			return model, model.submit()
		}
	case queryEventMsg:
		return model.handleQueryEvent(message)
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

func (model *Model) submit() tea.Cmd {
	entry, err := model.session.AppendUser(model.input.Value())
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	model.busy = true
	model.status = "Thinking…"
	model.transient = ""
	model.cancel = cancel
	model.canceling = false
	model.err = nil
	model.input.Reset()
	model.input.Blur()
	model.configureInputGeometry()

	return startQuery(model.responder, ctx, core.UserMessage(entry.Text))
}

func startQuery(responder Responder, ctx context.Context, message core.Message) tea.Cmd {
	return func() tea.Msg {
		events := responder.SubmitEvents(ctx, message, queryEventBuffer)
		return receiveQueryEvent(events)
	}
}

func nextQueryEvent(events <-chan query.Event) tea.Cmd {
	return func() tea.Msg {
		return receiveQueryEvent(events)
	}
}

func receiveQueryEvent(events <-chan query.Event) queryEventMsg {
	if events == nil {
		return queryEventMsg{}
	}
	event, ok := <-events
	return queryEventMsg{event: event, events: events, ok: ok}
}

func (model *Model) handleQueryEvent(message queryEventMsg) (tea.Model, tea.Cmd) {
	if !model.busy {
		return *model, nil
	}
	if !message.ok {
		if model.canceling {
			model.finishQuery(nil)
		} else {
			model.finishQuery(ErrResponderEnded)
		}
		return *model, model.input.Focus()
	}

	switch message.event.Type {
	case query.EventStream:
		if stream := message.event.Stream; stream != nil &&
			stream.Type == "content_block_delta" && stream.Delta != nil && stream.Delta.Type == "text_delta" {
			model.transient += stream.Delta.Text
		}
	case query.EventAssistantMessage:
		model.transient = ""
		if text := visibleAssistantText(message.event.Message); text != "" {
			if err := model.session.AppendAssistant(text); err != nil {
				model.err = err
			}
		}
	case query.EventCompleted:
		model.finishQuery(completionError(message.event))
		return *model, model.input.Focus()
	}
	return *model, nextQueryEvent(message.events)
}

func (model *Model) finishQuery(err error) {
	if model.cancel != nil {
		model.cancel()
	}
	model.busy = false
	model.status = ""
	model.transient = ""
	model.cancel = nil
	model.canceling = false
	model.err = nil
	if err != nil {
		// Source maps auth failures into assistant transcript errors rather than
		// leaving raw SDK text in the composer status area.
		if text := userVisibleQueryError(err); text != "" {
			if appendErr := model.session.AppendError(text); appendErr != nil {
				model.err = appendErr
			}
		} else {
			model.err = err
		}
	}
	model.configureInputGeometry()
}

func completionError(event query.Event) error {
	if event.Result != nil && event.Result.Outcome == query.OutcomeCanceled {
		return nil
	}
	if event.Err != nil {
		return event.Err
	}
	if event.Result == nil {
		return nil
	}
	switch event.Result.Outcome {
	case query.OutcomeEndTurn, query.OutcomeStopSequence, query.OutcomeCanceled:
		return nil
	case query.OutcomeMaxTokens,
		query.OutcomeRefusal,
		query.OutcomePauseTurn,
		query.OutcomeToolTurnLimit,
		query.OutcomeFailed:
		return fmt.Errorf("query ended with %s", event.Result.Outcome)
	default:
		return nil
	}
}

// userVisibleQueryError maps normalized query failures to source-aligned transcript text.
// Returns empty when the error should stay as a generic composer status.
func userVisibleQueryError(err error) string {
	if err == nil {
		return ""
	}
	if apiErr, ok := errors.AsType[*core.APIError](err); ok && apiErr != nil {
		switch apiErr.Kind {
		case core.APIErrorAuth, core.APIErrorPermission:
			return notLoggedInMessage
		}
	}
	// Defense in depth for classification gaps (wrapped SDK strings before ClassifyError).
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "no anthropic credentials found") ||
		strings.Contains(lower, "authentication_error") ||
		strings.Contains(lower, "invalid x-api-key") ||
		strings.Contains(lower, "x-api-key") {
		return notLoggedInMessage
	}
	return ""
}

func visibleAssistantText(message *core.Message) string {
	if message == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range message.Content {
		if block.Type == core.ContentBlockText {
			text.WriteString(block.Text)
		}
	}
	return text.String()
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

	// CondensedLogo: textWidth = max(columns - 15, 20)
	textWidth := max(20, model.width-headerInfoReserve)
	// "Claude Code v" is 13 runes; version is truncated to fit.
	version := truncateDisplay(model.config.Version, max(6, textWidth-13))
	modelLabel := truncateDisplay(model.config.Model, textWidth)
	cwd := model.config.WorkingDirectory
	if model.config.Agent != "" {
		// "@agent · " overhead is 1 + agent + 3 separators.
		agentWidth := runewidth.StringWidth(model.config.Agent)
		cwdWidth := max(10, textWidth-1-agentWidth-3)
		cwd = "@" + model.config.Agent + " · " + truncateDisplay(cwd, cwdWidth)
	} else {
		cwd = truncateDisplay(cwd, textWidth)
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(model.palette.text)
	// Source uses dimColor for version, model, and cwd (not mixed inactive/subtle).
	dimStyle := lipgloss.NewStyle().Foreground(model.palette.inactive)
	info := []string{
		titleStyle.Render("Claude Code") + " " + dimStyle.Render("v"+version),
		dimStyle.Render(modelLabel),
		dimStyle.Render(cwd),
	}

	markLines := model.renderClawd()
	// Source Box gap={2}; pad after the 9-col mark so total clawd+gap ≈ 11.
	gap := strings.Repeat(" ", headerGap)
	lines := make([]string, len(markLines))
	for index, mark := range markLines {
		value := mark + gap
		if index < len(info) {
			value += info[index]
		}
		lines[index] = value
	}
	return lines
}

// renderClawd matches source Clawd default pose with body + eye background colors.
func (model Model) renderClawd() []string {
	body := lipgloss.NewStyle().Foreground(model.palette.claude)
	eyes := lipgloss.NewStyle().
		Foreground(model.palette.claude).
		Background(model.palette.clawdBackground)
	return []string{
		body.Render(clawdR1Left) + eyes.Render(clawdR1Eyes) + body.Render(clawdR1Right),
		body.Render(clawdR2Left) + eyes.Render(clawdR2Mid) + body.Render(clawdR2Right),
		body.Render(clawdR3),
	}
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
	if model.status != "" {
		status := lipgloss.NewStyle().Foreground(model.palette.inactive).Render("  " + model.status)
		inputRows = append(inputRows, padDisplay(status, model.width))
	}
	// Non-auth failures still surface under the composer; auth is transcript-only.
	if model.err != nil {
		status := lipgloss.NewStyle().Foreground(model.palette.error).Render("  Error: " + model.err.Error())
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
	// Source PromptInputFooter: suppressHint when input.length > 0.
	left := ""
	if !model.busy && strings.TrimSpace(model.input.Value()) == "" {
		left = lipgloss.NewStyle().Foreground(model.palette.inactive).Render("? for shortcuts")
	}

	// Source Notifications: right-side auth notice when credentials are missing.
	// Detect via transcript error entries (no live credential probe in this rewrite).
	right := ""
	if model.hasAuthTranscriptError() {
		right = lipgloss.NewStyle().Foreground(model.palette.error).Render(notLoggedInFooter)
	}

	if left == "" && right == "" {
		return nil
	}
	if right == "" {
		return []string{padDisplay("  "+left, model.width)}
	}
	if left == "" {
		return []string{padDisplay(alignRight(right, model.width), model.width)}
	}
	// Space-separated left/right byline when both fit.
	gap := model.width - 2 - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return []string{padDisplay("  "+left, model.width)}
	}
	return []string{"  " + left + strings.Repeat(" ", gap) + right}
}

func (model Model) hasAuthTranscriptError() bool {
	for _, entry := range model.session.Entries() {
		if entry.Style == session.EntryStyleError && entry.Text == notLoggedInMessage {
			return true
		}
	}
	return false
}

func alignRight(value string, width int) string {
	w := lipgloss.Width(value)
	if width <= 0 {
		return ""
	}
	if w >= width {
		return ansi.Truncate(value, width, "")
	}
	return strings.Repeat(" ", width-w) + value
}

func (model Model) renderTranscript() []string {
	entries := model.session.Entries()
	if len(entries) == 0 && model.transient == "" {
		return nil
	}

	var builder strings.Builder
	for index, entry := range entries {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		switch {
		case entry.Style == session.EntryStyleError:
			model.renderErrorEntry(&builder, entry.Text)
		case entry.Role == core.RoleUser:
			model.renderUserEntry(&builder, entry.Text)
		case entry.Role == core.RoleAssistant:
			model.renderAssistantEntry(&builder, entry.Text)
		default:
			model.renderOtherEntry(&builder, string(entry.Role)+": "+entry.Text)
		}
	}
	if model.transient != "" {
		if len(entries) > 0 {
			builder.WriteString("\n\n")
		}
		model.renderAssistantEntry(&builder, model.transient)
	}
	return strings.Split(builder.String(), "\n")
}

func (model Model) renderUserEntry(builder *strings.Builder, text string) {
	// Source UserPromptMessage + HighlightedThinkingText: full-width
	// userMessageBackground with a subtle ❯ pointer (not the composer marker color).
	style := lipgloss.NewStyle().
		Foreground(model.palette.text).
		Background(model.palette.userMessageBackground)
	// Source HighlightedThinkingText uses figures.pointer + regular space.
	pointer := lipgloss.NewStyle().
		Foreground(model.palette.subtle).
		Background(model.palette.userMessageBackground).
		Render("❯ ")
	model.renderPrefixedEntry(builder, text, pointer, style, true)
}

func (model Model) renderAssistantEntry(builder *strings.Builder, text string) {
	// Source AssistantTextMessage: BLACK_CIRCLE with minWidth 2 gutter.
	style := lipgloss.NewStyle().Foreground(model.palette.text)
	pointer := lipgloss.NewStyle().Foreground(model.palette.text).Render("● ")
	model.renderPrefixedEntry(builder, text, pointer, style, false)
}

// renderErrorEntry matches source MessageResponse: dim "  ⎿ " plus error-colored text.
func (model Model) renderErrorEntry(builder *strings.Builder, text string) {
	prefix := lipgloss.NewStyle().Foreground(model.palette.inactive).Render("  ⎿ ")
	style := lipgloss.NewStyle().Foreground(model.palette.error)
	gutterWidth := lipgloss.Width(prefix)
	if model.width < gutterWidth+1 {
		gutterWidth = 0
		prefix = ""
	}
	contentWidth := max(1, model.width-gutterWidth)
	lines := wrapDisplay(text, contentWidth)
	for index, line := range lines {
		if index > 0 {
			builder.WriteByte('\n')
		}
		if index == 0 {
			builder.WriteString(prefix)
			builder.WriteString(style.Render(line))
			continue
		}
		builder.WriteString(style.Render(strings.Repeat(" ", gutterWidth) + line))
	}
}

func (model Model) renderOtherEntry(builder *strings.Builder, text string) {
	style := lipgloss.NewStyle().Foreground(model.palette.inactive)
	model.renderPrefixedEntry(builder, text, "", style, false)
}

// renderPrefixedEntry wraps text with a first-line marker and a fixed 2-col gutter.
// fullWidth pads each rendered line to the terminal width (user message background).
func (model Model) renderPrefixedEntry(builder *strings.Builder, text, firstPrefix string, style lipgloss.Style, fullWidth bool) {
	gutterWidth := 2
	if model.width < 3 {
		gutterWidth = 0
		firstPrefix = ""
	}
	contentWidth := max(1, model.width-gutterWidth)
	lines := wrapDisplay(text, contentWidth)
	for index, line := range lines {
		if index > 0 {
			builder.WriteByte('\n')
		}
		var rendered string
		if index == 0 && firstPrefix != "" {
			rendered = firstPrefix + style.Render(line)
		} else if gutterWidth > 0 {
			rendered = style.Render(strings.Repeat(" ", gutterWidth) + line)
		} else {
			rendered = style.Render(line)
		}
		if fullWidth {
			// Paint the remaining columns with the user message background.
			pad := max(0, model.width-lipgloss.Width(rendered))
			if pad > 0 {
				rendered += style.Render(strings.Repeat(" ", pad))
			}
		}
		builder.WriteString(rendered)
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
		config.Model = "Claude"
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

var (
	_ tea.Model = Model{}
	_ Responder = (*query.Engine)(nil)
)
