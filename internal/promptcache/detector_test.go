package promptcache

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"code-cli/internal/core"
)

func TestDetectsPromptCacheBreakWithPendingChanges(t *testing.T) {
	detector := NewDetector()
	base := snapshot("repl_main_thread", "claude-opus-4-8", "system", []ToolSchema{tool("Read", `{"type":"object"}`)})
	detector.RecordPromptState(base)
	if report := detector.CheckResponseForCacheBreak(observation("repl_main_thread", 10_000)); report != nil {
		t.Fatalf("first CheckResponseForCacheBreak() = %#v", report)
	}

	changed := snapshot("repl_main_thread", "claude-opus-4-7", "system plus", []ToolSchema{tool("Read", `{"type":"object","properties":{"path":{"type":"string"}}}`), tool("mcp__server__tool", `{"type":"object"}`)})
	changed.Betas = []string{"beta-b", "beta-a"}
	changed.FastMode = true
	changed.GlobalCacheStrategy = "tool_based"
	changed.EffortValue = "high"
	changed.ExtraBodyParams = map[string]any{"speed": "fast"}
	detector.RecordPromptState(changed)

	report := detector.CheckResponseForCacheBreak(observation("repl_main_thread", 7_000))
	if report == nil {
		t.Fatal("expected cache break report")
	}
	for _, want := range []string{
		"model changed (claude-opus-4-8 → claude-opus-4-7)",
		"system prompt changed (+5 chars)",
		"tools changed (+1/-0 tools)",
		"fast mode toggled",
		"global cache strategy changed (none → tool_based)",
		"betas changed (+beta-a,beta-b)",
		"effort changed (default → high)",
		"extra body params changed",
	} {
		if !strings.Contains(report.Reason, want) {
			t.Fatalf("reason %q missing %q", report.Reason, want)
		}
	}
	if report.PreviousCacheReadTokens != 10_000 || report.CacheReadTokens != 7_000 || report.TokenDrop != 3_000 {
		t.Fatalf("report tokens = %#v", report)
	}
	if report.CallNumber != 2 {
		t.Fatalf("call number = %d", report.CallNumber)
	}
	if !reflect.DeepEqual(report.Changes.AddedTools, []string{"mcp"}) {
		t.Fatalf("added tools = %#v", report.Changes.AddedTools)
	}
	if !reflect.DeepEqual(report.Changes.ChangedToolSchemas, []string{"Read"}) {
		t.Fatalf("changed tool schemas = %#v", report.Changes.ChangedToolSchemas)
	}
	if !strings.Contains(report.Summary, "[PROMPT CACHE BREAK]") || !strings.Contains(report.Summary, "cache read: 10000 → 7000") {
		t.Fatalf("summary = %q", report.Summary)
	}
}

func TestIgnoresSmallCacheReadDrops(t *testing.T) {
	detector := NewDetector()
	base := snapshot("sdk", "claude-opus-4-8", "system", nil)
	detector.RecordPromptState(base)
	detector.CheckResponseForCacheBreak(observation("sdk", 10_000))

	base.System[0].Text = "changed system"
	detector.RecordPromptState(base)
	if report := detector.CheckResponseForCacheBreak(observation("sdk", 9_600)); report != nil {
		t.Fatalf("small relative drop report = %#v", report)
	}

	base.System[0].Text = "changed system again"
	detector.RecordPromptState(base)
	if report := detector.CheckResponseForCacheBreak(observation("sdk", 8_100)); report != nil {
		t.Fatalf("small absolute drop report = %#v", report)
	}
}

func TestDetectsTTLAndServerSideReasonsWithoutPendingChanges(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	detector := NewDetector()
	detector.Now = func() time.Time { return now }
	base := snapshot("repl_main_thread", "claude-opus-4-8", "system", nil)
	detector.RecordPromptState(base)
	detector.CheckResponseForCacheBreak(observation("repl_main_thread", 10_000))

	detector.RecordPromptState(base)
	lastAssistant := now.Add(-2 * time.Hour)
	report := detector.CheckResponseForCacheBreak(Observation{QuerySource: "repl_main_thread", CacheReadTokens: 7_000, LastAssistantMessageTime: &lastAssistant})
	if report == nil || report.Reason != "possible 1h TTL expiry (prompt unchanged)" || !report.LastAssistantOver1HourAgo {
		t.Fatalf("ttl report = %#v", report)
	}

	detector.RecordPromptState(base)
	detector.CheckResponseForCacheBreak(observation("repl_main_thread", 10_000))
	detector.RecordPromptState(base)
	lastAssistant = now.Add(-time.Minute)
	report = detector.CheckResponseForCacheBreak(Observation{QuerySource: "repl_main_thread", CacheReadTokens: 7_000, LastAssistantMessageTime: &lastAssistant})
	if report == nil || report.Reason != "likely server-side (prompt unchanged, <5min gap)" || report.LastAssistantOver5MinAgo {
		t.Fatalf("server-side report = %#v", report)
	}
}

func TestCacheDeletionAndCompactionSuppressExpectedDrops(t *testing.T) {
	detector := NewDetector()
	base := snapshot("repl_main_thread", "claude-opus-4-8", "system", nil)
	detector.RecordPromptState(base)
	detector.CheckResponseForCacheBreak(observation("repl_main_thread", 10_000))

	detector.NotifyCacheDeletion("repl_main_thread", "")
	detector.RecordPromptState(base)
	if report := detector.CheckResponseForCacheBreak(observation("repl_main_thread", 3_000)); report != nil {
		t.Fatalf("cache deletion report = %#v", report)
	}

	detector.RecordPromptState(base)
	detector.CheckResponseForCacheBreak(observation("repl_main_thread", 10_000))
	detector.NotifyCompaction("repl_main_thread", "")
	detector.RecordPromptState(base)
	if report := detector.CheckResponseForCacheBreak(observation("repl_main_thread", 2_000)); report != nil {
		t.Fatalf("compaction report = %#v", report)
	}
}

func TestTrackingRulesAndExcludedModels(t *testing.T) {
	detector := NewDetector()
	base := snapshot("untracked", "claude-opus-4-8", "system", nil)
	detector.RecordPromptState(base)
	if report := detector.CheckResponseForCacheBreak(observation("untracked", 1)); report != nil {
		t.Fatalf("untracked report = %#v", report)
	}

	base = snapshot("compact", "claude-opus-4-8", "system", nil)
	detector.RecordPromptState(base)
	detector.CheckResponseForCacheBreak(observation("repl_main_thread", 10_000))
	base.System[0].Text = "changed"
	detector.RecordPromptState(base)
	if report := detector.CheckResponseForCacheBreak(observation("repl_main_thread", 7_000)); report == nil {
		t.Fatal("expected compact to share repl_main_thread tracking")
	}

	haikuDetector := NewDetector()
	haiku := snapshot("sdk", "claude-haiku-4-5", "system", nil)
	haikuDetector.RecordPromptState(haiku)
	haikuDetector.CheckResponseForCacheBreak(observation("sdk", 10_000))
	haiku.System[0].Text = "changed"
	haikuDetector.RecordPromptState(haiku)
	if report := haikuDetector.CheckResponseForCacheBreak(observation("sdk", 1_000)); report != nil {
		t.Fatalf("haiku report = %#v", report)
	}
}

func TestCacheControlChangeIsTrackedSeparately(t *testing.T) {
	detector := NewDetector()
	base := snapshot("sdk", "claude-opus-4-8", "system", nil)
	base.System[0].CacheControl = &core.CacheControl{Type: core.CacheControlEphemeral}
	detector.RecordPromptState(base)
	detector.CheckResponseForCacheBreak(observation("sdk", 10_000))

	base.System[0].CacheControl = nil
	detector.RecordPromptState(base)
	report := detector.CheckResponseForCacheBreak(observation("sdk", 7_000))
	if report == nil || report.Reason != "cache_control changed (scope or TTL)" || !report.Changes.CacheControlChanged {
		t.Fatalf("cache control report = %#v", report)
	}
}

func TestToolSchemasFromCoreCopiesInputSchemas(t *testing.T) {
	input := json.RawMessage(`{"type":"object"}`)
	schemas := ToolSchemasFromCore([]core.ToolDefinition{{Name: "Read", Description: "read", InputSchema: input}})
	input[1] = 'X'
	if len(schemas) != 1 || schemas[0].Name != "Read" || string(schemas[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("schemas = %#v", schemas)
	}
}

func snapshot(source string, model string, systemText string, tools []ToolSchema) Snapshot {
	return Snapshot{
		QuerySource: source,
		Model:       model,
		System: []core.SystemBlock{{
			Type: core.ContentBlockText,
			Text: systemText,
		}},
		ToolSchemas: tools,
	}
}

func observation(source string, cacheRead int64) Observation {
	return Observation{QuerySource: source, CacheReadTokens: cacheRead, CacheCreationTokens: 100, RequestID: "req_123"}
}

func tool(name string, schema string) ToolSchema {
	return ToolSchema{Name: name, Description: "tool " + name, InputSchema: json.RawMessage(schema)}
}
