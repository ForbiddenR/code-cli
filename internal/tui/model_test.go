package tui

import (
	"context"
	"errors"
	"image/color"
	"strings"
	"testing"

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
}

func (responder *fakeResponder) SubmitEvents(ctx context.Context, message core.Message, buffer int) <-chan query.Event {
	responder.calls = append(responder.calls, responderCall{ctx: ctx, message: message, buffer: buffer})
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
	// Before any text delta, status should sit above the bordered composer.
	savedTransient := model.transient
	model.transient = ""
	model.status = "Thinking…"
	idleBusy := ansi.Strip(strings.Join(model.renderPrompt(), "\n"))
	if !strings.Contains(idleBusy, "Thinking…") {
		t.Fatalf("busy prompt missing Thinking status: %q", idleBusy)
	}
	// Status must not appear as a row inside the open-side border block.
	// Border top is a full-width ─ line; Thinking should be above it.
	top := strings.Index(idleBusy, "─")
	statusAt := strings.Index(idleBusy, "Thinking…")
	if top < 0 || statusAt < 0 || statusAt > top {
		t.Fatalf("Thinking status should be above prompt border: statusAt=%d top=%d view=%q", statusAt, top, idleBusy)
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
	if len(liveLines) < 6 || strings.TrimSpace(liveLines[0]) != "" || strings.TrimSpace(liveLines[1]) != "● Thinking…" || strings.TrimSpace(liveLines[2]) != "" || !strings.Contains(liveLines[3], "─") || !strings.HasPrefix(liveLines[4], "❯") || !strings.Contains(liveLines[5], "─") {
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
