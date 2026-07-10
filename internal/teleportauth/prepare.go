package teleportauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"code-cli/internal/oauthclient"
	"code-cli/internal/oauthconfig"
	"code-cli/internal/oauthstorage"
)

const (
	// EnvOrganizationUUID is the SDK/remote fallback for the authenticated organization UUID.
	EnvOrganizationUUID = "CLAUDE_CODE_ORGANIZATION_UUID"
	// MissingClaudeAIAuthMessage matches the TypeScript web-sessions authentication error.
	MissingClaudeAIAuthMessage = "Claude Code web sessions require authentication with a Claude.ai account. API key authentication is not sufficient. Please run /login to authenticate, or check your authentication status with /status."
	// MissingOrganizationUUIDMessage matches the TypeScript prepareApiRequest organization error.
	MissingOrganizationUUIDMessage = "Unable to get organization UUID"
)

// ErrMissingPreparer is returned when an integration helper has no auth preparer.
var ErrMissingPreparer = errors.New("teleport auth preparer is required")

// TokenGetter returns Claude.ai OAuth tokens from the configured auth storage layer.
type TokenGetter interface {
	GetClaudeAIOAuthTokens() (*oauthstorage.Tokens, error)
}

// ProfileFetcher fetches OAuth profile information for organization UUID fallback.
type ProfileFetcher interface {
	FetchOAuthProfile(ctx context.Context, accessToken string) (*oauthclient.OAuthProfileResponse, error)
}

// AccountInfo is the cached OAuth account subset needed by teleport API calls.
type AccountInfo struct {
	AccountUUID      string
	EmailAddress     string
	OrganizationUUID string
	DisplayName      string
}

// PreparedRequest contains the auth values required by Sessions API calls.
type PreparedRequest struct {
	AccessToken string
	OrgUUID     string
}

// Config wires token storage, cached account state, and profile fallback for teleport auth.
type Config struct {
	TokenGetter    TokenGetter
	ProfileFetcher ProfileFetcher
	Account        *AccountInfo
	Getenv         func(string) string
}

// Preparer validates and prepares OAuth auth context for teleport API requests.
type Preparer struct {
	tokenGetter    TokenGetter
	profileFetcher ProfileFetcher
	account        *AccountInfo
	getenv         func(string) string
}

// NewPreparer creates a teleport auth preparer.
func NewPreparer(config Config) *Preparer {
	getenv := config.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return &Preparer{
		tokenGetter:    config.TokenGetter,
		profileFetcher: config.ProfileFetcher,
		account:        config.Account,
		getenv:         getenv,
	}
}

// PrepareAPIRequest returns the OAuth access token and organization UUID required by Sessions API calls.
func (p *Preparer) PrepareAPIRequest(ctx context.Context) (PreparedRequest, error) {
	tokens, err := p.tokens()
	if err != nil {
		return PreparedRequest{}, err
	}
	if tokens == nil || tokens.AccessToken == "" {
		return PreparedRequest{}, fmt.Errorf(MissingClaudeAIAuthMessage)
	}
	orgUUID, err := p.OrganizationUUID(ctx, tokens)
	if err != nil {
		return PreparedRequest{}, err
	}
	if orgUUID == "" {
		return PreparedRequest{}, fmt.Errorf(MissingOrganizationUUIDMessage)
	}
	return PreparedRequest{AccessToken: tokens.AccessToken, OrgUUID: orgUUID}, nil
}

// OrganizationUUID returns the cached, environment-provided, or profile-derived organization UUID.
func (p *Preparer) OrganizationUUID(ctx context.Context, tokens *oauthstorage.Tokens) (string, error) {
	if p.account != nil && p.account.OrganizationUUID != "" {
		return p.account.OrganizationUUID, nil
	}
	if p.getenv != nil {
		if orgUUID := p.getenv(EnvOrganizationUUID); orgUUID != "" {
			return orgUUID, nil
		}
	}
	if tokens == nil || tokens.AccessToken == "" || !HasProfileScope(tokens.Scopes) || p.profileFetcher == nil {
		return "", nil
	}
	profile, err := p.profileFetcher.FetchOAuthProfile(ctx, tokens.AccessToken)
	if err != nil || profile == nil {
		return "", nil
	}
	return profile.Organization.UUID, nil
}

// HasProfileScope reports whether OAuth scopes include user:profile.
func HasProfileScope(scopes []string) bool {
	return slices.Contains(scopes, oauthconfig.ClaudeAIProfileScope)
}

func (p *Preparer) tokens() (*oauthstorage.Tokens, error) {
	if p.tokenGetter == nil {
		return nil, nil
	}
	return p.tokenGetter.GetClaudeAIOAuthTokens()
}
