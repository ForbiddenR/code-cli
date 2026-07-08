package oauthclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"code-cli/internal/core"
	"code-cli/internal/oauthconfig"
)

// Client calls Claude Code OAuth token and profile endpoints.
type Client struct {
	oauthConfig    oauthconfig.Config
	httpClient     *http.Client
	timeout        time.Duration
	profileTimeout time.Duration
	now            func() time.Time
}

// NewClient creates an OAuth client.
func NewClient(config Config) (*Client, error) {
	oauthConfig := config.OAuthConfig
	if oauthConfig == (oauthconfig.Config{}) {
		resolved, err := oauthconfig.ConfigFromEnv()
		if err != nil {
			return nil, err
		}
		oauthConfig = resolved
	}
	if err := validateOAuthConfig(oauthConfig); err != nil {
		return nil, err
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	profileTimeout := config.ProfileTimeout
	if profileTimeout <= 0 {
		profileTimeout = DefaultProfileTimeout
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &Client{
		oauthConfig:    oauthConfig,
		httpClient:     httpClient,
		timeout:        timeout,
		profileTimeout: profileTimeout,
		now:            now,
	}, nil
}

// BuildAuthURL builds the OAuth authorization URL used by automatic and manual login flows.
func BuildAuthURL(config oauthconfig.Config, opts BuildAuthURLOptions) (string, error) {
	base := config.ConsoleAuthorizeURL
	if opts.LoginWithClaudeAI {
		base = config.ClaudeAIAuthorizeURL
	}
	if base == "" {
		return "", fmt.Errorf("authorize url is required")
	}
	authURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse authorize url: %w", err)
	}
	if authURL.Scheme == "" || authURL.Host == "" {
		return "", fmt.Errorf("parse authorize url: missing scheme or host")
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", opts.Port)
	if opts.IsManual {
		redirectURI = config.ManualRedirectURL
	}
	scopes := oauthconfig.AllOAuthScopes
	if opts.InferenceOnly {
		scopes = []string{oauthconfig.ClaudeAIInferenceScope}
	}

	query := authURL.Query()
	query.Add("code", "true")
	query.Add("client_id", config.ClientID)
	query.Add("response_type", "code")
	query.Add("redirect_uri", redirectURI)
	query.Add("scope", strings.Join(scopes, " "))
	query.Add("code_challenge", opts.CodeChallenge)
	query.Add("code_challenge_method", "S256")
	query.Add("state", opts.State)
	if opts.OrgUUID != "" {
		query.Add("orgUUID", opts.OrgUUID)
	}
	if opts.LoginHint != "" {
		query.Add("login_hint", opts.LoginHint)
	}
	if opts.LoginMethod != "" {
		query.Add("login_method", opts.LoginMethod)
	}
	authURL.RawQuery = query.Encode()
	return authURL.String(), nil
}

// ExchangeCodeForTokens exchanges an OAuth authorization code for access and refresh tokens.
func (c *Client) ExchangeCodeForTokens(ctx context.Context, authorizationCode string, state string, codeVerifier string, port int, useManualRedirect bool, opts ExchangeCodeOptions) (OAuthTokenExchangeResponse, error) {
	if strings.TrimSpace(authorizationCode) == "" {
		return OAuthTokenExchangeResponse{}, fmt.Errorf("authorization code is required")
	}
	if strings.TrimSpace(state) == "" {
		return OAuthTokenExchangeResponse{}, fmt.Errorf("state is required")
	}
	if strings.TrimSpace(codeVerifier) == "" {
		return OAuthTokenExchangeResponse{}, fmt.Errorf("code verifier is required")
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)
	if useManualRedirect {
		redirectURI = c.oauthConfig.ManualRedirectURL
	}
	body := map[string]any{
		"grant_type":    "authorization_code",
		"code":          authorizationCode,
		"redirect_uri":  redirectURI,
		"client_id":     c.oauthConfig.ClientID,
		"code_verifier": codeVerifier,
		"state":         state,
	}
	if opts.ExpiresIn != nil {
		body["expires_in"] = *opts.ExpiresIn
	}

	var response OAuthTokenExchangeResponse
	if err := c.doJSON(ctx, c.timeout, http.MethodPost, c.oauthConfig.TokenURL, nil, body, &response); err != nil {
		var apiErr *core.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			apiErr.Message = "authentication failed: invalid authorization code"
		}
		return OAuthTokenExchangeResponse{}, err
	}
	return response, nil
}

// RefreshOAuthToken refreshes an OAuth token and returns Claude Code's normalized token shape.
func (c *Client) RefreshOAuthToken(ctx context.Context, refreshToken string, opts RefreshOptions) (OAuthTokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return OAuthTokens{}, fmt.Errorf("refresh token is required")
	}
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = oauthconfig.ClaudeAIOAuthScopes
	}
	body := map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     c.oauthConfig.ClientID,
		"scope":         strings.Join(scopes, " "),
	}

	var response OAuthTokenExchangeResponse
	if err := c.doJSON(ctx, c.timeout, http.MethodPost, c.oauthConfig.TokenURL, nil, body, &response); err != nil {
		return OAuthTokens{}, err
	}
	newRefreshToken := response.RefreshToken
	if newRefreshToken == "" {
		newRefreshToken = refreshToken
	}

	profileInfo, err := c.FetchProfileInfo(ctx, response.AccessToken)
	if err != nil {
		return OAuthTokens{}, err
	}
	return c.formatTokens(response, newRefreshToken, profileInfo), nil
}

// FetchOAuthProfile fetches the raw profile using a Claude.ai OAuth access token.
func (c *Client) FetchOAuthProfile(ctx context.Context, accessToken string) (*OAuthProfileResponse, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("access token is required")
	}
	endpoint, err := c.baseEndpoint("/api/oauth/profile", nil)
	if err != nil {
		return nil, err
	}
	var response OAuthProfileResponse
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)
	headers.Set("Content-Type", "application/json")
	if err := c.doJSON(ctx, c.profileTimeout, http.MethodGet, endpoint, headers, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// FetchProfileInfo fetches and normalizes OAuth profile information.
func (c *Client) FetchProfileInfo(ctx context.Context, accessToken string) (ProfileInfo, error) {
	profile, err := c.FetchOAuthProfile(ctx, accessToken)
	if err != nil {
		return ProfileInfo{}, err
	}
	return ProfileInfoFromProfile(profile), nil
}

// FetchOAuthProfileFromAPIKey fetches profile information using an API key and account UUID.
func (c *Client) FetchOAuthProfileFromAPIKey(ctx context.Context, apiKey string, accountUUID string) (*OAuthProfileResponse, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("api key is required")
	}
	if strings.TrimSpace(accountUUID) == "" {
		return nil, fmt.Errorf("account uuid is required")
	}
	query := url.Values{"account_uuid": []string{accountUUID}}
	endpoint, err := c.baseEndpoint("/api/claude_cli_profile", query)
	if err != nil {
		return nil, err
	}
	var response OAuthProfileResponse
	headers := http.Header{}
	headers.Set("x-api-key", apiKey)
	headers.Set("anthropic-beta", oauthconfig.OAuthBetaHeader)
	if err := c.doJSON(ctx, c.profileTimeout, http.MethodGet, endpoint, headers, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// FetchUserRoles fetches OAuth organization and workspace role information.
func (c *Client) FetchUserRoles(ctx context.Context, accessToken string) (UserRolesResponse, error) {
	if strings.TrimSpace(accessToken) == "" {
		return UserRolesResponse{}, fmt.Errorf("access token is required")
	}
	var response UserRolesResponse
	headers := bearerHeaders(accessToken)
	if err := c.doJSON(ctx, c.timeout, http.MethodGet, c.oauthConfig.RolesURL, headers, nil, &response); err != nil {
		return UserRolesResponse{}, err
	}
	return response, nil
}

// CreateAPIKey creates an API key for the authenticated OAuth account.
func (c *Client) CreateAPIKey(ctx context.Context, accessToken string) (string, bool, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", false, fmt.Errorf("access token is required")
	}
	var response apiKeyResponse
	headers := bearerHeaders(accessToken)
	if err := c.doJSON(ctx, c.timeout, http.MethodPost, c.oauthConfig.APIKeyURL, headers, nil, &response); err != nil {
		return "", false, err
	}
	if response.RawKey == "" {
		return "", false, nil
	}
	return response.RawKey, true, nil
}

func (c *Client) formatTokens(response OAuthTokenExchangeResponse, refreshToken string, profileInfo ProfileInfo) OAuthTokens {
	var tokenAccount *TokenAccount
	if response.Account != nil {
		tokenAccount = &TokenAccount{
			UUID:             response.Account.UUID,
			EmailAddress:     response.Account.EmailAddress,
			OrganizationUUID: "",
		}
		if response.Organization != nil {
			tokenAccount.OrganizationUUID = response.Organization.UUID
		}
	}
	return OAuthTokens{
		AccessToken:      response.AccessToken,
		RefreshToken:     refreshToken,
		ExpiresAt:        c.now().Add(time.Duration(response.ExpiresIn) * time.Second),
		Scopes:           ParseScopes(response.Scope),
		SubscriptionType: profileInfo.SubscriptionType,
		RateLimitTier:    profileInfo.RateLimitTier,
		Profile:          profileInfo.RawProfile,
		TokenAccount:     tokenAccount,
	}
}

// ParseScopes splits an OAuth scope string and removes empty entries.
func ParseScopes(scopeString string) []string {
	if scopeString == "" {
		return nil
	}
	fields := strings.Fields(scopeString)
	return append([]string(nil), fields...)
}

// ShouldUseClaudeAIAuth reports whether scopes include the Claude.ai inference scope.
func ShouldUseClaudeAIAuth(scopes []string) bool {
	return slices.Contains(scopes, oauthconfig.ClaudeAIInferenceScope)
}

// IsOAuthTokenExpired reports whether the token expires within the five-minute refresh buffer.
func IsOAuthTokenExpired(expiresAt *time.Time, now time.Time) bool {
	if expiresAt == nil {
		return false
	}
	return !now.Add(5 * time.Minute).Before(*expiresAt)
}

// ProfileInfoFromProfile normalizes OAuth profile information for storage and gating logic.
func ProfileInfoFromProfile(profile *OAuthProfileResponse) ProfileInfo {
	if profile == nil {
		return ProfileInfo{}
	}
	var subscriptionType *SubscriptionType
	switch profile.Organization.OrganizationType {
	case "claude_max":
		value := SubscriptionMax
		subscriptionType = &value
	case "claude_pro":
		value := SubscriptionPro
		subscriptionType = &value
	case "claude_enterprise":
		value := SubscriptionEnterprise
		subscriptionType = &value
	case "claude_team":
		value := SubscriptionTeam
		subscriptionType = &value
	}
	return ProfileInfo{
		SubscriptionType:      subscriptionType,
		DisplayName:           profile.Account.DisplayName,
		RateLimitTier:         profile.Organization.RateLimitTier,
		HasExtraUsageEnabled:  profile.Organization.HasExtraUsageEnabled,
		BillingType:           profile.Organization.BillingType,
		AccountCreatedAt:      profile.Account.CreatedAt,
		SubscriptionCreatedAt: profile.Organization.SubscriptionCreatedAt,
		RawProfile:            profile,
	}
}

func (c *Client) baseEndpoint(path string, query url.Values) (string, error) {
	baseURL, err := url.Parse(c.oauthConfig.BaseAPIURL)
	if err != nil {
		return "", fmt.Errorf("parse base api url: %w", err)
	}
	basePath := strings.TrimRight(baseURL.Path, "/")
	path = "/" + strings.TrimLeft(path, "/")
	baseURL.Path = basePath + path
	baseURL.RawQuery = query.Encode()
	return baseURL.String(), nil
}

func (c *Client) doJSON(ctx context.Context, timeout time.Duration, method string, endpoint string, headers http.Header, body any, out any) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	for name, values := range headers {
		for _, value := range values {
			if value != "" {
				request.Header.Add(name, value)
			}
		}
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return classifyTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	if out == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func validateOAuthConfig(config oauthconfig.Config) error {
	for label, rawURL := range map[string]string{
		"token url":           config.TokenURL,
		"base api url":        config.BaseAPIURL,
		"api key url":         config.APIKeyURL,
		"roles url":           config.RolesURL,
		"manual redirect url": config.ManualRedirectURL,
	} {
		if strings.TrimSpace(rawURL) == "" {
			return fmt.Errorf("%s is required", label)
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("parse %s: %w", label, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("parse %s: missing scheme or host", label)
		}
	}
	if strings.TrimSpace(config.ClientID) == "" {
		return fmt.Errorf("client id is required")
	}
	return nil
}

func bearerHeaders(accessToken string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)
	return headers
}

func classifyTransportError(err error) *core.APIError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &core.APIError{Kind: core.APIErrorAbort, Message: err.Error(), Retryable: false, Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.APIError{Kind: core.APIErrorTimeout, Message: err.Error(), Retryable: true, Cause: err}
	}
	if netErr, ok := errors.AsType[net.Error](err); ok {
		kind := core.APIErrorNetwork
		if netErr.Timeout() {
			kind = core.APIErrorTimeout
		}
		return &core.APIError{Kind: kind, Message: err.Error(), Retryable: true, Cause: err}
	}
	return &core.APIError{Kind: core.APIErrorNetwork, Message: err.Error(), Retryable: true, Cause: err}
}

func responseError(response *http.Response) *core.APIError {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	message := strings.TrimSpace(string(body))
	if extracted := extractErrorMessage(body); extracted != "" {
		message = extracted
	}
	if message == "" {
		message = response.Status
	}
	return &core.APIError{
		Kind:       errorKindForStatus(response.StatusCode),
		StatusCode: response.StatusCode,
		Message:    message,
		RequestID:  responseRequestID(response),
		Retryable:  retryableStatus(response.StatusCode),
	}
}

func extractErrorMessage(body []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	if envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	return envelope.Message
}

func errorKindForStatus(status int) core.APIErrorKind {
	switch status {
	case http.StatusUnauthorized:
		return core.APIErrorAuth
	case http.StatusForbidden:
		return core.APIErrorPermission
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return core.APIErrorTimeout
	case http.StatusRequestEntityTooLarge:
		return core.APIErrorContextLength
	case http.StatusTooManyRequests:
		return core.APIErrorRateLimit
	case 529:
		return core.APIErrorOverloaded
	}
	if status >= 500 {
		return core.APIErrorServer
	}
	if status >= 400 {
		return core.APIErrorInvalidRequest
	}
	return core.APIErrorUnknown
}

func retryableStatus(status int) bool {
	switch errorKindForStatus(status) {
	case core.APIErrorRateLimit, core.APIErrorOverloaded, core.APIErrorTimeout, core.APIErrorServer:
		return true
	default:
		return false
	}
}

func responseRequestID(response *http.Response) string {
	if response == nil {
		return ""
	}
	if requestID := response.Header.Get("request-id"); requestID != "" {
		return requestID
	}
	return response.Header.Get("x-request-id")
}
