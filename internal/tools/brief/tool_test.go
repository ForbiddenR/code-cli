package brief

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const wantToolPrompt = "Send a message the user will read. Text outside this tool is visible in the detail view, but most won't open it — the answer lives here.\n\n" +
	"`message` supports markdown. `attachments` takes file paths (absolute or cwd-relative) for images, diffs, logs.\n\n" +
	"`status` labels intent: 'normal' when replying to what they just asked; 'proactive' when you're initiating — a scheduled task finished, a blocker surfaced during background work, you need input on something they haven't asked about. Set it honestly; downstream routing uses it."

const wantSystemPrompt = "## Talking to the user\n\n" +
	"SendUserMessage is where your replies go. Text outside it is visible if the user expands the detail view, but most won't — assume unread. Anything you want them to actually see goes through SendUserMessage. The failure mode: the real answer lives in plain text while SendUserMessage just says \"done!\" — they see \"done!\" and miss everything.\n\n" +
	"So: every time the user says something, the reply they actually read comes through SendUserMessage. Even for \"hi\". Even for \"thanks\".\n\n" +
	"If you can answer right away, send the answer. If you need to go look — run a command, read files, check something — ack first in one line (\"On it — checking the test output\"), then work, then send the result. Without the ack they're staring at a spinner.\n\n" +
	"For longer work: ack → work → result. Between those, send a checkpoint when something useful happened — a decision you made, a surprise you hit, a phase boundary. Skip the filler (\"running tests...\") — a checkpoint earns its place by carrying information.\n\n" +
	"Keep messages tight — the decision, the file:line, the PR number. Second person always (\"your config\"), never third."

func TestDefinitionAndPrompts(t *testing.T) {
	if ToolName != "SendUserMessage" || LegacyToolName != "Brief" || Description != "Send a message to the user" {
		t.Fatalf("tool identity = %q %q %q", ToolName, LegacyToolName, Description)
	}
	if ToolPrompt != wantToolPrompt || SystemPrompt != wantSystemPrompt {
		t.Fatal("Brief prompts do not match the TypeScript reference")
	}
	definition := Definition()
	if definition.Name != ToolName || definition.Description != ToolPrompt {
		t.Fatalf("definition = %#v", definition)
	}
	if !MatchesName(ToolName) || !MatchesName(LegacyToolName) || MatchesName("brief") {
		t.Fatal("tool alias matching mismatch")
	}
	aliases := Aliases()
	if len(aliases) != 1 || aliases[0] != LegacyToolName {
		t.Fatalf("aliases = %#v", aliases)
	}
	aliases[0] = "changed"
	if Aliases()[0] != LegacyToolName {
		t.Fatal("Aliases returned shared mutable state")
	}

	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
			Items       *struct {
				Type string `json:"type"`
			} `json:"items"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatalf("schema JSON: %v", err)
	}
	if schema.Type != "object" || schema.AdditionalProperties || strings.Join(schema.Required, ",") != "message,status" {
		t.Fatalf("schema shape = %#v", schema)
	}
	if schema.Properties["message"].Description != "The message for the user. Supports markdown formatting." {
		t.Fatalf("message schema = %#v", schema.Properties["message"])
	}
	attachments := schema.Properties["attachments"]
	if attachments.Items == nil || attachments.Items.Type != "string" {
		t.Fatalf("attachments schema = %#v", attachments)
	}
	status := schema.Properties["status"]
	if strings.Join(status.Enum, ",") != "normal,proactive" {
		t.Fatalf("status schema = %#v", status)
	}
}

func TestParseInput(t *testing.T) {
	valid := []struct {
		name string
		json string
		want Input
	}{
		{name: "minimal", json: `{"message":"hello","status":"normal"}`, want: Input{Message: "hello", Status: StatusNormal}},
		{name: "empty message", json: `{"message":"","status":"proactive"}`, want: Input{Status: StatusProactive}},
		{name: "attachments", json: `{"message":"m","attachments":["a","b"],"status":"normal"}`, want: Input{Message: "m", Attachments: []string{"a", "b"}, Status: StatusNormal}},
		{name: "empty attachments", json: `{"message":"m","attachments":[],"status":"normal"}`, want: Input{Message: "m", Attachments: []string{}, Status: StatusNormal}},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseInput([]byte(test.json))
			if err != nil {
				t.Fatalf("ParseInput() error = %v", err)
			}
			if got.Message != test.want.Message || got.Status != test.want.Status || strings.Join(got.Attachments, "\x00") != strings.Join(test.want.Attachments, "\x00") {
				t.Fatalf("ParseInput() = %#v, want %#v", got, test.want)
			}
		})
	}

	invalid := []string{
		``, `null`, `[]`, `{"status":"normal"}`, `{"message":"m"}`,
		`{"message":null,"status":"normal"}`, `{"message":"m","status":null}`,
		`{"message":"m","attachments":null,"status":"normal"}`,
		`{"message":"m","attachments":[null],"status":"normal"}`,
		`{"message":"m","attachments":[1],"status":"normal"}`,
		`{"message":"m","status":"NORMAL"}`, `{"message":"m","status":"other"}`,
		`{"message":"m","status":"normal","extra":true}`,
		`{"Message":"m","Status":"normal"}`,
		`{"message":"m","status":"normal"} {}`, `{"message":"m","status":"normal"} trailing`,
	}
	for _, value := range invalid {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseInput([]byte(value)); err == nil {
				t.Fatalf("ParseInput(%q) succeeded", value)
			}
		})
	}
}

func TestCallWithoutAttachments(t *testing.T) {
	nowCalls := 0
	tool := New(Config{
		Now: func() time.Time {
			nowCalls++
			return time.Date(2026, time.July, 25, 16, 3, 2, 123987654, time.FixedZone("west", -7*60*60))
		},
		Getwd: func() (string, error) { t.Fatal("Getwd called without attachments"); return "", nil },
		Stat:  func(string) (fs.FileInfo, error) { t.Fatal("Stat called without attachments"); return nil, nil },
	})
	got, err := tool.Call(context.Background(), Input{Message: "hello", Status: StatusNormal})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got.Message != "hello" || got.SentAt != "2026-07-25T23:03:02.123Z" || got.Attachments != nil || nowCalls != 1 {
		t.Fatalf("output = %#v, now calls = %d", got, nowCalls)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "attachments") {
		t.Fatalf("empty attachments were serialized: %s", data)
	}
}

func TestCallResolvesAndUploadsAttachments(t *testing.T) {
	directory := t.TempDir()
	paths := []string{"first.PNG", "second.txt", "third.webp"}
	for index, name := range paths {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(strings.Repeat("x", index+1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	uploader := &recordingUploader{delays: map[string]time.Duration{
		"first.PNG":  20 * time.Millisecond,
		"second.txt": 10 * time.Millisecond,
	}}
	uploader.fail = map[string]error{"second.txt": errors.New("offline")}
	uploader.uuid = map[string]string{"first.PNG": "uuid-1", "third.webp": ""}
	statSawClock := false
	clockCalled := false
	tool := New(Config{
		Now:   func() time.Time { clockCalled = true; return time.UnixMilli(123456789) },
		Getwd: func() (string, error) { return directory, nil },
		Stat: func(path string) (fs.FileInfo, error) {
			statSawClock = clockCalled
			return os.Stat(path)
		},
		Uploader: uploader,
	})
	got, err := tool.Call(context.Background(), Input{Message: "files", Attachments: paths, Status: StatusProactive})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !statSawClock || len(got.Attachments) != 3 {
		t.Fatalf("timestamp ordering or attachments = %#v", got)
	}
	if filepath.Base(got.Attachments[0].Path) != "first.PNG" || got.Attachments[0].Size != 1 || !got.Attachments[0].IsImage || got.Attachments[0].FileUUID != "uuid-1" {
		t.Fatalf("first attachment = %#v", got.Attachments[0])
	}
	if filepath.Base(got.Attachments[1].Path) != "second.txt" || got.Attachments[1].Size != 2 || got.Attachments[1].IsImage || got.Attachments[1].FileUUID != "" {
		t.Fatalf("second attachment = %#v", got.Attachments[1])
	}
	if filepath.Base(got.Attachments[2].Path) != "third.webp" || got.Attachments[2].Size != 3 || !got.Attachments[2].IsImage || got.Attachments[2].FileUUID != "" {
		t.Fatalf("third attachment = %#v", got.Attachments[2])
	}
	if uploader.callCount() != 3 {
		t.Fatalf("upload calls = %d", uploader.callCount())
	}
	data, err := json.Marshal(got.Attachments)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "file_uuid") != 1 {
		t.Fatalf("empty file UUID serialized: %s", data)
	}
}

func TestMapToolResultToToolResultBlockParam(t *testing.T) {
	for _, test := range []struct {
		count int
		want  string
	}{
		{count: 0, want: "Message delivered to user."},
		{count: 1, want: "Message delivered to user. (1 attachment included)"},
		{count: 2, want: "Message delivered to user. (2 attachments included)"},
	} {
		output := Output{Message: "secret message", SentAt: "secret timestamp", Attachments: make([]Attachment, test.count)}
		if test.count > 0 {
			output.Attachments[0] = Attachment{Path: "secret path", FileUUID: "secret uuid"}
		}
		got := MapToolResultToToolResultBlockParam(output, "toolu_1")
		if got.ToolUseID != "toolu_1" || got.Type != "tool_result" || got.Content != test.want {
			t.Fatalf("result block = %#v", got)
		}
		for _, secret := range []string{"secret message", "secret timestamp", "secret path", "secret uuid"} {
			if strings.Contains(got.Content, secret) {
				t.Fatalf("result leaked %q: %q", secret, got.Content)
			}
		}
	}
}

func TestCallRejectsInvalidStatusAndNilTool(t *testing.T) {
	if _, err := New(Config{}).Call(context.Background(), Input{Status: "invalid"}); err == nil {
		t.Fatal("invalid status succeeded")
	}
	var tool *Tool
	if _, err := tool.Call(context.Background(), Input{Status: StatusNormal}); err == nil {
		t.Fatal("nil tool succeeded")
	}
}

type recordingUploader struct {
	mu     sync.Mutex
	calls  []string
	delays map[string]time.Duration
	fail   map[string]error
	uuid   map[string]string
}

func (u *recordingUploader) Upload(_ context.Context, path string, _ int64) (string, error) {
	name := filepath.Base(path)
	u.mu.Lock()
	u.calls = append(u.calls, name)
	u.mu.Unlock()
	time.Sleep(u.delays[name])
	return u.uuid[name], u.fail[name]
}

func (u *recordingUploader) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}
