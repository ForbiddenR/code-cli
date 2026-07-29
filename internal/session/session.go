// Package session stores the in-memory conversation state used by the TUI.
package session

import (
	"errors"
	"strings"

	"code-cli/internal/core"
)

var ErrBlankMessage = errors.New("message is blank")

// EntryStyle controls transcript rendering for an entry.
type EntryStyle string

const (
	// EntryStyleDefault is normal user/assistant text.
	EntryStyleDefault EntryStyle = ""
	// EntryStyleError renders an assistant-style API error (source MessageResponse).
	EntryStyleError EntryStyle = "error"
	// EntryStyleTurnDuration renders the persisted completion row for a finished turn.
	EntryStyleTurnDuration EntryStyle = "turn_duration"
)

// Entry is one normalized conversation entry.
type Entry struct {
	Role  core.Role
	Text  string
	Style EntryStyle
}

// Session owns the transcript and the summary for one interactive session.
type Session struct {
	summary string
	entries []Entry
}

// New creates an empty in-memory session.
func New() *Session {
	return &Session{}
}

// AppendUser adds a user entry and uses the first valid user message as summary.
func (session *Session) AppendUser(text string) (Entry, error) {
	entry, err := newEntry(core.RoleUser, text)
	if err != nil {
		return Entry{}, err
	}
	if session.summary == "" {
		session.summary = entry.Text
	}
	session.entries = append(session.entries, entry)
	return entry, nil
}

// AppendAssistant adds an assistant entry without changing the summary.
func (session *Session) AppendAssistant(text string) error {
	entry, err := newEntry(core.RoleAssistant, text)
	if err != nil {
		return err
	}
	session.entries = append(session.entries, entry)
	return nil
}

// AppendError adds a transcript API-error entry without changing the summary.
// These match the source assistant isApiErrorMessage path and are not composer status.
func (session *Session) AppendError(text string) error {
	entry, err := newEntry(core.RoleAssistant, text)
	if err != nil {
		return err
	}
	entry.Style = EntryStyleError
	session.entries = append(session.entries, entry)
	return nil
}

// AppendTurnDuration adds a source-style completed-turn row without changing the summary.
func (session *Session) AppendTurnDuration(text string) error {
	entry, err := newEntry(core.RoleAssistant, text)
	if err != nil {
		return err
	}
	entry.Style = EntryStyleTurnDuration
	session.entries = append(session.entries, entry)
	return nil
}

// Summary returns the first valid user message, or an empty string initially.
func (session *Session) Summary() string {
	if session == nil {
		return ""
	}
	return session.summary
}

// Entries returns a defensive copy of the transcript.
func (session *Session) Entries() []Entry {
	if session == nil {
		return nil
	}
	return append([]Entry(nil), session.entries...)
}

func newEntry(role core.Role, text string) (Entry, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Entry{}, ErrBlankMessage
	}
	return Entry{Role: role, Text: text}, nil
}
