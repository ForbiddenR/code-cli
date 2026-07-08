package filesapi

import (
	"net/http"
	"time"
)

const (
	// DefaultBaseURL is the public Anthropic API base URL used by the Files API.
	DefaultBaseURL = "https://api.anthropic.com"
	// FilesAPIBetaHeader enables the beta Files API and OAuth on public API routes.
	FilesAPIBetaHeader = "files-api-2025-04-14,oauth-2025-04-20"
	// AnthropicVersion is the API version header used by the TypeScript Files API client.
	AnthropicVersion = "2023-06-01"

	DefaultMaxRetries  = 3
	DefaultBaseDelay   = 500 * time.Millisecond
	DefaultTimeout     = 60 * time.Second
	DefaultConcurrency = 5
	MaxFileSizeBytes   = 500 * 1024 * 1024
)

// Config contains process-level settings for Files API calls.
type Config struct {
	OAuthToken string
	BaseURL    string
	HTTPClient *http.Client
	MaxRetries int
	BaseDelay  time.Duration
	Timeout    time.Duration
	Sleep      func(time.Duration)
}

// File is one file attachment spec parsed from CLI arguments.
type File struct {
	FileID       string
	RelativePath string
}

// FileMetadata is metadata returned by list-files calls.
type FileMetadata struct {
	Filename string
	FileID   string
	Size     int64
}

// DownloadResult describes the outcome of downloading and saving one file.
type DownloadResult struct {
	FileID       string
	Path         string
	Success      bool
	Error        string
	BytesWritten int64
}

// LocalFile describes a local file to upload with its session-relative path.
type LocalFile struct {
	Path         string
	RelativePath string
}
