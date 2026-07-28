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
