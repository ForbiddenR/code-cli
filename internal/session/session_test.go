package session

import (
	"testing"

	"code-cli/internal/core"
)

func TestSessionTracksFirstUserMessageAsSummary(t *testing.T) {
	session := New()
	if _, err := session.AppendUser("  first question  "); err != nil {
		t.Fatalf("AppendUser() error = %v", err)
	}
	if err := session.AppendAssistant("first response"); err != nil {
		t.Fatalf("AppendAssistant() error = %v", err)
	}
	if _, err := session.AppendUser("second question"); err != nil {
		t.Fatalf("AppendUser() error = %v", err)
	}

	if got, want := session.Summary(), "first question"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	entries := session.Entries()
	if len(entries) != 3 {
		t.Fatalf("Entries() length = %d, want 3", len(entries))
	}
	if entries[0] != (Entry{Role: core.RoleUser, Text: "first question"}) {
		t.Fatalf("first entry = %#v", entries[0])
	}
}

func TestSessionRejectsBlankMessagesWithoutChangingState(t *testing.T) {
	session := New()
	if _, err := session.AppendUser(" \n\t "); err != ErrBlankMessage {
		t.Fatalf("AppendUser() error = %v, want %v", err, ErrBlankMessage)
	}
	if err := session.AppendAssistant(" "); err != ErrBlankMessage {
		t.Fatalf("AppendAssistant() error = %v, want %v", err, ErrBlankMessage)
	}
	if session.Summary() != "" || len(session.Entries()) != 0 {
		t.Fatalf("blank messages changed session: summary=%q entries=%v", session.Summary(), session.Entries())
	}
}

func TestSessionEntriesAreDefensive(t *testing.T) {
	session := New()
	if _, err := session.AppendUser("question"); err != nil {
		t.Fatalf("AppendUser() error = %v", err)
	}
	entries := session.Entries()
	entries[0].Text = "changed"
	entries = append(entries, Entry{Role: core.RoleAssistant, Text: "extra"})

	stored := session.Entries()
	if len(stored) != 1 || stored[0].Text != "question" {
		t.Fatalf("session entries were not defensive: %#v", stored)
	}
}

func TestNilSessionAccessors(t *testing.T) {
	var session *Session
	if session.Summary() != "" {
		t.Fatal("nil Summary() should be empty")
	}
	if session.Entries() != nil {
		t.Fatal("nil Entries() should be nil")
	}
}

func TestSessionAppendErrorDoesNotChangeSummary(t *testing.T) {
	session := New()
	if _, err := session.AppendUser("question"); err != nil {
		t.Fatalf("AppendUser() error = %v", err)
	}
	if err := session.AppendError("Not logged in · Please run /login"); err != nil {
		t.Fatalf("AppendError() error = %v", err)
	}
	if got, want := session.Summary(), "question"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	entries := session.Entries()
	if len(entries) != 2 {
		t.Fatalf("Entries() length = %d, want 2", len(entries))
	}
	if entries[1] != (Entry{Role: core.RoleAssistant, Text: "Not logged in · Please run /login", Style: EntryStyleError}) {
		t.Fatalf("error entry = %#v", entries[1])
	}
}
