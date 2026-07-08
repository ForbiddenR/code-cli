package oauthconfig

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

const (
	EnvUserType                = "USER_TYPE"
	EnvUseLocalOAuth           = "USE_LOCAL_OAUTH"
	EnvUseStagingOAuth         = "USE_STAGING_OAUTH"
	EnvCustomOAuthURL          = "CLAUDE_CODE_CUSTOM_OAUTH_URL"
	EnvOAuthClientID           = "CLAUDE_CODE_OAUTH_CLIENT_ID"
	EnvLocalOAuthAPIBase       = "CLAUDE_LOCAL_OAUTH_API_BASE"
	EnvLocalOAuthAppsBase      = "CLAUDE_LOCAL_OAUTH_APPS_BASE"
	EnvLocalOAuthConsoleBase   = "CLAUDE_LOCAL_OAUTH_CONSOLE_BASE"
	ClaudeAIInferenceScope     = "user:inference"
	ClaudeAIProfileScope       = "user:profile"
	ConsoleScope               = "org:create_api_key"
	OAuthBetaHeader            = "oauth-2025-04-20"
	MCPClientMetadataURL       = "https://claude.ai/oauth/claude-code-client-metadata"
	ProductionBaseAPIURL       = "https://api.anthropic.com"
	ProductionOAuthClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	NonProductionOAuthClientID = "22422756-60c9-4084-8eb7-27705fd5cf9a"
)

var (
	ConsoleOAuthScopes   = []string{ConsoleScope, ClaudeAIProfileScope}
	ClaudeAIOAuthScopes  = []string{ClaudeAIProfileScope, ClaudeAIInferenceScope, "user:sessions:claude_code", "user:mcp_servers", "user:file_upload"}
	AllOAuthScopes       = uniqueScopes(append(append([]string{}, ConsoleOAuthScopes...), ClaudeAIOAuthScopes...))
	AllowedOAuthBaseURLs = []string{
		"https://beacon.claude-ai.staging.ant.dev",
		"https://claude.fedstart.com",
		"https://claude-staging.fedstart.com",
	}
)

type ConfigType string

const (
	ConfigTypeProd    ConfigType = "prod"
	ConfigTypeStaging ConfigType = "staging"
	ConfigTypeLocal   ConfigType = "local"
)

// Config mirrors the OAuth URL configuration used by Claude Code.
type Config struct {
	BaseAPIURL           string
	ConsoleAuthorizeURL  string
	ClaudeAIAuthorizeURL string
	ClaudeAIOrigin       string
	TokenURL             string
	APIKeyURL            string
	RolesURL             string
	ConsoleSuccessURL    string
	ClaudeAISuccessURL   string
	ManualRedirectURL    string
	ClientID             string
	OAuthFileSuffix      string
	MCPProxyURL          string
	MCPProxyPath         string
}

// ConfigFromEnv returns the OAuth configuration selected by the current environment.
func ConfigFromEnv() (Config, error) {
	return ConfigFromLookup(os.Getenv)
}

// ConfigFromLookup returns the OAuth configuration selected by a getenv-compatible lookup.
func ConfigFromLookup(getenv func(string) string) (Config, error) {
	config := baseConfig(configTypeFromLookup(getenv), getenv)
	if oauthBaseURL := getenv(EnvCustomOAuthURL); oauthBaseURL != "" {
		base := strings.TrimRight(oauthBaseURL, "/")
		if !isAllowedOAuthBaseURL(base) {
			return Config{}, fmt.Errorf("CLAUDE_CODE_CUSTOM_OAUTH_URL is not an approved endpoint")
		}
		config.BaseAPIURL = base
		config.ConsoleAuthorizeURL = base + "/oauth/authorize"
		config.ClaudeAIAuthorizeURL = base + "/oauth/authorize"
		config.ClaudeAIOrigin = base
		config.TokenURL = base + "/v1/oauth/token"
		config.APIKeyURL = base + "/api/oauth/claude_cli/create_api_key"
		config.RolesURL = base + "/api/oauth/claude_cli/roles"
		config.ConsoleSuccessURL = base + "/oauth/code/success?app=claude-code"
		config.ClaudeAISuccessURL = base + "/oauth/code/success?app=claude-code"
		config.ManualRedirectURL = base + "/oauth/code/callback"
		config.OAuthFileSuffix = "-custom-oauth"
	}
	if clientID := getenv(EnvOAuthClientID); clientID != "" {
		config.ClientID = clientID
	}
	return config, nil
}

// FileSuffixFromEnv returns the OAuth credential file suffix selected by the current environment.
func FileSuffixFromEnv() string {
	return FileSuffixFromLookup(os.Getenv)
}

// FileSuffixFromLookup returns the OAuth credential file suffix selected by a getenv-compatible lookup.
func FileSuffixFromLookup(getenv func(string) string) string {
	if getenv(EnvCustomOAuthURL) != "" {
		return "-custom-oauth"
	}
	switch configTypeFromLookup(getenv) {
	case ConfigTypeLocal:
		return "-local-oauth"
	case ConfigTypeStaging:
		return "-staging-oauth"
	default:
		return ""
	}
}

func configTypeFromLookup(getenv func(string) string) ConfigType {
	if getenv(EnvUserType) == "ant" {
		if isEnvTruthy(getenv(EnvUseLocalOAuth)) {
			return ConfigTypeLocal
		}
		if isEnvTruthy(getenv(EnvUseStagingOAuth)) {
			return ConfigTypeStaging
		}
	}
	return ConfigTypeProd
}

func baseConfig(configType ConfigType, getenv func(string) string) Config {
	switch configType {
	case ConfigTypeLocal:
		return localConfig(getenv)
	case ConfigTypeStaging:
		return stagingConfig()
	default:
		return productionConfig()
	}
}

func productionConfig() Config {
	return Config{
		BaseAPIURL:           ProductionBaseAPIURL,
		ConsoleAuthorizeURL:  "https://platform.claude.com/oauth/authorize",
		ClaudeAIAuthorizeURL: "https://claude.com/cai/oauth/authorize",
		ClaudeAIOrigin:       "https://claude.ai",
		TokenURL:             "https://platform.claude.com/v1/oauth/token",
		APIKeyURL:            "https://api.anthropic.com/api/oauth/claude_cli/create_api_key",
		RolesURL:             "https://api.anthropic.com/api/oauth/claude_cli/roles",
		ConsoleSuccessURL:    "https://platform.claude.com/buy_credits?returnUrl=/oauth/code/success%3Fapp%3Dclaude-code",
		ClaudeAISuccessURL:   "https://platform.claude.com/oauth/code/success?app=claude-code",
		ManualRedirectURL:    "https://platform.claude.com/oauth/code/callback",
		ClientID:             ProductionOAuthClientID,
		OAuthFileSuffix:      "",
		MCPProxyURL:          "https://mcp-proxy.anthropic.com",
		MCPProxyPath:         "/v1/mcp/{server_id}",
	}
}

func stagingConfig() Config {
	return Config{
		BaseAPIURL:           "https://api-staging.anthropic.com",
		ConsoleAuthorizeURL:  "https://platform.staging.ant.dev/oauth/authorize",
		ClaudeAIAuthorizeURL: "https://claude-ai.staging.ant.dev/oauth/authorize",
		ClaudeAIOrigin:       "https://claude-ai.staging.ant.dev",
		TokenURL:             "https://platform.staging.ant.dev/v1/oauth/token",
		APIKeyURL:            "https://api-staging.anthropic.com/api/oauth/claude_cli/create_api_key",
		RolesURL:             "https://api-staging.anthropic.com/api/oauth/claude_cli/roles",
		ConsoleSuccessURL:    "https://platform.staging.ant.dev/buy_credits?returnUrl=/oauth/code/success%3Fapp%3Dclaude-code",
		ClaudeAISuccessURL:   "https://platform.staging.ant.dev/oauth/code/success?app=claude-code",
		ManualRedirectURL:    "https://platform.staging.ant.dev/oauth/code/callback",
		ClientID:             NonProductionOAuthClientID,
		OAuthFileSuffix:      "-staging-oauth",
		MCPProxyURL:          "https://mcp-proxy-staging.anthropic.com",
		MCPProxyPath:         "/v1/mcp/{server_id}",
	}
}

func localConfig(getenv func(string) string) Config {
	api := trimTrailingSlashOrDefault(getenv(EnvLocalOAuthAPIBase), "http://localhost:8000")
	apps := trimTrailingSlashOrDefault(getenv(EnvLocalOAuthAppsBase), "http://localhost:4000")
	consoleBase := trimTrailingSlashOrDefault(getenv(EnvLocalOAuthConsoleBase), "http://localhost:3000")
	return Config{
		BaseAPIURL:           api,
		ConsoleAuthorizeURL:  consoleBase + "/oauth/authorize",
		ClaudeAIAuthorizeURL: apps + "/oauth/authorize",
		ClaudeAIOrigin:       apps,
		TokenURL:             api + "/v1/oauth/token",
		APIKeyURL:            api + "/api/oauth/claude_cli/create_api_key",
		RolesURL:             api + "/api/oauth/claude_cli/roles",
		ConsoleSuccessURL:    consoleBase + "/buy_credits?returnUrl=/oauth/code/success%3Fapp%3Dclaude-code",
		ClaudeAISuccessURL:   consoleBase + "/oauth/code/success?app=claude-code",
		ManualRedirectURL:    consoleBase + "/oauth/code/callback",
		ClientID:             NonProductionOAuthClientID,
		OAuthFileSuffix:      "-local-oauth",
		MCPProxyURL:          "http://localhost:8205",
		MCPProxyPath:         "/v1/toolbox/shttp/mcp/{server_id}",
	}
}

func trimTrailingSlashOrDefault(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return strings.TrimRight(value, "/")
}

func isAllowedOAuthBaseURL(baseURL string) bool {
	return slices.Contains(AllowedOAuthBaseURLs, baseURL)
}

func isEnvTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func uniqueScopes(scopes []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if seen[scope] {
			continue
		}
		seen[scope] = true
		unique = append(unique, scope)
	}
	return unique
}
