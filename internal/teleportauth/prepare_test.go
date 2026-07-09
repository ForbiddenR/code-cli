package teleportauth

import (
	"context"
	"errors"
	"testing"

	"code-cli/internal/oauthclient"
	"code-cli/internal/oauthconfig"
	"code-cli/internal/oauthstorage"
)

func TestPrepareAPIRequestUsesCachedAccountOrganization(t *testing.T) {
	preparer := NewPreparer(Config{
		TokenGetter: tokenGetter{tokens: &oauthstorage.Tokens{
			AccessToken: "access",
			Scopes:      []string{oauthconfig.ClaudeAIInferenceScope},
		}},
		Account: &AccountInfo{OrganizationUUID: "org_cached"},
	})

	prepared, err := preparer.PrepareAPIRequest(context.Background())
	if err != nil {
		t.Fatalf("PrepareAPIRequest() error = %v", err)
	}
	if prepared.AccessToken != "access" || prepared.OrgUUID != "org_cached" {
		t.Fatalf("prepared = %#v", prepared)
	}
}

func TestPrepareAPIRequestUsesEnvironmentOrganization(t *testing.T) {
	preparer := NewPreparer(Config{
		TokenGetter: tokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}},
		Getenv: func(key string) string {
			if key == EnvOrganizationUUID {
				return "org_env"
			}
			return ""
		},
	})

	prepared, err := preparer.PrepareAPIRequest(context.Background())
	if err != nil {
		t.Fatalf("PrepareAPIRequest() error = %v", err)
	}
	if prepared.OrgUUID != "org_env" {
		t.Fatalf("prepared = %#v", prepared)
	}
}

func TestPrepareAPIRequestFetchesProfileOrganization(t *testing.T) {
	fetcher := &profileFetcher{profile: &oauthclient.OAuthProfileResponse{Organization: oauthclient.ProfileOrganization{UUID: "org_profile"}}}
	preparer := NewPreparer(Config{
		TokenGetter: tokenGetter{tokens: &oauthstorage.Tokens{
			AccessToken: "access",
			Scopes:      []string{oauthconfig.ClaudeAIProfileScope, oauthconfig.ClaudeAIInferenceScope},
		}},
		ProfileFetcher: fetcher,
		Getenv:         func(string) string { return "" },
	})

	prepared, err := preparer.PrepareAPIRequest(context.Background())
	if err != nil {
		t.Fatalf("PrepareAPIRequest() error = %v", err)
	}
	if prepared.OrgUUID != "org_profile" || fetcher.accessToken != "access" {
		t.Fatalf("prepared = %#v, fetcher = %#v", prepared, fetcher)
	}
}

func TestPrepareAPIRequestRequiresOAuthToken(t *testing.T) {
	preparer := NewPreparer(Config{TokenGetter: tokenGetter{tokens: nil}})
	_, err := preparer.PrepareAPIRequest(context.Background())
	if err == nil || err.Error() != MissingClaudeAIAuthMessage {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareAPIRequestRequiresOrganizationUUID(t *testing.T) {
	preparer := NewPreparer(Config{
		TokenGetter: tokenGetter{tokens: &oauthstorage.Tokens{
			AccessToken: "access",
			Scopes:      []string{oauthconfig.ClaudeAIInferenceScope},
		}},
		Getenv: func(string) string { return "" },
	})
	_, err := preparer.PrepareAPIRequest(context.Background())
	if err == nil || err.Error() != MissingOrganizationUUIDMessage {
		t.Fatalf("error = %v", err)
	}
}

func TestOrganizationUUIDDoesNotFetchProfileWithoutProfileScope(t *testing.T) {
	fetcher := &profileFetcher{profile: &oauthclient.OAuthProfileResponse{Organization: oauthclient.ProfileOrganization{UUID: "org_profile"}}}
	preparer := NewPreparer(Config{ProfileFetcher: fetcher, Getenv: func(string) string { return "" }})
	orgUUID, err := preparer.OrganizationUUID(context.Background(), &oauthstorage.Tokens{
		AccessToken: "access",
		Scopes:      []string{oauthconfig.ClaudeAIInferenceScope},
	})
	if err != nil {
		t.Fatalf("OrganizationUUID() error = %v", err)
	}
	if orgUUID != "" || fetcher.accessToken != "" {
		t.Fatalf("orgUUID = %q, fetcher = %#v", orgUUID, fetcher)
	}
}

func TestOrganizationUUIDSwallowsProfileFetchFailure(t *testing.T) {
	preparer := NewPreparer(Config{
		ProfileFetcher: &profileFetcher{err: errors.New("profile failed")},
		Getenv:         func(string) string { return "" },
	})
	orgUUID, err := preparer.OrganizationUUID(context.Background(), &oauthstorage.Tokens{
		AccessToken: "access",
		Scopes:      []string{oauthconfig.ClaudeAIProfileScope},
	})
	if err != nil {
		t.Fatalf("OrganizationUUID() error = %v", err)
	}
	if orgUUID != "" {
		t.Fatalf("orgUUID = %q", orgUUID)
	}
}

func TestPrepareAPIRequestPropagatesTokenReadErrors(t *testing.T) {
	preparer := NewPreparer(Config{TokenGetter: tokenGetter{err: errors.New("read tokens")}})
	_, err := preparer.PrepareAPIRequest(context.Background())
	if err == nil || err.Error() != "read tokens" {
		t.Fatalf("error = %v", err)
	}
}

func TestHasProfileScope(t *testing.T) {
	if !HasProfileScope([]string{oauthconfig.ClaudeAIInferenceScope, oauthconfig.ClaudeAIProfileScope}) {
		t.Fatal("expected profile scope")
	}
	if HasProfileScope([]string{oauthconfig.ClaudeAIInferenceScope}) {
		t.Fatal("unexpected profile scope")
	}
}

type tokenGetter struct {
	tokens *oauthstorage.Tokens
	err    error
}

func (g tokenGetter) GetClaudeAIOAuthTokens() (*oauthstorage.Tokens, error) {
	return g.tokens, g.err
}

type profileFetcher struct {
	profile     *oauthclient.OAuthProfileResponse
	err         error
	accessToken string
}

func (f *profileFetcher) FetchOAuthProfile(_ context.Context, accessToken string) (*oauthclient.OAuthProfileResponse, error) {
	f.accessToken = accessToken
	return f.profile, f.err
}
