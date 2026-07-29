// Package tui provides the first interactive terminal surface for code-cli.
package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"code-cli/internal/anthropicapi"
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
	defaultWidth          = 80
	maxInputContentLines  = 9999
	queryEventBuffer      = 32
	spinnerTickInterval   = 50 * time.Millisecond
	spinnerFrameInterval  = 120 * time.Millisecond
	turnDurationThreshold = 30 * time.Second

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
	session          *session.Session
	responder        Responder
	config           Config
	input            textarea.Model
	width            int
	darkBackground   bool
	colorProfile     colorprofile.Profile
	palette          palette
	busy             bool
	status           string
	statusFrame      int
	statusStarted    time.Time
	statusElapsed    time.Duration
	statusTicking    bool
	statusMode       statusMode
	thinkingStarted  time.Time
	thoughtDuration  time.Duration
	thoughtVisibleTo time.Time
	turnStarted      time.Time
	turnGeneration   uint64
	transient        string
	cancel           context.CancelFunc
	canceling        bool
	err              error
	headerPrinted    bool
	printedEntries   int
	staticQueued     bool
	queuedHeader     bool
	queuedEntryCount int
}

type staticOutputMsg struct {
	content       string
	includeHeader bool
	entryCount    int
	next          tea.Cmd
}

type staticOutputCommittedMsg struct {
	includeHeader bool
	entryCount    int
	next          tea.Cmd
}

type queryEventMsg struct {
	ctx        context.Context
	event      query.Event
	events     <-chan query.Event
	generation uint64
	ok         bool
	canceled   bool
}

type statusTickMsg struct {
	generation uint64
	at         time.Time
}

type statusMode uint8

const (
	statusRequesting statusMode = iota
	statusThinking
	statusResponding
	statusToolUse
)

var (
	spinnerFrames = []string{"·", "✢", "*", "✶", "✻", "✽", "✻", "✶", "*", "✢"}
	spinnerVerbs  = []string{
		"Architecting", "Brewing", "Calculating", "Clauding", "Cogitating",
		"Composing", "Considering", "Crafting", "Deciphering", "Deliberating",
		"Generating", "Ideating", "Inferring", "Musing", "Orchestrating",
		"Pondering", "Processing", "Ruminating", "Synthesizing", "Working",
	}
	turnCompletionVerbs = []string{
		"Baked", "Brewed", "Churned", "Cogitated", "Cooked", "Crunched", "Sautéed", "Worked",
	}
)

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
	input.Placeholder = ""
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

// Init requests the terminal background color for palette adaptation.
func (model Model) Init() tea.Cmd {
	return func() tea.Msg {
		return tea.RequestBackgroundColor()
	}
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
	case staticOutputMsg:
		return model, tea.Sequence(
			tea.Println(message.content),
			func() tea.Msg {
				return staticOutputCommittedMsg{
					includeHeader: message.includeHeader,
					entryCount:    message.entryCount,
					next:          message.next,
				}
			},
		)
	case staticOutputCommittedMsg:
		if message.includeHeader {
			model.headerPrinted = true
		}
		model.printedEntries = max(model.printedEntries, message.entryCount)
		model.staticQueued = false
		model.queuedHeader = false
		model.queuedEntryCount = 0
		if model.busy && !model.statusTicking {
			model.statusTicking = true
			return model, tea.Batch(
				message.next,
				statusTick(model.turnGeneration),
			)
		}
		return model, message.next
	case statusTickMsg:
		if message.generation != model.turnGeneration {
			return model, nil
		}
		if !model.busy {
			model.statusTicking = false
			return model, nil
		}
		model.statusElapsed = max(0, message.at.Sub(model.statusStarted))
		model.statusFrame = int(model.statusElapsed / spinnerFrameInterval)
		return model, statusTick(message.generation)
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

// View renders only content still managed by Bubble Tea. Stable header and
// transcript rows are printed above the program into native scrollback.
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
	started := time.Now()
	model.busy = true
	model.status = spinnerVerbs[rand.IntN(len(spinnerVerbs))] + "…"
	model.statusFrame = 0
	model.statusStarted = started
	model.statusElapsed = 0
	model.statusMode = statusRequesting
	model.thinkingStarted = time.Time{}
	model.thoughtDuration = 0
	model.thoughtVisibleTo = time.Time{}
	model.turnStarted = started
	model.turnGeneration++
	model.transient = ""
	model.cancel = cancel
	model.canceling = false
	model.err = nil
	model.input.Reset()
	model.input.Blur()
	model.configureInputGeometry()

	return model.queueStaticOutput(startQuery(
		model.responder,
		ctx,
		model.turnGeneration,
		core.UserMessage(entry.Text),
	))
}

func statusTick(generation uint64) tea.Cmd {
	return tea.Tick(spinnerTickInterval, func(now time.Time) tea.Msg {
		return statusTickMsg{generation: generation, at: now}
	})
}

func startQuery(responder Responder, ctx context.Context, generation uint64, message core.Message) tea.Cmd {
	return func() tea.Msg {
		events := responder.SubmitEvents(ctx, message, queryEventBuffer)
		return receiveQueryEvent(ctx, generation, events)
	}
}

// queueStaticOutput removes stable rows from the managed view and schedules
// them for insertion above the program. Printed markers advance only after the
// renderer receives the output, and the continuation runs after that commit.
func (model *Model) queueStaticOutput(next tea.Cmd) tea.Cmd {
	entries := model.session.Entries()
	start := min(model.printedEntries, len(entries))
	includeHeader := !model.headerPrinted
	if !includeHeader && start == len(entries) {
		return next
	}

	entryCount := len(entries)
	content := model.renderStaticOutput(includeHeader, entries, start, entryCount)
	model.staticQueued = true
	model.queuedHeader = includeHeader
	model.queuedEntryCount = entryCount
	return func() tea.Msg {
		return staticOutputMsg{
			content:       content,
			includeHeader: includeHeader,
			entryCount:    entryCount,
			next:          next,
		}
	}
}

func (model Model) renderStaticOutput(includeHeader bool, entries []session.Entry, start, end int) string {
	var lines []string
	if includeHeader {
		lines = append(lines, model.renderHeader()...)
	}
	lines = append(lines, model.renderTranscriptEntries(entries, start, end, true)...)
	return strings.Join(lines, "\n")
}

func nextQueryEvent(ctx context.Context, generation uint64, events <-chan query.Event) tea.Cmd {
	return func() tea.Msg {
		return receiveQueryEvent(ctx, generation, events)
	}
}

func receiveQueryEvent(ctx context.Context, generation uint64, events <-chan query.Event) queryEventMsg {
	if events == nil {
		return queryEventMsg{ctx: ctx, generation: generation}
	}
	select {
	case <-ctx.Done():
		return queryEventMsg{ctx: ctx, generation: generation, canceled: true}
	case event, ok := <-events:
		return queryEventMsg{ctx: ctx, event: event, events: events, generation: generation, ok: ok}
	}
}

func (model *Model) handleQueryEvent(message queryEventMsg) (tea.Model, tea.Cmd) {
	if message.generation != model.turnGeneration || !model.busy {
		return *model, nil
	}
	if message.canceled {
		model.finishQuery(nil)
		return *model, model.input.Focus()
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
		model.updateStatusFromStream(message.event.Stream)
		if stream := message.event.Stream; stream != nil &&
			stream.Type == "content_block_delta" && stream.Delta != nil && stream.Delta.Type == "text_delta" {
			model.transient += stream.Delta.Text
		}
	case query.EventAssistantMessage:
		model.transient = ""
		if text := visibleAssistantText(message.event.Message); text != "" {
			if err := model.session.AppendAssistant(text); err != nil {
				model.err = err
			} else {
				return *model, model.queueStaticOutput(nextQueryEvent(message.ctx, message.generation, message.events))
			}
		}
	case query.EventCompleted:
		completionErr := completionError(message.event)
		completedAt := time.Now()
		if model.shouldAppendTurnDuration(message.event, completedAt) {
			duration := completedAt.Sub(model.turnStarted)
			verb := turnCompletionVerbs[rand.IntN(len(turnCompletionVerbs))]
			completionErr = errors.Join(completionErr, model.session.AppendTurnDuration(
				fmt.Sprintf("%s for %s", verb, formatTurnDuration(duration)),
			))
		}
		model.finishQuery(completionErr)
		return *model, model.queueStaticOutput(model.input.Focus())
	}
	return *model, nextQueryEvent(message.ctx, message.generation, message.events)
}

func (model *Model) updateStatusFromStream(stream *anthropicapi.StreamEvent) {
	if stream == nil {
		return
	}
	previous := model.statusMode
	switch stream.Type {
	case anthropicapi.StreamEventContentBlockStart:
		if stream.Block == nil {
			return
		}
		switch stream.Block.Type {
		case core.ContentBlockThinking:
			model.statusMode = statusThinking
		case core.ContentBlockText:
			model.statusMode = statusResponding
		case core.ContentBlockToolUse, core.ContentBlockServerToolUse:
			model.statusMode = statusToolUse
		}
	case anthropicapi.StreamEventContentBlockDelta:
		if stream.Delta == nil {
			return
		}
		switch stream.Delta.Type {
		case "thinking_delta":
			model.statusMode = statusThinking
		case "text_delta":
			model.statusMode = statusResponding
		case "input_json_delta":
			model.statusMode = statusToolUse
		}
	}
	if model.statusMode != statusThinking && !model.thinkingStarted.IsZero() &&
		(previous == statusThinking || model.statusMode == statusResponding || model.statusMode == statusToolUse) {
		model.thoughtDuration = time.Since(model.thinkingStarted)
		model.thoughtVisibleTo = time.Now().Add(2 * time.Second)
		model.thinkingStarted = time.Time{}
	}
	if previous != statusThinking && model.statusMode == statusThinking && model.thinkingStarted.IsZero() {
		model.thinkingStarted = time.Now()
	}
}

func (model *Model) finishQuery(err error) {
	if model.cancel != nil {
		model.cancel()
	}
	model.busy = false
	model.status = ""
	model.statusFrame = 0
	model.statusStarted = time.Time{}
	model.statusElapsed = 0
	model.statusTicking = false
	model.thinkingStarted = time.Time{}
	model.thoughtDuration = 0
	model.thoughtVisibleTo = time.Time{}
	model.turnStarted = time.Time{}
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

func (model Model) shouldAppendTurnDuration(event query.Event, completedAt time.Time) bool {
	if model.canceling || model.turnStarted.IsZero() || completedAt.Sub(model.turnStarted) <= turnDurationThreshold {
		return false
	}
	return event.Result == nil || event.Result.Outcome != query.OutcomeCanceled
}

func formatTurnDuration(duration time.Duration) string {
	if duration < time.Minute {
		return fmt.Sprintf("%ds", max(0, int(duration/time.Second)))
	}

	totalSeconds := int64((duration + 500*time.Millisecond) / time.Second)
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}

	hours := minutes / 60
	minutes %= 60
	if hours < 24 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}

	days := hours / 24
	hours %= 24
	return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
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
		// Soft dark-theme text (palette.text), not pure white.
		Cursor: textarea.CursorStyle{
			Color: model.palette.text,
			Shape: tea.CursorBlock,
			Blink: false,
		},
	})
}

func (model Model) renderLines() []string {
	var lines []string
	if !model.headerPrinted && !(model.staticQueued && model.queuedHeader) {
		lines = model.renderHeader()
	}
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

	// Source places SpinnerWithVerb above PromptInput, not inside the border.
	content := lipgloss.NewStyle().
		Width(model.width).
		BorderForeground(model.palette.promptBorder).
		Border(lipgloss.RoundedBorder(), true, false, true, false).
		Render(strings.Join(inputRows, "\n"))
	lines := []string{strings.Repeat(" ", model.width)}
	if status := model.busyStatusLine(); status != "" {
		lines = append(lines, status, strings.Repeat(" ", model.width))
	}
	lines = append(lines, strings.Split(content, "\n")...)
	// Non-auth failures stay outside the composer; auth is transcript-only.
	if model.err != nil {
		errLine := lipgloss.NewStyle().Foreground(model.palette.error).Render("  Error: " + model.err.Error())
		lines = append(lines, padDisplay(errLine, model.width))
	}
	return lines
}

// busyStatusLine mirrors the source spinner row above the prompt.
// Hidden while streamed assistant text is visible, except during cancel.
func (model Model) busyStatusLine() string {
	if model.status == "" {
		return ""
	}
	if model.transient != "" && !model.canceling {
		return ""
	}

	frame := spinnerFrames[model.statusFrame%len(spinnerFrames)]
	frameText := lipgloss.NewStyle().Foreground(model.palette.claude).Render(frame + " ")
	message := model.renderSpinnerMessage(model.status)

	suffix := ""
	suffixColor := model.palette.inactive
	now := time.Now()
	switch {
	case model.canceling:
		message = lipgloss.NewStyle().Foreground(model.palette.claude).Render(model.status)
	case model.thoughtDuration > 0 && now.Before(model.thoughtVisibleTo):
		seconds := max(1, int(model.thoughtDuration.Round(time.Second)/time.Second))
		suffix = fmt.Sprintf(" (thought for %ds)", seconds)
	case model.statusMode == statusThinking && !model.thinkingStarted.IsZero():
		suffix = " (thinking)"
		if model.statusElapsed >= 3*time.Second {
			phase := float64(model.statusElapsed-3*time.Second) / float64(2*time.Second) * 2 * math.Pi
			intensity := (math.Sin(phase) + 1) / 2
			suffixColor = blendTerminalColor(model.colorProfile, model.palette.inactive, lipgloss.Color("#b9b9b9"), intensity)
		}
	}

	status := frameText + message
	if suffix != "" {
		status += lipgloss.NewStyle().Foreground(suffixColor).Render(suffix)
	}
	return padDisplay(status, model.width)
}

func (model Model) renderSpinnerMessage(message string) string {
	if message == "" {
		return ""
	}

	runes := []rune(message)
	speed := 200 * time.Millisecond
	if model.statusMode == statusRequesting {
		speed = 50 * time.Millisecond
	}
	cycleLength := float64(len(runes) + 20)
	cyclePosition := math.Mod(float64(model.statusElapsed)/float64(speed), cycleLength)
	glimmerPosition := float64(len(runes)) + 10 - cyclePosition
	if model.statusMode == statusRequesting {
		glimmerPosition = cyclePosition - 10
	}

	var rendered strings.Builder
	for index, character := range runes {
		distance := math.Abs(float64(index) - glimmerPosition)
		intensity := max(0, 1-distance/2)
		foreground := blendTerminalColor(model.colorProfile, model.palette.claude, model.palette.text, intensity)
		rendered.WriteString(lipgloss.NewStyle().Foreground(foreground).Render(string(character)))
	}
	return rendered.String()
}

func blendTerminalColor(profile colorprofile.Profile, from, to color.Color, amount float64) color.Color {
	amount = min(1, max(0, amount))
	fromR, fromG, fromB, _ := from.RGBA()
	toR, toG, toB, _ := to.RGBA()
	blend := func(start, end uint32) uint8 {
		return uint8(math.Round((float64(start) + (float64(end)-float64(start))*amount) / 257))
	}
	return profile.Convert(color.RGBA{
		R: blend(fromR, toR),
		G: blend(fromG, toG),
		B: blend(fromB, toB),
		A: 0xff,
	})
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
	start := model.printedEntries
	if model.staticQueued {
		start = max(start, model.queuedEntryCount)
	}
	start = min(start, len(entries))
	if start == len(entries) && model.transient == "" {
		return nil
	}

	lines := model.renderTranscriptEntries(entries, start, len(entries), true)
	if model.transient == "" {
		return lines
	}

	var builder strings.Builder
	if len(lines) > 0 {
		builder.WriteString(strings.Join(lines, "\n"))
		builder.WriteString("\n\n")
	} else {
		builder.WriteByte('\n')
	}
	model.renderAssistantEntry(&builder, model.transient)
	return strings.Split(builder.String(), "\n")
}

func (model Model) renderTranscriptEntries(entries []session.Entry, start, end int, leadingMargin bool) []string {
	start = min(max(0, start), len(entries))
	end = min(max(start, end), len(entries))
	if start == end {
		return nil
	}

	var builder strings.Builder
	if leadingMargin {
		builder.WriteByte('\n')
	}
	for index := start; index < end; index++ {
		if index > start {
			builder.WriteString("\n\n")
		}
		entry := entries[index]
		switch {
		case entry.Style == session.EntryStyleError:
			model.renderErrorEntry(&builder, entry.Text)
		case entry.Style == session.EntryStyleTurnDuration:
			model.renderTurnDurationEntry(&builder, entry.Text)
		case entry.Role == core.RoleUser:
			model.renderUserEntry(&builder, entry.Text)
		case entry.Role == core.RoleAssistant:
			model.renderAssistantEntry(&builder, entry.Text)
		default:
			model.renderOtherEntry(&builder, string(entry.Role)+": "+entry.Text)
		}
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

// renderTurnDurationEntry matches the source's persisted dim completion row.
func (model Model) renderTurnDurationEntry(builder *strings.Builder, text string) {
	style := lipgloss.NewStyle().Foreground(model.palette.inactive)
	model.renderPrefixedEntry(builder, text, style.Render("✻ "), style, false)
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
