package tui

import (
	"context"
	"errors"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
	"code-cli/internal/query"
	"code-cli/internal/session"
)

type responderCall struct {
	ctx     context.Context
	message core.Message
	buffer  int
}

type fakeResponder struct {
	channels []<-chan query.Event
	calls    []responderCall
	started  chan struct{}
}

func (responder *fakeResponder) SubmitEvents(ctx context.Context, message core.Message, buffer int) <-chan query.Event {
	responder.calls = append(responder.calls, responderCall{ctx: ctx, message: message, buffer: buffer})
	if responder.started != nil {
		select {
		case responder.started <- struct{}{}:
		default:
		}
	}
	index := len(responder.calls) - 1
	if index >= len(responder.channels) {
		return nil
	}
	return responder.channels[index]
}

func TestRunWithConfigValidatesBeforeStartingProgram(t *testing.T) {
	if err := RunWithConfig(session.New(), Config{}); !errors.Is(err, ErrNilResponder) {
		t.Fatalf("RunWithConfig() error = %v, want %v", err, ErrNilResponder)
	}
}

func TestModelStreamsAndCommitsOnlyVisibleAssistantText(t *testing.T) {
	events := make(chan query.Event, 8)
	responder := &fakeResponder{channels: []<-chan query.Event{events}}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("  hello world  ")

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if command == nil {
		t.Fatal("Enter should start an asynchronous command")
	}
	if len(responder.calls) != 0 {
		t.Fatal("responder ran synchronously in Update")
	}
	if !model.busy || model.status == "" || model.input.Focused() {
		t.Fatalf("submitted model state = busy %v, status %q, focused %v", model.busy, model.status, model.input.Focused())
	}
	if got := model.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty", got)
	}
	entries := model.Session().Entries()
	if len(entries) != 1 || entries[0] != (session.Entry{Role: core.RoleUser, Text: "hello world"}) {
		t.Fatalf("immediate entries = %#v", entries)
	}
	if got, want := model.Session().Summary(), "hello world"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}

	events <- textDelta("Hello ")
	model, command = runCommand(t, model, command)
	if len(responder.calls) != 1 {
		t.Fatalf("responder calls = %d, want 1", len(responder.calls))
	}
	call := responder.calls[0]
	if call.ctx == nil || call.buffer != queryEventBuffer || visibleAssistantText(&call.message) != "hello world" {
		t.Fatalf("responder call = %#v", call)
	}
	if got := model.transient; got != "Hello " {
		t.Fatalf("transient = %q, want streamed text", got)
	}
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "Hello ") {
		t.Fatalf("streaming view missing transient text: %q", content)
	}
	// Source hides the spinner while streamed assistant text is visible.
	if strings.Contains(content, "Thinking…") {
		t.Fatalf("streaming view still shows spinner inside/near prompt: %q", content)
	}
	// Before any text delta, the source-style verb should sit above the bordered composer.
	savedTransient := model.transient
	model.transient = ""
	idleBusy := ansi.Strip(strings.Join(model.renderPrompt(), "\n"))
	if !strings.Contains(idleBusy, model.status) {
		t.Fatalf("busy prompt missing spinner verb %q: %q", model.status, idleBusy)
	}
	if strings.Contains(idleBusy, "Thinking…") {
		t.Fatalf("busy prompt uses hardcoded initial Thinking status: %q", idleBusy)
	}
	// Status must not appear as a row inside the open-side border block.
	top := strings.Index(idleBusy, "─")
	statusAt := strings.Index(idleBusy, model.status)
	if top < 0 || statusAt < 0 || statusAt > top {
		t.Fatalf("spinner status should be above prompt border: statusAt=%d top=%d view=%q", statusAt, top, idleBusy)
	}
	model.transient = savedTransient

	events <- query.Event{Type: query.EventStream, Stream: &anthropicapi.StreamEvent{
		Type:  anthropicapi.StreamEventContentBlockDelta,
		Delta: &anthropicapi.ContentDelta{Type: "thinking_delta", Thinking: "hidden"},
	}}
	model, command = runCommand(t, model, command)
	if got := model.transient; got != "Hello " {
		t.Fatalf("non-text delta changed transient to %q", got)
	}

	events <- textDelta("world")
	model, command = runCommand(t, model, command)
	if got := model.transient; got != "Hello world" {
		t.Fatalf("transient = %q, want accumulated text", got)
	}

	assistant := core.AssistantMessage([]core.ContentBlock{
		core.TextBlock("Hello "),
		{Type: core.ContentBlockToolUse, ID: "call", Name: "hidden"},
		core.TextBlock("world"),
	})
	events <- query.Event{Type: query.EventAssistantMessage, Message: &assistant}
	model, command = runCommand(t, model, command)
	if model.transient != "" {
		t.Fatalf("transient after canonical assistant = %q", model.transient)
	}
	entries = model.Session().Entries()
	if len(entries) != 2 || entries[1] != (session.Entry{Role: core.RoleAssistant, Text: "Hello world"}) {
		t.Fatalf("committed entries = %#v", entries)
	}

	hidden := core.UserMessage("canonical hidden history")
	events <- query.Event{Type: query.EventToolResults, Message: &hidden}
	model, command = runCommand(t, model, command)
	events <- query.Event{Type: query.EventInjectedMessage, Message: &hidden}
	model, command = runCommand(t, model, command)
	if got := len(model.Session().Entries()); got != 2 {
		t.Fatalf("hidden canonical events added %d entries", got)
	}

	events <- query.Event{Type: query.EventCompleted, Result: &query.Result{Outcome: query.OutcomeEndTurn}}
	model, _ = runCommand(t, model, command)
	// With Blink disabled, Focus() may return a nil command; focused state is what matters.
	if model.busy || model.status != "" || model.transient != "" || model.cancel != nil || !model.input.Focused() || model.err != nil {
		t.Fatalf("completed model = busy %v status %q transient %q cancel %v focused %v err %v", model.busy, model.status, model.transient, model.cancel != nil, model.input.Focused(), model.err)
	}
	view := model.View()
	if view.AltScreen {
		t.Fatal("View() enables the alternate screen, want inline rendering")
	}
	content = ansi.Strip(view.Content)
	for _, want := range []string{"❯", "? for shortcuts", "─"} {
		if !strings.Contains(content, want) {
			t.Fatalf("live View() does not contain %q: %q", want, content)
		}
	}
	for _, unwanted := range []string{"▐▛███▜▌", "hello world", "Hello world", "Claude Code", "canonical hidden history"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("live View() still contains committed content %q: %q", unwanted, content)
		}
	}
	if got, want := view.WindowTitle, "hello world"; got != want {
		t.Fatalf("WindowTitle = %q, want %q", got, want)
	}
}

func TestThinkingStatusAdvancesAndTransitionsWithStream(t *testing.T) {
	events := make(chan query.Event, 4)
	model := newTestModelWithResponder(t, &fakeResponder{channels: []<-chan query.Event{events}})
	model.input.SetValue("thinking")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	message := command()
	output, ok := message.(staticOutputMsg)
	if !ok {
		t.Fatalf("submit command = %T, want staticOutputMsg", message)
	}
	updated, command = model.Update(output)
	model = updated.(Model)
	updated, _ = model.Update(staticOutputCommittedMsg{includeHeader: output.includeHeader, entryCount: output.entryCount, next: output.next})
	model = updated.(Model)
	if model.statusFrame != 0 {
		t.Fatalf("initial status frame = %d, want 0", model.statusFrame)
	}
	if model.status == "Thinking…" || !isSpinnerVerb(model.status) {
		t.Fatalf("initial status = %q, want a source-style spinner verb", model.status)
	}
	if !model.thinkingStarted.IsZero() {
		t.Fatalf("thinking started at submission: %s", model.thinkingStarted)
	}
	initialStatus := model.status
	initialLine := ansi.Strip(model.busyStatusLine())
	if !strings.Contains(initialLine, "· "+initialStatus) || strings.Contains(initialLine, "Thinking…") {
		t.Fatalf("initial spinner line = %q", initialLine)
	}

	events <- query.Event{Type: query.EventStream, Stream: &anthropicapi.StreamEvent{
		Type:  anthropicapi.StreamEventContentBlockStart,
		Block: &core.ContentBlock{Type: core.ContentBlockThinking},
	}}
	model, command = runCommand(t, model, output.next)
	_ = command
	if model.statusMode != statusThinking || model.thinkingStarted.IsZero() {
		t.Fatalf("thinking state = mode %d started %s", model.statusMode, model.thinkingStarted)
	}
	thinkingLine := ansi.Strip(model.busyStatusLine())
	if !strings.Contains(thinkingLine, initialStatus+" (thinking)") {
		t.Fatalf("thinking spinner line = %q", thinkingLine)
	}
	updated, _ = model.Update(statusTickMsg{
		generation: model.turnGeneration,
		at:         model.statusStarted.Add(spinnerFrameInterval),
	})
	model = updated.(Model)
	if model.statusFrame != 1 {
		t.Fatalf("status frame = %d, want 1", model.statusFrame)
	}
	advancedLine := ansi.Strip(model.busyStatusLine())
	if !strings.Contains(advancedLine, "✢ "+initialStatus) {
		t.Fatalf("advanced spinner line = %q", advancedLine)
	}

	events <- textDelta("answer")
	model, _ = runCommand(t, model, command)
	if model.statusMode != statusResponding || model.thoughtDuration <= 0 || model.status != initialStatus {
		t.Fatalf("transition state = mode %d duration %s status %q, want stable %q", model.statusMode, model.thoughtDuration, model.status, initialStatus)
	}
	model.transient = ""
	thoughtLine := ansi.Strip(model.busyStatusLine())
	if !strings.Contains(thoughtLine, initialStatus+" (thought for 1s)") {
		t.Fatalf("post-thinking spinner line = %q", thoughtLine)
	}
}

func TestSpinnerGlimmerUpdatesBetweenGlyphFrames(t *testing.T) {
	model := newTestModel(t)
	model.colorProfile = colorprofile.TrueColor
	model.updatePalette()
	model.busy = true
	model.status = "Orchestrating…"
	model.statusMode = statusThinking
	model.statusStarted = time.Now()
	model.thinkingStarted = model.statusStarted

	updated, _ := model.Update(statusTickMsg{
		generation: model.turnGeneration,
		at:         model.statusStarted.Add(2400 * time.Millisecond),
	})
	model = updated.(Model)
	firstFrame := model.statusFrame
	first := model.busyStatusLine()

	updated, _ = model.Update(statusTickMsg{
		generation: model.turnGeneration,
		at:         model.statusStarted.Add(2450 * time.Millisecond),
	})
	model = updated.(Model)
	second := model.busyStatusLine()
	if model.statusFrame != firstFrame {
		t.Fatalf("glyph advanced from frame %d to %d within 50ms", firstFrame, model.statusFrame)
	}
	if first == second {
		t.Fatal("50ms animation tick did not update the glimmer")
	}
	if ansi.Strip(first) != ansi.Strip(second) {
		t.Fatalf("glimmer changed visible text: first %q second %q", ansi.Strip(first), ansi.Strip(second))
	}
}

func TestCompletedLongTurnAddsPersistentFinishedTag(t *testing.T) {
	events := make(chan query.Event, 1)
	model := newTestModelWithResponder(t, &fakeResponder{channels: []<-chan query.Event{events}})
	model.input.SetValue("long turn")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	model.turnStarted = time.Now().Add(-(12*time.Minute + 12*time.Second))

	events <- query.Event{Type: query.EventCompleted, Result: &query.Result{Outcome: query.OutcomeEndTurn}}
	model, _ = runCommand(t, model, command)

	entries := model.Session().Entries()
	if len(entries) != 2 || entries[1].Style != session.EntryStyleTurnDuration {
		t.Fatalf("completed entries = %#v, want user + turn duration", entries)
	}
	parts := strings.SplitN(entries[1].Text, " for ", 2)
	if len(parts) != 2 || !isTurnCompletionVerb(parts[0]) || parts[1] != "12m 12s" {
		t.Fatalf("finished tag = %q", entries[1].Text)
	}
	static := ansi.Strip(model.renderStaticOutput(false, entries, 1, 2))
	if !strings.Contains(static, "✻ "+entries[1].Text) {
		t.Fatalf("finished tag static output = %q", static)
	}
}

func TestCompletedTurnDurationEligibility(t *testing.T) {
	for _, test := range []struct {
		name      string
		duration  time.Duration
		canceling bool
		outcome   query.Outcome
		want      bool
	}{
		{name: "over threshold", duration: 31 * time.Second, outcome: query.OutcomeEndTurn, want: true},
		{name: "at threshold", duration: 30 * time.Second, outcome: query.OutcomeEndTurn},
		{name: "canceling", duration: time.Minute, canceling: true, outcome: query.OutcomeEndTurn},
		{name: "canceled outcome", duration: time.Minute, outcome: query.OutcomeCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newTestModel(t)
			completedAt := time.Now()
			model.turnStarted = completedAt.Add(-test.duration)
			model.canceling = test.canceling
			event := query.Event{Result: &query.Result{Outcome: test.outcome}}
			if got := model.shouldAppendTurnDuration(event, completedAt); got != test.want {
				t.Fatalf("shouldAppendTurnDuration() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFormatTurnDuration(t *testing.T) {
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{
		{duration: 999 * time.Millisecond, want: "0s"},
		{duration: 12*time.Second + 999*time.Millisecond, want: "12s"},
		{duration: 12*time.Minute + 12*time.Second, want: "12m 12s"},
		{duration: 59*time.Minute + 59*time.Second + 600*time.Millisecond, want: "1h 0m 0s"},
		{duration: 2*time.Hour + 3*time.Minute + 4*time.Second, want: "2h 3m 4s"},
		{duration: 2*24*time.Hour + 3*time.Hour + 4*time.Minute + 45*time.Second, want: "2d 3h 4m"},
	} {
		if got := formatTurnDuration(test.duration); got != test.want {
			t.Fatalf("formatTurnDuration(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestModelAllowsOnlyOneActiveQuery(t *testing.T) {
	events := make(chan query.Event, 1)
	responder := &fakeResponder{channels: []<-chan query.Event{events}}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("first")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	events <- textDelta("working")
	model, next := runCommand(t, model, command)

	model.input.SetValue("second")
	updated, ignored := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if ignored != nil {
		t.Fatal("Enter while busy returned a command")
	}
	if len(responder.calls) != 1 || len(model.Session().Entries()) != 1 {
		t.Fatalf("busy submit calls=%d entries=%#v", len(responder.calls), model.Session().Entries())
	}
	if model.input.Value() != "second" {
		t.Fatalf("busy input changed to %q", model.input.Value())
	}
	_ = next
}

func TestSpinnerStartsBeforeFirstQueryEvent(t *testing.T) {
	events := make(chan query.Event)
	model := newTestModelWithResponder(t, &fakeResponder{channels: []<-chan query.Event{events}})
	model.input.SetValue("wait")

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	output := command().(staticOutputMsg)
	updated, command = model.Update(staticOutputCommittedMsg{
		includeHeader: output.includeHeader,
		entryCount:    output.entryCount,
		next:          output.next,
	})
	model = updated.(Model)

	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("post-commit command = %T len %d, want two-command batch", message, len(batch))
	}
	messages := make(chan tea.Msg, len(batch))
	for _, command := range batch {
		go func(command tea.Cmd) { messages <- command() }(command)
	}

	select {
	case message := <-messages:
		tick, ok := message.(statusTickMsg)
		if !ok || tick.generation != model.turnGeneration {
			t.Fatalf("first batch result = %#v, want active-turn status tick", message)
		}
	case <-time.After(time.Second):
		t.Fatal("spinner did not tick while first query event was silent")
	}
	model.cancel()
}

func TestCtrlCCancelsSilentResponderWithoutQuitting(t *testing.T) {
	events := make(chan query.Event)
	responder := &fakeResponder{
		channels: []<-chan query.Event{events},
		started:  make(chan struct{}, 1),
	}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("cancel silent")

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	output := command().(staticOutputMsg)
	queryResult := make(chan tea.Msg, 1)
	go func() { queryResult <- output.next() }()

	select {
	case <-responder.started:
	case <-time.After(time.Second):
		t.Fatal("responder did not start")
	}

	updated, cancelCommand := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(Model)
	if cancelCommand != nil {
		t.Fatal("first Ctrl+C should cancel without quitting")
	}

	select {
	case message := <-queryResult:
		updated, _ = model.Update(message)
		model = updated.(Model)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release silent responder wait")
	}
	if model.busy || model.canceling || !model.input.Focused() || model.err != nil {
		t.Fatalf("silent cancellation left stale state: busy=%v canceling=%v focused=%v err=%v", model.busy, model.canceling, model.input.Focused(), model.err)
	}
}

func TestStaleTurnMessagesCannotAffectActiveTurn(t *testing.T) {
	model := newTestModel(t)
	model.busy = true
	model.statusStarted = time.Now()
	model.turnGeneration = 2
	model.statusTicking = true

	updated, command := model.Update(statusTickMsg{
		generation: 1,
		at:         model.statusStarted.Add(time.Second),
	})
	model = updated.(Model)
	if command != nil || model.statusElapsed != 0 || model.statusFrame != 0 || !model.statusTicking {
		t.Fatalf("stale tick changed active turn: elapsed=%s frame=%d ticking=%v command=%v", model.statusElapsed, model.statusFrame, model.statusTicking, command != nil)
	}

	updated, command = model.Update(queryEventMsg{
		generation: 1,
		ok:         true,
		event: query.Event{
			Type:   query.EventCompleted,
			Result: &query.Result{Outcome: query.OutcomeEndTurn},
		},
	})
	model = updated.(Model)
	if command != nil || !model.busy {
		t.Fatal("stale completion finished the active turn")
	}
}

func TestCtrlCCancelsActiveQueryWithoutQuitting(t *testing.T) {
	events := make(chan query.Event, 2)
	responder := &fakeResponder{channels: []<-chan query.Event{events}}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("cancel me")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	events <- textDelta("partial")
	model, command = runCommand(t, model, command)

	updated, cancelCommand := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(Model)
	if cancelCommand != nil {
		t.Fatal("Ctrl+C while busy should cancel without quitting")
	}
	if model.status != "Canceling…" || !model.busy {
		t.Fatalf("cancel state = busy %v status %q", model.busy, model.status)
	}
	select {
	case <-responder.calls[0].ctx.Done():
	default:
		t.Fatal("Ctrl+C did not cancel responder context")
	}

	events <- query.Event{Type: query.EventCompleted, Result: &query.Result{Outcome: query.OutcomeCanceled}, Err: context.Canceled}
	model, _ = runCommand(t, model, command)
	if model.busy || model.status != "" || model.transient != "" || model.err != nil || !model.input.Focused() {
		t.Fatalf("canceled completion left stale state: %#v", model)
	}
}

func TestSecondCtrlCForceQuitsActiveQuery(t *testing.T) {
	events := make(chan query.Event, 1)
	events <- textDelta("working")
	responder := &fakeResponder{channels: []<-chan query.Event{events}}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("cancel me")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	model, command = runCommand(t, model, command)
	_ = command

	updated, first := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(Model)
	if first != nil || !model.canceling || model.status != "Canceling…" {
		t.Fatalf("first Ctrl+C command=%v canceling=%v status=%q", first != nil, model.canceling, model.status)
	}
	_, second := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if second == nil {
		t.Fatal("second Ctrl+C should force quit")
	}
}

func TestCompletionErrorSurfacesTerminalOutcomes(t *testing.T) {
	for _, outcome := range []query.Outcome{
		query.OutcomeMaxTokens,
		query.OutcomeRefusal,
		query.OutcomePauseTurn,
		query.OutcomeToolTurnLimit,
		query.OutcomeFailed,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			err := completionError(query.Event{Result: &query.Result{Outcome: outcome}})
			if err == nil || !strings.Contains(err.Error(), string(outcome)) {
				t.Fatalf("completionError(%q) = %v", outcome, err)
			}
		})
	}
	for _, outcome := range []query.Outcome{query.OutcomeEndTurn, query.OutcomeStopSequence, query.OutcomeCanceled} {
		t.Run("quiet "+string(outcome), func(t *testing.T) {
			if err := completionError(query.Event{Result: &query.Result{Outcome: outcome}, Err: context.Canceled}); outcome == query.OutcomeCanceled && err != nil {
				t.Fatalf("canceled completion error = %v", err)
			} else if outcome != query.OutcomeCanceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("explicit event error was lost: %v", err)
			}
		})
	}
	for _, outcome := range []query.Outcome{query.OutcomeEndTurn, query.OutcomeStopSequence} {
		if err := completionError(query.Event{Result: &query.Result{Outcome: outcome}}); err != nil {
			t.Fatalf("completionError(%q) = %v, want nil", outcome, err)
		}
	}
}

func TestCompletedErrorClearsBusyStateAndDisplaysError(t *testing.T) {
	events := make(chan query.Event, 1)
	responder := &fakeResponder{channels: []<-chan query.Event{events}}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("fail")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	failure := errors.New("request failed")
	events <- query.Event{Type: query.EventCompleted, Result: &query.Result{Outcome: query.OutcomeFailed}, Err: failure}
	model, _ = runCommand(t, model, command)
	if model.busy || model.status != "" || model.transient != "" || !errors.Is(model.err, failure) || !model.input.Focused() {
		t.Fatalf("failed completion state = busy %v status %q transient %q err %v focused %v", model.busy, model.status, model.transient, model.err, model.input.Focused())
	}
	if content := ansi.Strip(model.View().Content); !strings.Contains(content, "Error: request failed") {
		t.Fatalf("error view = %q", content)
	}
}

func TestAuthFailureRendersInTranscriptNotComposer(t *testing.T) {
	events := make(chan query.Event, 1)
	responder := &fakeResponder{channels: []<-chan query.Event{events}}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("hello")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)

	authErr := &core.APIError{
		Kind:    core.APIErrorAuth,
		Message: "no Anthropic credentials found. The SDK tried these sources in order:\n  1. ANTHROPIC_API_KEY env var: not set",
	}
	events <- query.Event{Type: query.EventCompleted, Result: &query.Result{Outcome: query.OutcomeFailed}, Err: authErr}
	model, _ = runCommand(t, model, command)

	if model.busy || model.err != nil || !model.input.Focused() {
		t.Fatalf("auth failure left composer error: busy=%v err=%v focused=%v", model.busy, model.err, model.input.Focused())
	}
	entries := model.Session().Entries()
	if len(entries) != 2 {
		t.Fatalf("entries = %#v, want user + error", entries)
	}
	if entries[1] != (session.Entry{Role: core.RoleAssistant, Text: notLoggedInMessage, Style: session.EntryStyleError}) {
		t.Fatalf("transcript error entry = %#v", entries[1])
	}

	content := ansi.Strip(model.View().Content)
	if strings.Contains(content, notLoggedInMessage) || strings.Contains(content, "⎿") {
		t.Fatalf("committed auth transcript remained in live view: %q", content)
	}
	if !strings.Contains(content, notLoggedInFooter) {
		t.Fatalf("view missing footer auth notice: %q", content)
	}
	if strings.Contains(content, "Error: ") {
		t.Fatalf("auth failure still rendered in composer: %q", content)
	}

	staticError := ansi.Strip(model.renderStaticOutput(false, entries, 1, 2))
	if !strings.Contains(staticError, notLoggedInMessage) || !strings.Contains(staticError, "⎿") {
		t.Fatalf("static auth transcript = %q", staticError)
	}
	if strings.Contains(staticError, "no Anthropic credentials found") || strings.Contains(staticError, "ANTHROPIC_API_KEY") {
		t.Fatalf("raw credential diagnostics leaked into static transcript: %q", staticError)
	}
}

func TestUserVisibleQueryErrorMapsAuthKinds(t *testing.T) {
	if got := userVisibleQueryError(&core.APIError{Kind: core.APIErrorAuth, Message: "x"}); got != notLoggedInMessage {
		t.Fatalf("auth kind = %q", got)
	}
	if got := userVisibleQueryError(&core.APIError{Kind: core.APIErrorPermission, Message: "x"}); got != notLoggedInMessage {
		t.Fatalf("permission kind = %q", got)
	}
	if got := userVisibleQueryError(errors.New("unknown: no Anthropic credentials found")); got != notLoggedInMessage {
		t.Fatalf("message fallback = %q", got)
	}
	if got := userVisibleQueryError(errors.New("request failed")); got != "" {
		t.Fatalf("generic error mapped to %q", got)
	}
}

func TestResponderCloseWithoutCompletionRecoversInput(t *testing.T) {
	events := make(chan query.Event)
	close(events)
	model := newTestModelWithResponder(t, &fakeResponder{channels: []<-chan query.Event{events}})
	model.input.SetValue("unexpected close")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	model, _ = runCommand(t, model, command)
	if model.busy || !errors.Is(model.err, ErrResponderEnded) || !model.input.Focused() {
		t.Fatalf("closed responder state = busy %v err %v focused %v", model.busy, model.err, model.input.Focused())
	}
}

func TestConstructorsRequireSessionAndResponder(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilSession) {
		t.Fatalf("New(nil) error = %v", err)
	}
	if _, err := New(session.New()); !errors.Is(err, ErrNilResponder) {
		t.Fatalf("New() error = %v, want ErrNilResponder", err)
	}
	if _, err := NewWithConfig(session.New(), Config{}); !errors.Is(err, ErrNilResponder) {
		t.Fatalf("NewWithConfig() error = %v, want ErrNilResponder", err)
	}
}

func TestSubmitPrintsHeaderAndUserBeforeStartingQuery(t *testing.T) {
	responder := &fakeResponder{}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("hello")

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if command == nil || model.headerPrinted || model.printedEntries != 0 || !model.staticQueued || !model.queuedHeader || model.queuedEntryCount != 1 {
		t.Fatalf("submit static state = printed header %v entries %d queued %v queued header %v queued entries %d command %v", model.headerPrinted, model.printedEntries, model.staticQueued, model.queuedHeader, model.queuedEntryCount, command != nil)
	}
	if len(responder.calls) != 0 {
		t.Fatal("query started before static transcript output")
	}

	message := command()
	output, ok := message.(staticOutputMsg)
	if !ok {
		t.Fatalf("submit command message = %T, want staticOutputMsg", message)
	}
	static := ansi.Strip(output.content)
	for _, want := range []string{"Claude Code", "vtest", "hello"} {
		if !strings.Contains(static, want) {
			t.Fatalf("static output missing %q: %q", want, static)
		}
	}
	lines := strings.Split(static, "\n")
	if len(lines) < 5 || lines[3] != "" {
		t.Fatalf("static header/user margin = %#v, want blank row after 3-line header", lines)
	}
	if output.next == nil {
		t.Fatal("static output did not retain query continuation")
	}

	updated, sequence := model.Update(output)
	model = updated.(Model)
	if !model.staticQueued || model.headerPrinted || model.printedEntries != 0 || sequence == nil {
		t.Fatalf("scheduled static output = queued %v header %v entries %d sequence %v", model.staticQueued, model.headerPrinted, model.printedEntries, sequence != nil)
	}

	updated, continuation := model.Update(staticOutputCommittedMsg{
		includeHeader: output.includeHeader,
		entryCount:    output.entryCount,
		next:          output.next,
	})
	model = updated.(Model)
	if model.staticQueued || !model.headerPrinted || model.printedEntries != 1 || continuation == nil {
		t.Fatalf("committed static output = queued %v header %v entries %d continuation %v", model.staticQueued, model.headerPrinted, model.printedEntries, continuation != nil)
	}
	if len(responder.calls) != 0 {
		t.Fatal("query started before static output commit")
	}

	live := ansi.Strip(model.View().Content)
	if strings.Contains(live, "Claude Code") || strings.Contains(live, "hello") {
		t.Fatalf("live view retained committed header/transcript: %q", live)
	}
	liveLines := strings.Split(live, "\n")
	statusLine := ""
	if len(liveLines) > 1 {
		statusLine = strings.TrimSpace(liveLines[1])
	}
	if len(liveLines) < 6 || strings.TrimSpace(liveLines[0]) != "" || statusLine != "· "+model.status || strings.Contains(statusLine, "Thinking…") || strings.TrimSpace(liveLines[2]) != "" || !strings.Contains(liveLines[3], "─") || !strings.HasPrefix(liveLines[4], "❯") || !strings.Contains(liveLines[5], "─") {
		t.Fatalf("live status/composer order = %#v", liveLines)
	}
}

func TestStaticOutputPrintsOnlyNewTranscriptRows(t *testing.T) {
	model := newTestModel(t)
	mustAppendUser(t, model.session, "first")
	firstCommand := model.queueStaticOutput(nil)
	firstMessage := firstCommand()
	first, ok := firstMessage.(staticOutputMsg)
	if !ok {
		t.Fatalf("first static command = %T", firstMessage)
	}
	if content := ansi.Strip(first.content); !strings.Contains(content, "Claude Code") || !strings.Contains(content, "first") {
		t.Fatalf("first static output = %q", content)
	}
	updated, _ := model.Update(staticOutputCommittedMsg{
		includeHeader: first.includeHeader,
		entryCount:    first.entryCount,
		next:          first.next,
	})
	model = updated.(Model)

	mustAppendAssistant(t, model.session, "second")
	secondCommand := model.queueStaticOutput(nil)
	secondMessage := secondCommand()
	second, ok := secondMessage.(staticOutputMsg)
	if !ok {
		t.Fatalf("second static command = %T", secondMessage)
	}
	content := ansi.Strip(second.content)
	if !strings.HasPrefix(content, "\n") || !strings.Contains(content, "second") {
		t.Fatalf("second static output lacks source margin/message: %q", content)
	}
	for _, repeated := range []string{"Claude Code", "first"} {
		if strings.Contains(content, repeated) {
			t.Fatalf("second static output repeated %q: %q", repeated, content)
		}
	}
}

func TestInlineViewRendersRegionsSequentially(t *testing.T) {
	model := newTestModel(t)
	mustAppendUser(t, model.session, "earliest transcript entry")
	mustAppendAssistant(t, model.session, "first response")
	mustAppendUser(t, model.session, "latest transcript entry")
	mustAppendAssistant(t, model.session, "latest response")

	content := ansi.Strip(model.View().Content)
	headerIndex := strings.Index(content, "Claude Code")
	earliestIndex := strings.Index(content, "earliest transcript entry")
	latestIndex := strings.Index(content, "latest transcript entry")
	// Composer has no placeholder; the open-side prompt border sits just above the footer.
	footerIndex := strings.Index(content, "? for shortcuts")
	composerIndex := -1
	if footerIndex > 0 {
		composerIndex = strings.LastIndex(content[:footerIndex], "─")
	}
	if !(headerIndex >= 0 && headerIndex < earliestIndex && earliestIndex < latestIndex && latestIndex < composerIndex && composerIndex < footerIndex) {
		t.Fatalf("regions are not sequential: header=%d earliest=%d latest=%d composer=%d footer=%d\n%s", headerIndex, earliestIndex, latestIndex, composerIndex, footerIndex, content)
	}
	if !strings.Contains(content, "vtest") {
		t.Fatalf("header missing dim version: %q", content)
	}
}

func TestInlineViewKeepsCompleteHistoryPastTerminalHeight(t *testing.T) {
	model := newTestModel(t)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 3})
	model = updated.(Model)
	for _, message := range []string{"message-zero", "message-one", "message-two", "message-three", "message-four"} {
		mustAppendUser(t, model.session, message)
		mustAppendAssistant(t, model.session, "response to "+message)
	}

	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "message-zero") || !strings.Contains(content, "message-four") {
		t.Fatalf("inline view clipped history: %q", content)
	}
}

func TestShiftEnterInsertsNewlineAndEnterSubmits(t *testing.T) {
	model := newTestModel(t)
	model.input.SetValue("first line")

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	model = updated.(Model)
	if got, want := model.input.Value(), "first line\n"; got != want {
		t.Fatalf("input after Shift+Enter = %q, want %q", got, want)
	}
	if got := len(model.Session().Entries()); got != 0 {
		t.Fatalf("Shift+Enter submitted %d entries, want 0", got)
	}
	if model.input.Height() != 2 {
		t.Fatalf("input height after Shift+Enter = %d, want 2", model.input.Height())
	}

	model.input.InsertString("second line")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if command == nil {
		t.Fatal("multiline Enter did not start query")
	}
	entries := model.Session().Entries()
	if len(entries) != 1 || entries[0].Text != "first line\nsecond line" {
		t.Fatalf("multiline submission entries = %#v", entries)
	}
	if model.input.Height() != 1 {
		t.Fatalf("input height after submit = %d, want 1", model.input.Height())
	}
}

func TestComposerGrowsWithoutFullscreenHeightCap(t *testing.T) {
	model := newTestModel(t)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	model = updated.(Model)
	if got := model.input.MaxHeight; got != 0 {
		t.Fatalf("input MaxHeight = %d, want no non-fullscreen cap", got)
	}

	updated, _ = model.Update(tea.KeyPressMsg{Text: strings.Repeat("x", 300), Code: 'x'})
	model = updated.(Model)
	if got := model.input.Height(); got <= 7 {
		t.Fatalf("wrapped input height = %d, want growth beyond former fullscreen cap", got)
	}
	before := model.input.Height()
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 20, Height: 100})
	model = updated.(Model)
	if got := model.input.Height(); got != before {
		t.Fatalf("input height changed from %d to %d based only on terminal height", before, got)
	}
}

func TestPromptUsesOpenSidesAndNonbreakingMarkerSpace(t *testing.T) {
	model := newTestModel(t)
	model.colorProfile = colorprofile.ASCII
	model.updatePalette()
	model.input.SetValue("first\nsecond")

	prompt := ansi.Strip(strings.Join(model.renderPrompt(), "\n"))
	if strings.ContainsAny(prompt, "│╭╮╰╯") {
		t.Fatalf("prompt contains side walls or corners: %q", prompt)
	}
	if got := strings.Count(prompt, strings.Repeat("─", model.width)); got != 2 {
		t.Fatalf("full-width border count = %d, want 2: %q", got, prompt)
	}
	if !strings.Contains(prompt, "❯ first") {
		t.Fatalf("prompt does not contain marker with NBSP: %q", prompt)
	}
	if !strings.Contains(prompt, "\n  second") {
		t.Fatalf("continuation row is not indented: %q", prompt)
	}
}

func TestFooterSuppressesHintWhenInputNonEmpty(t *testing.T) {
	model := newTestModel(t)
	emptyFooter := ansi.Strip(strings.Join(model.renderFooter(), "\n"))
	if !strings.Contains(emptyFooter, "? for shortcuts") {
		t.Fatalf("empty input footer = %q, want shortcuts hint", emptyFooter)
	}

	model.input.SetValue("typing")
	busyFooter := model.renderFooter()
	if len(busyFooter) != 0 {
		t.Fatalf("non-empty input footer = %q, want suppressed", busyFooter)
	}
}

func TestHeaderSplitsTitleAndVersion(t *testing.T) {
	model := newTestModel(t)
	header := ansi.Strip(strings.Join(model.renderHeader(), "\n"))
	if !strings.Contains(header, "Claude Code") || !strings.Contains(header, "vtest") {
		t.Fatalf("header = %q, want bold title + dim version", header)
	}
	// Clawd mark still present as plain glyphs after strip.
	if !strings.Contains(header, "▐▛███▜▌") || !strings.Contains(header, "▝▜█████▛▘") {
		t.Fatalf("header missing clawd mark: %q", header)
	}
}

func TestTranscriptWrapsWithinWidthWithContinuationGutter(t *testing.T) {
	model := newTestModel(t)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 10, Height: 2})
	model = updated.(Model)
	mustAppendUser(t, model.session, "abcdefghijklmno世界")

	transcript := ansi.Strip(strings.Join(model.renderTranscript(), "\n"))
	if !strings.Contains(transcript, "\n  ijklmno") {
		t.Fatalf("wrapped continuation lacks gutter: %q", transcript)
	}
	for line := range strings.SplitSeq(transcript, "\n") {
		if width := lipgloss.Width(line); width > model.width {
			t.Fatalf("transcript line width = %d, want <= %d: %q", width, model.width, line)
		}
	}
	if !strings.Contains(transcript, "世界") {
		t.Fatalf("wrapped transcript lost wide text: %q", transcript)
	}
}

func TestModelIgnoresBlankSubmission(t *testing.T) {
	responder := &fakeResponder{}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue(" \t ")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if command != nil || len(responder.calls) != 0 {
		t.Fatal("blank submission started a responder")
	}
	if len(model.Session().Entries()) != 0 {
		t.Fatalf("blank submission added entries: %#v", model.Session().Entries())
	}
	if got := model.View().WindowTitle; got != "Claude Code" {
		t.Fatalf("WindowTitle = %q, want fallback", got)
	}
}

func TestModelResizesNarrowlyWithoutClippingLogicalLayout(t *testing.T) {
	model := newTestModel(t)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	model = updated.(Model)
	if model.width != 1 {
		t.Fatalf("width = %d, want 1", model.width)
	}
	if model.input.MaxHeight != 0 {
		t.Fatalf("input MaxHeight = %d, want 0", model.input.MaxHeight)
	}
	content := model.View().Content
	if content == "" || strings.Count(content, "\n") < 4 {
		t.Fatalf("View() at minimum size = %q, want complete sequential layout", content)
	}
	for line := range strings.SplitSeq(content, "\n") {
		if width := lipgloss.Width(line); width > model.width {
			t.Fatalf("rendered line width = %d, want <= %d: %q", width, model.width, line)
		}
	}
}

func TestColorMessagesRepaintExistingTranscript(t *testing.T) {
	model := newTestModel(t)
	mustAppendUser(t, model.session, "hello")
	mustAppendAssistant(t, model.session, "response")
	updated, _ := model.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	model = updated.(Model)

	darkView := model.View().Content
	if !strings.Contains(darkView, "48;2;55;55;55") {
		t.Fatalf("dark view does not use source user background: %q", darkView)
	}
	updated, _ = model.Update(tea.BackgroundColorMsg{Color: color.White})
	model = updated.(Model)
	lightView := model.View().Content
	if !strings.Contains(lightView, "48;2;240;240;240") {
		t.Fatalf("light view does not use source user background: %q", lightView)
	}
	if darkView == lightView {
		t.Fatal("background update did not repaint the view")
	}
}

func TestModelCtrlCQuitsWhenIdle(t *testing.T) {
	model := newTestModel(t)
	_, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("Ctrl+C should return a quit command while idle")
	}
}

func TestTruncatePreservesDisplayWidth(t *testing.T) {
	if got, want := truncateDisplay("hello", 5), "hello"; got != want {
		t.Fatalf("truncateDisplay() = %q, want %q", got, want)
	}
	if got, want := truncateDisplay("hello", 4), "hel…"; got != want {
		t.Fatalf("truncateDisplay() = %q, want %q", got, want)
	}
	if got, want := truncateDisplay("世界你好", 3), "世…"; got != want {
		t.Fatalf("truncateDisplay() = %q, want %q", got, want)
	}
}

func isSpinnerVerb(status string) bool {
	for _, verb := range spinnerVerbs {
		if status == verb+"…" {
			return true
		}
	}
	return false
}

func isTurnCompletionVerb(verb string) bool {
	return slices.Contains(turnCompletionVerbs, verb)
}

func textDelta(text string) query.Event {
	return query.Event{Type: query.EventStream, Stream: &anthropicapi.StreamEvent{
		Type:  anthropicapi.StreamEventContentBlockDelta,
		Delta: &anthropicapi.ContentDelta{Type: "text_delta", Text: text},
	}}
}

func runCommand(t *testing.T, model Model, command tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	if command == nil {
		t.Fatal("expected non-nil command")
	}

	message := command()
	if output, ok := message.(staticOutputMsg); ok {
		updated, _ := model.Update(staticOutputCommittedMsg{
			includeHeader: output.includeHeader,
			entryCount:    output.entryCount,
			next:          output.next,
		})
		model = updated.(Model)
		if output.next == nil {
			return model, nil
		}
		message = output.next()
	}

	updated, next := model.Update(message)
	model = updated.(Model)
	if !model.staticQueued || next == nil {
		return model, next
	}

	output, ok := next().(staticOutputMsg)
	if !ok {
		t.Fatal("static output was queued without a staticOutputMsg command")
	}
	updated, _ = model.Update(staticOutputCommittedMsg{
		includeHeader: output.includeHeader,
		entryCount:    output.entryCount,
		next:          output.next,
	})
	return updated.(Model), output.next
}

func mustAppendUser(t *testing.T, state *session.Session, text string) {
	t.Helper()
	if _, err := state.AppendUser(text); err != nil {
		t.Fatalf("AppendUser() error = %v", err)
	}
}

func mustAppendAssistant(t *testing.T, state *session.Session, text string) {
	t.Helper()
	if err := state.AppendAssistant(text); err != nil {
		t.Fatalf("AppendAssistant() error = %v", err)
	}
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	return newTestModelWithResponder(t, &fakeResponder{})
}

func newTestModelWithResponder(t *testing.T, responder Responder) Model {
	t.Helper()
	model, err := NewWithConfig(session.New(), Config{
		Responder:        responder,
		Version:          "test",
		Model:            "claude-test",
		WorkingDirectory: "/tmp/code-cli",
	})
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v", err)
	}
	return model
}
