package core

// ModelID is a Claude model identifier accepted by the API boundary.
type ModelID string

const (
	ModelClaudeFable5  ModelID = "claude-fable-5"
	ModelClaudeOpus48  ModelID = "claude-opus-4-8"
	ModelClaudeOpus47  ModelID = "claude-opus-4-7"
	ModelClaudeOpus46  ModelID = "claude-opus-4-6"
	ModelClaudeSonnet5 ModelID = "claude-sonnet-5"
	ModelClaudeHaiku45 ModelID = "claude-haiku-4-5"

	// DefaultModel is the model used when a request does not choose one.
	DefaultModel ModelID = ModelClaudeOpus48
)

func (m ModelID) String() string {
	return string(m)
}
