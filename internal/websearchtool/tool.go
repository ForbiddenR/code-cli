// Package websearchtool implements the server-side WebSearchTool used by the
// Claude Code query streaming path.
package websearchtool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
)

const (
	// ToolName is the Claude Code tool name exposed to the model/runtime.
	ToolName = "WebSearch"
	// ServerToolName is the Anthropic server-side tool name.
	ServerToolName = "web_search"
	// ServerToolType identifies the supported Anthropic web-search tool version.
	ServerToolType = anthropicapi.ServerToolWebSearch20250305
	// MaxUses matches the TypeScript WebSearchTool hard limit.
	MaxUses = 8
)

// Input is the validated WebSearchTool input.
type Input struct {
	Query          string   `json:"query"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

// SearchHit is one link returned by the provider web-search tool.
type SearchHit struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// SearchResult groups links produced by one server-tool invocation.
type SearchResult struct {
	ToolUseID string      `json:"tool_use_id"`
	Content   []SearchHit `json:"content"`
}

// Result is either model commentary or a group of search links.
type Result struct {
	Text   string
	Search *SearchResult
}

// MarshalJSON preserves the TypeScript union output shape: strings remain
// strings and search results remain objects.
func (r Result) MarshalJSON() ([]byte, error) {
	if r.Search != nil {
		return json.Marshal(r.Search)
	}
	return json.Marshal(r.Text)
}

// UnmarshalJSON accepts either a model commentary string or a search-result
// object, including transcript data that was round-tripped through JSON.
func (r *Result) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*r = Result{}
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &r.Text)
	}
	var result SearchResult
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	r.Search = &result
	return nil
}

// Output is the structured result returned by WebSearchTool.
type Output struct {
	Query           string   `json:"query"`
	Results         []Result `json:"results"`
	DurationSeconds float64  `json:"durationSeconds"`
}

// ProgressType identifies a WebSearchTool progress update.
type ProgressType string

const (
	ProgressQueryUpdate     ProgressType = "query_update"
	ProgressResultsReceived ProgressType = "search_results_received"
)

// ProgressEvent is emitted while the provider executes one or more searches.
type ProgressEvent struct {
	ToolUseID   string       `json:"toolUseID"`
	Type        ProgressType `json:"type"`
	Query       string       `json:"query"`
	ResultCount int          `json:"resultCount,omitempty"`
}

// ProgressFunc receives best-effort search progress updates.
type ProgressFunc func(ProgressEvent)

// Config controls a WebSearchTool call.
type Config struct {
	Client       anthropicapi.Client
	MainModel    core.ModelID
	FastModel    core.ModelID
	UseFastModel bool
	Thinking     *core.ThinkingConfig
	MaxTokens    int
	Timeout      time.Duration
	Now          func() time.Time
}

// DefaultConfig returns a production-shaped configuration with stable model
// defaults. Authentication and provider-specific configuration belong to the
// injected anthropicapi.Client.
func DefaultConfig(client anthropicapi.Client) Config {
	return Config{
		Client:    client,
		MainModel: core.DefaultModel,
		FastModel: core.ModelClaudeHaiku45,
		MaxTokens: anthropicapi.DefaultMaxTokens,
		Now:       time.Now,
	}
}

// WebSearchTool is the focused Go tool implementation.
type WebSearchTool struct {
	config Config
}

// New constructs a WebSearchTool with caller-supplied API configuration.
func New(config Config) *WebSearchTool {
	if config.MainModel == "" {
		config.MainModel = core.DefaultModel
	}
	if config.FastModel == "" {
		config.FastModel = core.ModelClaudeHaiku45
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = anthropicapi.DefaultMaxTokens
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &WebSearchTool{config: config}
}

// ValidateInput mirrors the TypeScript schema and tool-level validation.
func ValidateInput(input Input) ValidationResult {
	if utf8.RuneCountInString(input.Query) < 2 {
		if input.Query == "" {
			return ValidationResult{Message: "Error: Missing query", ErrorCode: 1}
		}
		return ValidationResult{Message: "Error: Query must be at least 2 characters", ErrorCode: 1}
	}
	if len(input.AllowedDomains) > 0 && len(input.BlockedDomains) > 0 {
		return ValidationResult{
			Message:   "Error: Cannot specify both allowed_domains and blocked_domains in the same request",
			ErrorCode: 2,
		}
	}
	return ValidationResult{Valid: true}
}

// ValidationResult describes local input validation without making an API call.
type ValidationResult struct {
	Valid     bool
	Message   string
	ErrorCode int
}

// IsEnabled reports whether the supplied provider/model combination supports
// Anthropic server-side web search.
func IsEnabled(provider string, model core.ModelID) bool {
	switch provider {
	case "firstParty", "foundry":
		return true
	case "vertex":
		name := model.String()
		return strings.Contains(name, "claude-opus-4") ||
			strings.Contains(name, "claude-sonnet-4") ||
			strings.Contains(name, "claude-haiku-4")
	default:
		return false
	}
}

// Prompt returns the web-search system prompt for the supplied local time.
func Prompt(now time.Time) string {
	monthYear := now.Local().Format("January 2006")
	return fmt.Sprintf(`
- Allows Claude to search the web and use the results to inform responses
- Provides up-to-date information for current events and recent data
- Returns search result information formatted as search result blocks, including links as markdown hyperlinks
- Use this tool for accessing information beyond Claude's knowledge cutoff
- Searches are performed automatically within a single API call

CRITICAL REQUIREMENT - You MUST follow this:
  - After answering the user's question, you MUST include a "Sources:" section at the end of your response
  - In the Sources section, list all relevant URLs from the search results as markdown hyperlinks: [Title](URL)
  - This is MANDATORY - never skip including sources in your response
  - Example format:

    [Your answer here]

    Sources:
    - [Source Title 1](https://example.com/1)
    - [Source Title 2](https://example.com/2)

Usage notes:
  - Domain filtering is supported to include or block specific websites
  - Web search is only available in the US

IMPORTANT - Use the correct year in search queries:
  - The current month is %s. You MUST use this year when searching for recent information, documentation, or current events.
  - Example: If the user asks for "latest React docs", search for "React documentation" with the current year, NOT last year
`, monthYear)
}

// Call executes a server-side web search and returns structured results.
func (t *WebSearchTool) Call(ctx context.Context, input Input, onProgress ProgressFunc) (Output, error) {
	if t == nil {
		return Output{}, errors.New("web search tool is nil")
	}
	validation := ValidateInput(input)
	if !validation.Valid {
		return Output{}, errors.New(validation.Message)
	}
	if t.config.Client == nil {
		return Output{}, errors.New("web search client is nil")
	}

	now := t.config.Now()
	start := now
	model := t.config.MainModel
	thinking := t.config.Thinking
	var toolChoice *anthropicapi.ToolChoice
	if t.config.UseFastModel {
		model = t.config.FastModel
		thinking = nil
		toolChoice = &anthropicapi.ToolChoice{Type: "tool", Name: ServerToolName}
	}

	request := anthropicapi.MessageRequest{
		Model:     model,
		MaxTokens: t.config.MaxTokens,
		System:    []core.SystemBlock{core.TextSystemBlock(Prompt(now))},
		Messages:  []core.Message{core.UserMessage("Perform a web search for the query: " + input.Query)},
		ServerTools: []anthropicapi.ServerToolDefinition{{
			Type:           ServerToolType,
			Name:           ServerToolName,
			MaxUses:        MaxUses,
			AllowedDomains: append([]string(nil), input.AllowedDomains...),
			BlockedDomains: append([]string(nil), input.BlockedDomains...),
		}},
		ToolChoice: toolChoice,
		Thinking:   thinking,
	}

	var callOptions []anthropicapi.CallOption
	if t.config.Timeout > 0 {
		callOptions = append(callOptions, anthropicapi.WithTimeout(t.config.Timeout))
	}
	stream, err := t.config.Client.StreamMessage(ctx, request, callOptions...)
	if err != nil {
		return Output{}, err
	}
	if stream == nil {
		return Output{}, errors.New("web search stream is nil")
	}
	defer stream.Close()

	blocks := make(map[int]core.ContentBlock)
	order := make([]int, 0, 4)
	partialInput := make(map[int]string)
	toolUseQueries := make(map[string]string)
	progressCounter := 0
	for event := range stream.Events() {
		if event.Error != nil {
			return Output{}, event.Error
		}
		switch event.Type {
		case anthropicapi.StreamEventContentBlockStart:
			if event.Block == nil {
				continue
			}
			if _, exists := blocks[event.Index]; !exists {
				order = append(order, event.Index)
			}
			blocks[event.Index] = *event.Block
			if event.Block.Type == core.ContentBlockServerToolUse {
				partialInput[event.Index] = ""
			}
			if event.Block.Type == core.ContentBlockWebSearchToolResult {
				actualQuery := input.Query
				if event.Block.ToolUseID != "" {
					if tracked, ok := toolUseQueries[event.Block.ToolUseID]; ok {
						actualQuery = tracked
					}
				}
				progressCounter++
				emitProgress(onProgress, ProgressEvent{
					ToolUseID:   firstNonEmpty(event.Block.ToolUseID, fmt.Sprintf("search-progress-%d", progressCounter)),
					Type:        ProgressResultsReceived,
					Query:       actualQuery,
					ResultCount: len(event.Block.Content),
				})
			}
		case anthropicapi.StreamEventContentBlockDelta:
			if event.Delta == nil {
				continue
			}
			block, exists := blocks[event.Index]
			if !exists {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				block.Text += event.Delta.Text
			case "thinking_delta":
				block.Thinking += event.Delta.Thinking
			case "signature_delta":
				block.Signature += event.Delta.Signature
			case "input_json_delta":
				partialInput[event.Index] += event.Delta.PartialJSON
				if block.Type == core.ContentBlockServerToolUse {
					if query, ok := extractQuery(partialInput[event.Index]); ok && toolUseQueries[block.ID] != query {
						toolUseQueries[block.ID] = query
						progressCounter++
						emitProgress(onProgress, ProgressEvent{
							ToolUseID: fmt.Sprintf("search-progress-%d", progressCounter),
							Type:      ProgressQueryUpdate,
							Query:     query,
						})
					}
				}
			}
			blocks[event.Index] = block
		}
	}

	end := t.config.Now()
	duration := end.Sub(start).Seconds()
	if duration < 0 {
		duration = 0
	}
	ordered := make([]core.ContentBlock, 0, len(order))
	for _, index := range order {
		ordered = append(ordered, blocks[index])
	}
	return makeOutputFromSearchResponse(ordered, input.Query, duration), nil
}

// MapToolResultToToolResultBlockParam formats output for a following model turn.
func MapToolResultToToolResultBlockParam(output Output, toolUseID string) ToolResultBlock {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Web search results for query: %q\n\n", output.Query)
	for _, result := range output.Results {
		if result.Search == nil {
			if result.Text != "" {
				builder.WriteString(result.Text)
				builder.WriteString("\n\n")
			}
			continue
		}
		if len(result.Search.Content) == 0 {
			builder.WriteString("No links found.\n\n")
			continue
		}
		data, _ := json.Marshal(result.Search.Content)
		builder.WriteString("Links: ")
		builder.Write(data)
		builder.WriteString("\n\n")
	}
	builder.WriteString("\nREMINDER: You MUST include the sources above in your response to the user using markdown hyperlinks.")
	return ToolResultBlock{ToolUseID: toolUseID, Type: "tool_result", Content: strings.TrimSpace(builder.String())}
}

// ToolResultBlock is the normalized tool-result payload returned to a query loop.
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
}

func makeOutputFromSearchResponse(blocks []core.ContentBlock, query string, durationSeconds float64) Output {
	results := make([]Result, 0)
	textAcc := ""
	inText := true
	for _, block := range blocks {
		switch block.Type {
		case core.ContentBlockServerToolUse:
			if inText && strings.TrimSpace(textAcc) != "" {
				results = append(results, Result{Text: strings.TrimSpace(textAcc)})
			}
			textAcc = ""
			inText = false
		case core.ContentBlockWebSearchToolResult:
			if block.ErrorCode != "" {
				results = append(results, Result{Text: "Web search error: " + block.ErrorCode})
				continue
			}
			hits := make([]SearchHit, 0, len(block.Content))
			for _, hit := range block.Content {
				if hit.Type == core.ContentBlockWebSearchResult {
					hits = append(hits, SearchHit{Title: hit.Title, URL: hit.URL})
				}
			}
			results = append(results, Result{Search: &SearchResult{ToolUseID: block.ToolUseID, Content: hits}})
		case core.ContentBlockText:
			if inText {
				textAcc += block.Text
			} else {
				inText = true
				textAcc = block.Text
			}
		}
	}
	if strings.TrimSpace(textAcc) != "" {
		results = append(results, Result{Text: strings.TrimSpace(textAcc)})
	}
	return Output{Query: query, Results: results, DurationSeconds: durationSeconds}
}

var queryPattern = regexp.MustCompile(`"query"\s*:\s*"(([^"\\]|\\.)*)"`)

func extractQuery(partial string) (string, bool) {
	match := queryPattern.FindStringSubmatch(partial)
	if len(match) < 2 || match[1] == "" {
		return "", false
	}
	var query string
	if err := json.Unmarshal([]byte("\""+match[1]+"\""), &query); err != nil {
		return "", false
	}
	return query, query != ""
}

func emitProgress(callback ProgressFunc, event ProgressEvent) {
	if callback != nil {
		callback(event)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
