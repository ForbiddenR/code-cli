package tui

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"code-cli/internal/core"
	"code-cli/internal/query"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.String()
}

type runtimeResponder struct {
	output         *lockedBuffer
	outputAtSubmit string
}

func (responder *runtimeResponder) SubmitEvents(context.Context, core.Message, int) <-chan query.Event {
	responder.outputAtSubmit = responder.output.String()
	events := make(chan query.Event, 1)
	events <- query.Event{
		Type:   query.EventCompleted,
		Result: &query.Result{Outcome: query.OutcomeEndTurn},
	}
	close(events)
	return events
}

type runtimeSubmitMsg struct{}

type runtimeModel struct {
	Model
}

func (model runtimeModel) Init() tea.Cmd {
	return func() tea.Msg { return runtimeSubmitMsg{} }
}

func (model runtimeModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := message.(runtimeSubmitMsg); ok {
		updated, command := model.Model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		model.Model = updated.(Model)
		return model, command
	}

	wasBusy := model.busy
	updated, command := model.Model.Update(message)
	model.Model = updated.(Model)
	if wasBusy && !model.busy {
		return model, tea.Sequence(command, tea.Quit)
	}
	return model, command
}

type scriptedRuntimeResponder struct {
	output         *lockedBuffer
	answers        []string
	prompts        []string
	outputAtSubmit []string
}

func (responder *scriptedRuntimeResponder) SubmitEvents(_ context.Context, message core.Message, _ int) <-chan query.Event {
	responder.prompts = append(responder.prompts, visibleAssistantText(&message))
	responder.outputAtSubmit = append(responder.outputAtSubmit, responder.output.String())
	index := len(responder.prompts) - 1
	answer := responder.answers[index]
	assistant := core.AssistantMessage([]core.ContentBlock{core.TextBlock(answer)})
	events := make(chan query.Event, 3)
	events <- textDelta(answer)
	events <- query.Event{Type: query.EventAssistantMessage, Message: &assistant}
	events <- query.Event{Type: query.EventCompleted, Result: &query.Result{Outcome: query.OutcomeEndTurn}}
	close(events)
	return events
}

type scriptedSubmitMsg struct{}

type scriptedRuntimeModel struct {
	Model
	prompts []string
	next    int
}

func (model scriptedRuntimeModel) Init() tea.Cmd {
	return func() tea.Msg { return scriptedSubmitMsg{} }
}

func (model scriptedRuntimeModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := message.(scriptedSubmitMsg); ok {
		updated, command := model.Model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		model.Model = updated.(Model)
		return model, command
	}

	updated, command := model.Model.Update(message)
	model.Model = updated.(Model)
	if _, committed := message.(staticOutputCommittedMsg); !committed || model.busy || model.staticQueued {
		return model, command
	}
	if model.next >= len(model.prompts) {
		return model, tea.Sequence(command, tea.Quit)
	}
	model.input.SetValue(model.prompts[model.next])
	model.next++
	return model, tea.Sequence(command, func() tea.Msg { return scriptedSubmitMsg{} })
}

func TestProgramKeepsComposerStableAcrossMultipleTurns(t *testing.T) {
	var output lockedBuffer
	prompts := []string{"first question", "second question", "third question"}
	answers := []string{
		"first answer",
		"second answer line 1\nsecond answer line 2\nsecond answer line 3\nsecond answer line 4\nsecond answer line 5\nsecond answer line 6\nsecond answer line 7\nsecond answer line 8",
		"third answer",
	}
	responder := &scriptedRuntimeResponder{output: &output, answers: answers}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue(prompts[0])

	program := tea.NewProgram(
		scriptedRuntimeModel{Model: model, prompts: prompts[1:]},
		tea.WithWindowSize(40, 10),
		tea.WithColorProfile(colorprofile.ANSI),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}),
		tea.WithInput(bytes.NewReader(nil)),
		tea.WithOutput(&output),
		tea.WithoutSignals(),
	)
	final, err := program.Run()
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(responder.prompts) != len(prompts) || len(responder.outputAtSubmit) != len(prompts) {
		t.Fatalf("submitted prompts = %#v snapshots=%d", responder.prompts, len(responder.outputAtSubmit))
	}
	for index, prompt := range prompts {
		if responder.prompts[index] != prompt {
			t.Fatalf("prompt %d = %q, want %q", index, responder.prompts[index], prompt)
		}
		start := 0
		if index > 0 {
			start = len(responder.outputAtSubmit[index-1])
		}
		delta := ansi.Strip(responder.outputAtSubmit[index][start:])
		if !strings.Contains(delta, prompt) {
			t.Fatalf("query %d started before prompt insertion: %q", index, delta)
		}
		for earlier := range index {
			if strings.Contains(delta, prompts[earlier]) {
				t.Fatalf("query %d replayed committed prompt %q: %q", index, prompts[earlier], delta)
			}
		}
	}

	finalModel := final.(scriptedRuntimeModel)
	if finalModel.busy || !finalModel.input.Focused() {
		t.Fatalf("final composer state = busy %v focused %v", finalModel.busy, finalModel.input.Focused())
	}
	allOutput := output.String()
	if strings.Contains(allOutput, "\x1b[?1049h") {
		t.Fatal("program entered the alternate screen")
	}
	plain := ansi.Strip(allOutput)
	for _, value := range prompts {
		if !strings.Contains(plain, value) {
			t.Fatalf("terminal output missing %q", value)
		}
	}
	for _, answer := range answers {
		for line := range strings.SplitSeq(answer, "\n") {
			if !strings.Contains(plain, line) {
				t.Fatalf("terminal output missing answer line %q", line)
			}
		}
	}
	if got := strings.Count(plain, "✻ "); got < len(prompts) {
		t.Fatalf("finish tags = %d, want at least %d", got, len(prompts))
	}
}

func TestProgramPrintsStaticTranscriptBeforeStartingQuery(t *testing.T) {
	var output lockedBuffer
	responder := &runtimeResponder{output: &output}
	model := newTestModelWithResponder(t, responder)
	model.input.SetValue("runtime hello")

	program := tea.NewProgram(
		runtimeModel{Model: model},
		tea.WithWindowSize(80, 24),
		tea.WithColorProfile(colorprofile.ANSI),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}),
		tea.WithInput(bytes.NewReader(nil)),
		tea.WithOutput(&output),
		tea.WithoutSignals(),
	)
	if _, err := program.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	atSubmit := responder.outputAtSubmit
	if atSubmit == "" {
		t.Fatal("responder started before the renderer produced output")
	}
	insertLine := regexp.MustCompile(`\x1b\[[0-9]+L`).FindStringIndex(atSubmit)
	if insertLine == nil {
		t.Fatalf("output before query has no insert-line sequence: %q", atSubmit)
	}
	inserted := atSubmit[insertLine[1]:]
	for _, want := range []string{"Claude Code", "runtime hello"} {
		if !strings.Contains(inserted, want) {
			t.Fatalf("static insertion before query missing %q: %q", want, inserted)
		}
	}
	if strings.Contains(atSubmit, "\x1b[?1049h") || strings.Contains(output.String(), "\x1b[?1049h") {
		t.Fatal("program entered the alternate screen")
	}
}
