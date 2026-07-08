package oauthclient

import (
	"net/http"
	"time"

	"code-cli/internal/oauthconfig"
)

const (
	// DefaultTimeout matches the OAuth token exchange timeout in services/oauth/client.ts.
	DefaultTimeout = 15 * time.Second
	// DefaultProfileTimeout matches getOauthProfile.ts profile request timeouts.
	DefaultProfileTimeout = 10 * time.Second
)

// Config contains process-level settings for OAuth token and profile API calls.
type Config struct {
	OAuthConfig    oauthconfig.Config
	HTTPClient     *http.Client
	Timeout        time.Duration
	ProfileTimeout time.Duration
	Now            func() time.Time
}

// BuildAuthURLOptions contains the query parameters for Claude Code OAuth authorization URLs.
type BuildAuthURLOptions struct {
	CodeChallenge     string
	State             string
	Port              int
	IsManual          bool
	LoginWithClaudeAI bool
	InferenceOnly     bool
	OrgUUID           string
	LoginHint         string
	LoginMethod       string
}

// ExchangeCodeOptions contains optional token-exchange fields.
type ExchangeCodeOptions struct {
	ExpiresIn *int
}

// RefreshOptions contains optional token-refresh fields.
type RefreshOptions struct {
	Scopes []string
}

// OAuthTokenExchangeResponse is the raw token endpoint response.
type OAuthTokenExchangeResponse struct {
	AccessToken  string                `json:"access_token"`
	RefreshToken string                `json:"refresh_token"`
	ExpiresIn    int64                 `json:"expires_in"`
	Scope        string                `json:"scope"`
	Account      *TokenAccountResponse `json:"account,omitempty"`
	Organization *TokenOrgResponse     `json:"organization,omitempty"`
	Extra        map[string]any        `json:"-"`
}

// TokenAccountResponse is the account summary embedded in token responses.
type TokenAccountResponse struct {
	UUID         string `json:"uuid"`
	EmailAddress string `json:"email_address"`
}

// TokenOrgResponse is the organization summary embedded in token responses.
type TokenOrgResponse struct {
	UUID string `json:"uuid"`
}

// OAuthTokens is the normalized token shape used by Claude Code auth flows.
type OAuthTokens struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	Scopes           []string
	SubscriptionType *SubscriptionType
	RateLimitTier    *RateLimitTier
	Profile          *OAuthProfileResponse
	TokenAccount     *TokenAccount
}

// TokenAccount is the normalized account identity from token responses.
type TokenAccount struct {
	UUID             string
	EmailAddress     string
	OrganizationUUID string
}

type SubscriptionType string

const (
	SubscriptionMax        SubscriptionType = "max"
	SubscriptionPro        SubscriptionType = "pro"
	SubscriptionEnterprise SubscriptionType = "enterprise"
	SubscriptionTeam       SubscriptionType = "team"
)

type RateLimitTier string
type BillingType string

// OAuthProfileResponse is the raw response from OAuth profile endpoints.
type OAuthProfileResponse struct {
	Account      ProfileAccount      `json:"account"`
	Organization ProfileOrganization `json:"organization"`
}

type ProfileAccount struct {
	UUID        string `json:"uuid"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

type ProfileOrganization struct {
	UUID                  string         `json:"uuid"`
	OrganizationType      string         `json:"organization_type"`
	RateLimitTier         *RateLimitTier `json:"rate_limit_tier"`
	HasExtraUsageEnabled  *bool          `json:"has_extra_usage_enabled"`
	BillingType           *BillingType   `json:"billing_type"`
	SubscriptionCreatedAt string         `json:"subscription_created_at"`
}

// ProfileInfo is the normalized subset Claude Code stores from the profile response.
type ProfileInfo struct {
	SubscriptionType      *SubscriptionType
	DisplayName           string
	RateLimitTier         *RateLimitTier
	HasExtraUsageEnabled  *bool
	BillingType           *BillingType
	AccountCreatedAt      string
	SubscriptionCreatedAt string
	RawProfile            *OAuthProfileResponse
}

// UserRolesResponse is the raw user roles response.
type UserRolesResponse struct {
	OrganizationRole string `json:"organization_role"`
	WorkspaceRole    string `json:"workspace_role"`
	OrganizationName string `json:"organization_name"`
}

type apiKeyResponse struct {
	RawKey string `json:"raw_key"`
}
