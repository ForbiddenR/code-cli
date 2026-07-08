package oauthconfig

import (
	"reflect"
	"testing"
)

func TestConfigFromLookupProduction(t *testing.T) {
	config, err := ConfigFromLookup(mapLookup(nil))
	if err != nil {
		t.Fatalf("ConfigFromLookup: %v", err)
	}
	if config.BaseAPIURL != ProductionBaseAPIURL {
		t.Fatalf("BaseAPIURL = %q", config.BaseAPIURL)
	}
	if config.ConsoleAuthorizeURL != "https://platform.claude.com/oauth/authorize" {
		t.Fatalf("ConsoleAuthorizeURL = %q", config.ConsoleAuthorizeURL)
	}
	if config.ClaudeAIAuthorizeURL != "https://claude.com/cai/oauth/authorize" {
		t.Fatalf("ClaudeAIAuthorizeURL = %q", config.ClaudeAIAuthorizeURL)
	}
	if config.ClaudeAIOrigin != "https://claude.ai" {
		t.Fatalf("ClaudeAIOrigin = %q", config.ClaudeAIOrigin)
	}
	if config.TokenURL != "https://platform.claude.com/v1/oauth/token" {
		t.Fatalf("TokenURL = %q", config.TokenURL)
	}
	if config.APIKeyURL != "https://api.anthropic.com/api/oauth/claude_cli/create_api_key" {
		t.Fatalf("APIKeyURL = %q", config.APIKeyURL)
	}
	if config.RolesURL != "https://api.anthropic.com/api/oauth/claude_cli/roles" {
		t.Fatalf("RolesURL = %q", config.RolesURL)
	}
	if config.ClientID != ProductionOAuthClientID {
		t.Fatalf("ClientID = %q", config.ClientID)
	}
	if config.OAuthFileSuffix != "" {
		t.Fatalf("OAuthFileSuffix = %q", config.OAuthFileSuffix)
	}
	if config.MCPProxyURL != "https://mcp-proxy.anthropic.com" || config.MCPProxyPath != "/v1/mcp/{server_id}" {
		t.Fatalf("mcp proxy = %q %q", config.MCPProxyURL, config.MCPProxyPath)
	}
}

func TestConfigFromLookupRequiresAntForStagingAndLocal(t *testing.T) {
	config, err := ConfigFromLookup(mapLookup(map[string]string{EnvUseStagingOAuth: "true", EnvUseLocalOAuth: "true"}))
	if err != nil {
		t.Fatalf("ConfigFromLookup: %v", err)
	}
	if config.BaseAPIURL != ProductionBaseAPIURL {
		t.Fatalf("BaseAPIURL without ant user = %q", config.BaseAPIURL)
	}
}

func TestConfigFromLookupStaging(t *testing.T) {
	config, err := ConfigFromLookup(mapLookup(map[string]string{EnvUserType: "ant", EnvUseStagingOAuth: "yes"}))
	if err != nil {
		t.Fatalf("ConfigFromLookup: %v", err)
	}
	if config.BaseAPIURL != "https://api-staging.anthropic.com" {
		t.Fatalf("BaseAPIURL = %q", config.BaseAPIURL)
	}
	if config.TokenURL != "https://platform.staging.ant.dev/v1/oauth/token" {
		t.Fatalf("TokenURL = %q", config.TokenURL)
	}
	if config.ClientID != NonProductionOAuthClientID {
		t.Fatalf("ClientID = %q", config.ClientID)
	}
	if config.OAuthFileSuffix != "-staging-oauth" {
		t.Fatalf("OAuthFileSuffix = %q", config.OAuthFileSuffix)
	}
	if config.MCPProxyURL != "https://mcp-proxy-staging.anthropic.com" {
		t.Fatalf("MCPProxyURL = %q", config.MCPProxyURL)
	}
}

func TestConfigFromLookupLocal(t *testing.T) {
	config, err := ConfigFromLookup(mapLookup(map[string]string{EnvUserType: "ant", EnvUseLocalOAuth: "1"}))
	if err != nil {
		t.Fatalf("ConfigFromLookup: %v", err)
	}
	if config.BaseAPIURL != "http://localhost:8000" {
		t.Fatalf("BaseAPIURL = %q", config.BaseAPIURL)
	}
	if config.ClaudeAIOrigin != "http://localhost:4000" {
		t.Fatalf("ClaudeAIOrigin = %q", config.ClaudeAIOrigin)
	}
	if config.ConsoleAuthorizeURL != "http://localhost:3000/oauth/authorize" {
		t.Fatalf("ConsoleAuthorizeURL = %q", config.ConsoleAuthorizeURL)
	}
	if config.MCPProxyURL != "http://localhost:8205" || config.MCPProxyPath != "/v1/toolbox/shttp/mcp/{server_id}" {
		t.Fatalf("mcp proxy = %q %q", config.MCPProxyURL, config.MCPProxyPath)
	}
	if config.OAuthFileSuffix != "-local-oauth" {
		t.Fatalf("OAuthFileSuffix = %q", config.OAuthFileSuffix)
	}
}

func TestConfigFromLookupLocalOverridesTrimTrailingSlash(t *testing.T) {
	config, err := ConfigFromLookup(mapLookup(map[string]string{
		EnvUserType:              "ant",
		EnvUseLocalOAuth:         "true",
		EnvLocalOAuthAPIBase:     "http://api.local/",
		EnvLocalOAuthAppsBase:    "http://apps.local/",
		EnvLocalOAuthConsoleBase: "http://console.local/",
	}))
	if err != nil {
		t.Fatalf("ConfigFromLookup: %v", err)
	}
	if config.BaseAPIURL != "http://api.local" {
		t.Fatalf("BaseAPIURL = %q", config.BaseAPIURL)
	}
	if config.ClaudeAIAuthorizeURL != "http://apps.local/oauth/authorize" {
		t.Fatalf("ClaudeAIAuthorizeURL = %q", config.ClaudeAIAuthorizeURL)
	}
	if config.ConsoleSuccessURL != "http://console.local/buy_credits?returnUrl=/oauth/code/success%3Fapp%3Dclaude-code" {
		t.Fatalf("ConsoleSuccessURL = %q", config.ConsoleSuccessURL)
	}
}

func TestConfigFromLookupLocalTakesPrecedenceOverStaging(t *testing.T) {
	config, err := ConfigFromLookup(mapLookup(map[string]string{EnvUserType: "ant", EnvUseLocalOAuth: "true", EnvUseStagingOAuth: "true"}))
	if err != nil {
		t.Fatalf("ConfigFromLookup: %v", err)
	}
	if config.OAuthFileSuffix != "-local-oauth" || config.BaseAPIURL != "http://localhost:8000" {
		t.Fatalf("config = %#v", config)
	}
}

func TestConfigFromLookupCustomOAuthURL(t *testing.T) {
	config, err := ConfigFromLookup(mapLookup(map[string]string{EnvCustomOAuthURL: "https://claude.fedstart.com/"}))
	if err != nil {
		t.Fatalf("ConfigFromLookup: %v", err)
	}
	if config.BaseAPIURL != "https://claude.fedstart.com" {
		t.Fatalf("BaseAPIURL = %q", config.BaseAPIURL)
	}
	if config.ConsoleAuthorizeURL != "https://claude.fedstart.com/oauth/authorize" {
		t.Fatalf("ConsoleAuthorizeURL = %q", config.ConsoleAuthorizeURL)
	}
	if config.TokenURL != "https://claude.fedstart.com/v1/oauth/token" {
		t.Fatalf("TokenURL = %q", config.TokenURL)
	}
	if config.APIKeyURL != "https://claude.fedstart.com/api/oauth/claude_cli/create_api_key" {
		t.Fatalf("APIKeyURL = %q", config.APIKeyURL)
	}
	if config.OAuthFileSuffix != "-custom-oauth" {
		t.Fatalf("OAuthFileSuffix = %q", config.OAuthFileSuffix)
	}
	if config.MCPProxyURL != "https://mcp-proxy.anthropic.com" {
		t.Fatalf("MCPProxyURL should remain from base config, got %q", config.MCPProxyURL)
	}
}

func TestConfigFromLookupRejectsUnapprovedCustomOAuthURL(t *testing.T) {
	_, err := ConfigFromLookup(mapLookup(map[string]string{EnvCustomOAuthURL: "https://evil.example"}))
	if err == nil {
		t.Fatalf("ConfigFromLookup accepted unapproved custom OAuth URL")
	}
}

func TestConfigFromLookupClientIDOverride(t *testing.T) {
	config, err := ConfigFromLookup(mapLookup(map[string]string{EnvOAuthClientID: "custom-client"}))
	if err != nil {
		t.Fatalf("ConfigFromLookup: %v", err)
	}
	if config.ClientID != "custom-client" {
		t.Fatalf("ClientID = %q", config.ClientID)
	}
}

func TestFileSuffixFromLookup(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "prod", want: ""},
		{name: "staging", env: map[string]string{EnvUserType: "ant", EnvUseStagingOAuth: "true"}, want: "-staging-oauth"},
		{name: "local", env: map[string]string{EnvUserType: "ant", EnvUseLocalOAuth: "true"}, want: "-local-oauth"},
		{name: "custom", env: map[string]string{EnvCustomOAuthURL: "https://claude.fedstart.com"}, want: "-custom-oauth"},
		{name: "custom wins over local", env: map[string]string{EnvUserType: "ant", EnvUseLocalOAuth: "true", EnvCustomOAuthURL: "https://claude.fedstart.com"}, want: "-custom-oauth"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FileSuffixFromLookup(mapLookup(test.env)); got != test.want {
				t.Fatalf("FileSuffixFromLookup = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOAuthScopes(t *testing.T) {
	if !reflect.DeepEqual(ConsoleOAuthScopes, []string{ConsoleScope, ClaudeAIProfileScope}) {
		t.Fatalf("ConsoleOAuthScopes = %#v", ConsoleOAuthScopes)
	}
	wantAll := []string{ConsoleScope, ClaudeAIProfileScope, ClaudeAIInferenceScope, "user:sessions:claude_code", "user:mcp_servers", "user:file_upload"}
	if !reflect.DeepEqual(AllOAuthScopes, wantAll) {
		t.Fatalf("AllOAuthScopes = %#v", AllOAuthScopes)
	}
}

func mapLookup(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
