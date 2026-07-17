package sessionsapi

import (
	"encoding/json"
	"net/http"
	"time"
)

const (
	// DefaultBaseURL is the production OAuth BASE_API_URL used by Sessions API endpoints.
	DefaultBaseURL = "https://api.anthropic.com"
	// AnthropicVersion is the API version sent by the TypeScript OAuth helpers.
	AnthropicVersion = "2023-06-01"
	// CCRBYOCBeta is the beta header required by the CCR BYOC Sessions API.
	CCRBYOCBeta = "ccr-byoc-2025-07-29"
	// DefaultTimeout matches the single-session fetch timeout in teleport/api.ts.
	DefaultTimeout = 15 * time.Second
	// DefaultSendEventTimeout matches the remote event send timeout in teleport/api.ts.
	DefaultSendEventTimeout = 30 * time.Second
	// DefaultPollEventsTimeout matches the pollRemoteSessionEvents request timeout in teleport.tsx.
	DefaultPollEventsTimeout = 30 * time.Second
	// DefaultArchiveTimeout matches the archiveRemoteSession request timeout in teleport.tsx.
	DefaultArchiveTimeout = 10 * time.Second
	// MaxEventPages is the safety valve against stuck cursors while paging session events.
	MaxEventPages = 50
)

// DefaultRetryDelays matches axiosGetWithRetry in teleport/api.ts.
var DefaultRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// Config contains process-level settings for Sessions API calls.
type Config struct {
	BaseURL          string
	AccessToken      string
	OrgUUID          string
	HTTPClient       *http.Client
	Timeout          time.Duration
	SendEventTimeout time.Duration
	RetryDelays      []time.Duration
	Sleep            func(time.Duration)
}

// SessionStatus is the raw Sessions API session_status value.
type SessionStatus string

const (
	SessionStatusRequiresAction SessionStatus = "requires_action"
	SessionStatusRunning        SessionStatus = "running"
	SessionStatusIdle           SessionStatus = "idle"
	SessionStatusArchived       SessionStatus = "archived"
)

// SessionContextSource is one source inside a session context.
type SessionContextSource struct {
	Type                     string  `json:"type"`
	URL                      string  `json:"url,omitempty"`
	Revision                 *string `json:"revision,omitempty"`
	AllowUnrestrictedGitPush *bool   `json:"allow_unrestricted_git_push,omitempty"`
	KnowledgeBaseID          string  `json:"knowledge_base_id,omitempty"`
}

// OutcomeGitInfo is the git outcome information returned by the API.
type OutcomeGitInfo struct {
	Type     string   `json:"type"`
	Repo     string   `json:"repo"`
	Branches []string `json:"branches"`
}

// Outcome is one session outcome.
type Outcome struct {
	Type    string         `json:"type"`
	GitInfo OutcomeGitInfo `json:"git_info"`
}

// GitHubPR identifies an associated GitHub pull request.
type GitHubPR struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// SessionContext is the runtime context stored on a session resource.
type SessionContext struct {
	Sources              []SessionContextSource `json:"sources"`
	CWD                  string                 `json:"cwd"`
	Outcomes             []Outcome              `json:"outcomes"`
	CustomSystemPrompt   *string                `json:"custom_system_prompt"`
	AppendSystemPrompt   *string                `json:"append_system_prompt"`
	Model                *string                `json:"model"`
	SeedBundleFileID     string                 `json:"seed_bundle_file_id,omitempty"`
	GitHubPR             *GitHubPR              `json:"github_pr,omitempty"`
	ReuseOutcomeBranches bool                   `json:"reuse_outcome_branches,omitempty"`
}

// SessionResource is one raw Sessions API resource.
type SessionResource struct {
	Type           string         `json:"type"`
	ID             string         `json:"id"`
	Title          *string        `json:"title"`
	SessionStatus  SessionStatus  `json:"session_status"`
	EnvironmentID  string         `json:"environment_id"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
	SessionContext SessionContext `json:"session_context"`
}

// ListSessionsResponse is the raw response from GET /v1/sessions.
type ListSessionsResponse struct {
	Data    []SessionResource `json:"data"`
	HasMore bool              `json:"has_more"`
	FirstID *string           `json:"first_id"`
	LastID  *string           `json:"last_id"`
}

// CodeSession is the transformed shape used by teleport UI flows.
type CodeSession struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      SessionStatus `json:"status"`
	Repo        *Repo         `json:"repo"`
	Turns       []string      `json:"turns"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
}

// Repo is the GitHub repository summary embedded in a CodeSession.
type Repo struct {
	Name          string    `json:"name"`
	Owner         RepoOwner `json:"owner"`
	DefaultBranch string    `json:"default_branch,omitempty"`
}

// RepoOwner is the GitHub repository owner summary embedded in a CodeSession.
type RepoOwner struct {
	Login string `json:"login"`
}

// RemoteMessageContent is the message content accepted by the remote-session event endpoint.
type RemoteMessageContent any

// SendEventOptions contains optional event-send fields.
type SendEventOptions struct {
	UUID string
}

type sendEventsRequest struct {
	Events []remoteSessionEvent `json:"events"`
}

type remoteSessionEvent struct {
	UUID            string        `json:"uuid"`
	SessionID       string        `json:"session_id"`
	Type            string        `json:"type"`
	ParentToolUseID *string       `json:"parent_tool_use_id"`
	Message         remoteMessage `json:"message"`
}

type remoteMessage struct {
	Role    string               `json:"role"`
	Content RemoteMessageContent `json:"content"`
}

type updateSessionTitleRequest struct {
	Title string `json:"title"`
}

// PollEventsOptions configures PollRemoteSessionEvents.
type PollEventsOptions struct {
	// AfterID is the previous response's last event id. Empty fetches from the start.
	AfterID string
	// SkipMetadata avoids the per-call GET /v1/sessions/{id} when branch/status aren't needed.
	SkipMetadata bool
	// MaxPages overrides MaxEventPages when positive.
	MaxPages int
}

// PollEventsResult mirrors PollRemoteSessionResponse from teleport.tsx.
type PollEventsResult struct {
	NewEvents     []json.RawMessage
	LastEventID   *string
	Branch        *string
	SessionStatus *SessionStatus
}

// ListSessionEventsResponse is one page from GET /v1/sessions/{id}/events.
type ListSessionEventsResponse struct {
	Data    []json.RawMessage `json:"data"`
	HasMore bool              `json:"has_more"`
	FirstID *string           `json:"first_id"`
	LastID  *string           `json:"last_id"`
}
