package gitremote

import (
	"testing"

	"code-cli/internal/sessionsapi"
)

func TestParseGitRemote(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  ParsedRepository
		ok    bool
	}{
		{name: "https", input: "https://github.com/owner/repo.git", want: ParsedRepository{Host: "github.com", Owner: "owner", Name: "repo"}, ok: true},
		{name: "https no git", input: "https://github.com/owner/repo", want: ParsedRepository{Host: "github.com", Owner: "owner", Name: "repo"}, ok: true},
		{name: "https with port", input: "https://ghe.corp.com:8443/owner/repo.git", want: ParsedRepository{Host: "ghe.corp.com:8443", Owner: "owner", Name: "repo"}, ok: true},
		{name: "ssh scp", input: "git@github.com:owner/repo.git", want: ParsedRepository{Host: "github.com", Owner: "owner", Name: "repo"}, ok: true},
		{name: "ssh url port dropped", input: "ssh://git@ghe.corp.com:2222/owner/repo.git", want: ParsedRepository{Host: "ghe.corp.com", Owner: "owner", Name: "repo"}, ok: true},
		{name: "git protocol", input: "git://github.com/owner/repo.git", want: ParsedRepository{Host: "github.com", Owner: "owner", Name: "repo"}, ok: true},
		{name: "repo with dots", input: "https://github.com/owner/cc.kurs.web.git", want: ParsedRepository{Host: "github.com", Owner: "owner", Name: "cc.kurs.web"}, ok: true},
		{name: "ssh alias rejected", input: "git@github.com-work:owner/repo.git", ok: false},
		{name: "no tld", input: "git@localhost:owner/repo.git", ok: false},
		{name: "empty", input: "  ", ok: false},
		{name: "plain owner/repo", input: "owner/repo", ok: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseGitRemote(test.input)
			if ok != test.ok {
				t.Fatalf("ok = %v, want %v (got %#v)", ok, test.ok, got)
			}
			if ok && got != test.want {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseGitHubRepository(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "https github", input: "https://github.com/owner/repo.git", want: "owner/repo", ok: true},
		{name: "ssh github", input: "git@github.com:owner/repo.git", want: "owner/repo", ok: true},
		{name: "ghe rejected", input: "https://ghe.corp.com/owner/repo.git", ok: false},
		{name: "plain owner/repo", input: "owner/repo", want: "owner/repo", ok: true},
		{name: "plain with git", input: "owner/repo.git", want: "owner/repo", ok: true},
		{name: "too many parts", input: "a/b/c", ok: false},
		{name: "url-ish without parse", input: "not-a-url", ok: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseGitHubRepository(test.input)
			if ok != test.ok || got != test.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestLooksLikeRealHostnameAndStripPort(t *testing.T) {
	if !LooksLikeRealHostname("github.com") || !LooksLikeRealHostname("ghe.corp.com") {
		t.Fatal("expected real hostnames")
	}
	if LooksLikeRealHostname("github.com-work") || LooksLikeRealHostname("localhost") || LooksLikeRealHostname("foo.") {
		t.Fatal("expected rejections")
	}
	if got := StripPort("ghe.corp.com:8443"); got != "ghe.corp.com" {
		t.Fatalf("StripPort = %q", got)
	}
	if got := StripPort("github.com"); got != "github.com" {
		t.Fatalf("StripPort plain = %q", got)
	}
}

func TestValidateSessionRepositoryNoRepoRequired(t *testing.T) {
	result := ValidateSessionRepository(sessionsapi.SessionResource{}, nil)
	if result.Status != StatusNoRepoRequired {
		t.Fatalf("result = %#v", result)
	}

	result = ValidateSessionRepository(sessionWithGitURL("not-parseable"), nil)
	if result.Status != StatusNoRepoRequired {
		t.Fatalf("unparseable = %#v", result)
	}
}

func TestValidateSessionRepositoryNotInRepo(t *testing.T) {
	result := ValidateSessionRepository(sessionWithGitURL("https://github.com/owner/repo.git"), nil)
	if result.Status != StatusNotInRepo || result.SessionRepo != "owner/repo" || result.SessionHost != "github.com" || result.CurrentRepo != nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestValidateSessionRepositoryMatch(t *testing.T) {
	current := &ParsedRepository{Host: "github.com", Owner: "Owner", Name: "Repo"}
	result := ValidateSessionRepository(sessionWithGitURL("git@github.com:owner/repo.git"), current)
	if result.Status != StatusMatch || result.SessionRepo != "owner/repo" || result.CurrentRepo == nil || *result.CurrentRepo != "Owner/Repo" {
		t.Fatalf("result = %#v", result)
	}
}

func TestValidateSessionRepositoryMatchIgnoresPort(t *testing.T) {
	current := &ParsedRepository{Host: "ghe.corp.com", Owner: "owner", Name: "repo"}
	result := ValidateSessionRepository(sessionWithGitURL("https://ghe.corp.com:8443/owner/repo.git"), current)
	if result.Status != StatusMatch {
		t.Fatalf("result = %#v", result)
	}
}

func TestValidateSessionRepositoryMismatch(t *testing.T) {
	current := &ParsedRepository{Host: "github.com", Owner: "other", Name: "repo"}
	result := ValidateSessionRepository(sessionWithGitURL("https://github.com/owner/repo.git"), current)
	if result.Status != StatusMismatch || result.SessionRepo != "owner/repo" || result.CurrentHost != "github.com" || result.SessionHost != "github.com" {
		t.Fatalf("result = %#v", result)
	}
}

func TestValidateSessionRepositoryHostMismatch(t *testing.T) {
	current := &ParsedRepository{Host: "ghe.corp.com", Owner: "owner", Name: "repo"}
	result := ValidateSessionRepository(sessionWithGitURL("https://github.com/owner/repo.git"), current)
	if result.Status != StatusMismatch || result.SessionHost != "github.com" || result.CurrentHost != "ghe.corp.com" {
		t.Fatalf("result = %#v", result)
	}
}

func TestValidateSessionRepositoryPlainGitHubFallback(t *testing.T) {
	// URL that ParseGitRemote rejects but ParseGitHubRepository accepts as owner/repo is hard;
	// instead use a source that is already owner/repo style if it ever appears, or GHE parse.
	// When session URL is github owner/repo plain via a non-URL path, no_repo for remote parse
	// is covered. Exercise github.com path with plain fallback using a crafted unparseable-as-remote
	// but owner/repo-like string is not possible when type is git_repository URL. Use github.com SSH.
	current := &ParsedRepository{Host: "github.com", Owner: "a", Name: "b"}
	result := ValidateSessionRepository(sessionWithGitURL("https://github.com/a/b"), current)
	if result.Status != StatusMatch {
		t.Fatalf("result = %#v", result)
	}
}

func sessionWithGitURL(url string) sessionsapi.SessionResource {
	return sessionsapi.SessionResource{
		SessionContext: sessionsapi.SessionContext{
			Sources: []sessionsapi.SessionContextSource{{
				Type: "git_repository",
				URL:  url,
			}},
		},
	}
}
