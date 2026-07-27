package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"code-cli/internal/session"
)

func TestModelEchoesSubmittedMessageAndKeepsFirstSummary(t *testing.T) {
	model := newTestModel(t)
	model.input.SetValue("  hello world  ")

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil {
		t.Fatal("submit should not start an asynchronous command")
	}
	model = updated.(Model)

	if got, want := model.Session().Summary(), "hello world"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	entries := model.Session().Entries()
	if len(entries) != 2 {
		t.Fatalf("Entries() length = %d, want 2", len(entries))
	}
	if entries[0].Text != "hello world" || entries[1].Text != "hello world" {
		t.Fatalf("echo entries = %#v", entries)
	}
	if model.input.Value() != "" {
		t.Fatalf("input value = %q, want empty", model.input.Value())
	}
	view := model.View()
	if view.AltScreen {
		t.Fatal("View() enables the alternate screen, want inline rendering")
	}
	content := ansi.Strip(view.Content)
	for _, want := range []string{"▐▛███▜▌", "hello world", "❯ ", "● ", "Claude Code vtest", "? for shortcuts", "─"} {
		if !strings.Contains(content, want) {
			t.Fatalf("View() does not contain %q: %q", want, content)
		}
	}
	if got, want := view.WindowTitle, "hello world"; got != want {
		t.Fatalf("WindowTitle = %q, want %q", got, want)
	}

	model.input.SetValue("second message")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if got, want := model.Session().Summary(), "hello world"; got != want {
		t.Fatalf("Summary() after second message = %q, want %q", got, want)
	}
	if got := len(model.Session().Entries()); got != 4 {
		t.Fatalf("Entries() length after second message = %d, want 4", got)
	}
	if got := model.View().WindowTitle; got != "hello world" {
		t.Fatalf("WindowTitle after second message = %q, want first summary", got)
	}
}

func TestInlineViewRendersRegionsSequentially(t *testing.T) {
	model := newTestModel(t)
	for _, message := range []string{"earliest transcript entry", "latest transcript entry"} {
		model.input.SetValue(message)
		updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		model = updated.(Model)
	}

	content := ansi.Strip(model.View().Content)
	headerIndex := strings.Index(content, "Claude Code vtest")
	earliestIndex := strings.Index(content, "earliest transcript entry")
	latestIndex := strings.LastIndex(content, "latest transcript entry")
	composerIndex := strings.Index(content, "Type a message…")
	footerIndex := strings.Index(content, "? for shortcuts")
	if !(headerIndex >= 0 && headerIndex < earliestIndex && earliestIndex < latestIndex && latestIndex < composerIndex && composerIndex < footerIndex) {
		t.Fatalf("regions are not sequential: header=%d earliest=%d latest=%d composer=%d footer=%d\n%s", headerIndex, earliestIndex, latestIndex, composerIndex, footerIndex, content)
	}
}

func TestInlineViewKeepsCompleteHistoryPastTerminalHeight(t *testing.T) {
	model := newTestModel(t)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 3})
	model = updated.(Model)
	for index, message := range []string{"message-zero", "message-one", "message-two", "message-three", "message-four"} {
		model.input.SetValue(message)
		updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		model = updated.(Model)
		if got := len(model.Session().Entries()); got != (index+1)*2 {
			t.Fatalf("entries after message %d = %d", index, got)
		}
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
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	entries := model.Session().Entries()
	if len(entries) != 2 {
		t.Fatalf("multiline submission entries = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if got, want := entry.Text, "first line\nsecond line"; got != want {
			t.Fatalf("multiline entry = %q, want %q", got, want)
		}
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

func TestTranscriptWrapsWithinWidthWithContinuationGutter(t *testing.T) {
	model := newTestModel(t)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 10, Height: 2})
	model = updated.(Model)
	model.input.SetValue("abcdefghijklmno世界")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)

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
	model, err := New(session.New())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	model.input.SetValue(" \t ")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if len(model.Session().Entries()) != 0 {
		t.Fatalf("blank submission added entries: %#v", model.Session().Entries())
	}
	if got := model.View().WindowTitle; got != "Claude Code" {
		t.Fatalf("WindowTitle = %q, want fallback", got)
	}
}

func TestModelResizesNarrowlyWithoutClippingLogicalLayout(t *testing.T) {
	model, err := New(session.New())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
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
	model.input.SetValue("hello")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
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

func TestModelCtrlCQuits(t *testing.T) {
	model, err := New(session.New())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("Ctrl+C should return a quit command")
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

func newTestModel(t *testing.T) Model {
	t.Helper()
	model, err := NewWithConfig(session.New(), Config{
		Version:          "test",
		Model:            "Local echo",
		WorkingDirectory: "/tmp/code-cli",
	})
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v", err)
	}
	return model
}
