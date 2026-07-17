// Package gitremote migrates pure git remote parsing and teleport session
// repository validation from utils/detectRepository.ts and the pure comparison
// logic in utils/teleport.tsx (validateSessionRepository).
package gitremote

import (
	"regexp"
	"strings"

	"code-cli/internal/sessionsapi"
)

// ParsedRepository is one host/owner/name triple from a git remote URL.
type ParsedRepository struct {
	Host  string
	Owner string
	Name  string
}

// OwnerName returns "owner/name".
func (p ParsedRepository) OwnerName() string {
	return p.Owner + "/" + p.Name
}

var (
	// SSH format: git@host:owner/repo.git
	sshRemotePattern = regexp.MustCompile(`^git@([^:]+):([^/]+)/([^/]+?)(?:\.git)?$`)
	// URL format: https://host/owner/repo.git, ssh://git@host/owner/repo, git://host/owner/repo
	urlRemotePattern = regexp.MustCompile(`^(https?|ssh|git)://(?:[^@]+@)?([^/:]+(?::\d+)?)/([^/]+)/([^/]+?)(?:\.git)?$`)
	// Real TLDs are purely alphabetic (com, org, io). SSH aliases like
	// "github.com-work" have last segment "com-work" which contains a hyphen.
	realTLDPattern = regexp.MustCompile(`^[a-zA-Z]+$`)
)

// ParseGitRemote parses a git remote URL into host, owner, and name.
// Accepts any host (github.com, GHE, etc.).
func ParseGitRemote(input string) (ParsedRepository, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ParsedRepository{}, false
	}

	if match := sshRemotePattern.FindStringSubmatch(trimmed); len(match) == 4 {
		host := match[1]
		if !LooksLikeRealHostname(host) {
			return ParsedRepository{}, false
		}
		return ParsedRepository{Host: host, Owner: match[2], Name: match[3]}, true
	}

	if match := urlRemotePattern.FindStringSubmatch(trimmed); len(match) == 5 {
		protocol := match[1]
		hostWithPort := match[2]
		hostWithoutPort, _, _ := strings.Cut(hostWithPort, ":")
		if !LooksLikeRealHostname(hostWithoutPort) {
			return ParsedRepository{}, false
		}
		// Only preserve port for HTTPS — SSH/git ports are not usable for
		// constructing web URLs.
		host := hostWithoutPort
		if protocol == "https" || protocol == "http" {
			host = hostWithPort
		}
		return ParsedRepository{Host: host, Owner: match[3], Name: match[4]}, true
	}

	return ParsedRepository{}, false
}

// ParseGitHubRepository parses a git remote URL or "owner/repo" string and
// returns "owner/repo". Only returns results for github.com hosts — GHE URLs
// return false. Also accepts plain "owner/repo" strings.
func ParseGitHubRepository(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}

	if parsed, ok := ParseGitRemote(trimmed); ok {
		if parsed.Host != "github.com" {
			return "", false
		}
		return parsed.OwnerName(), true
	}

	if !strings.Contains(trimmed, "://") && !strings.Contains(trimmed, "@") && strings.Contains(trimmed, "/") {
		parts := strings.Split(trimmed, "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			repo := strings.TrimSuffix(parts[1], ".git")
			return parts[0] + "/" + repo, true
		}
	}
	return "", false
}

// LooksLikeRealHostname checks whether a hostname looks like a real domain
// rather than an SSH config alias. Requires a dot and a purely alphabetic TLD.
func LooksLikeRealHostname(host string) bool {
	if !strings.Contains(host, ".") {
		return false
	}
	lastDot := strings.LastIndex(host, ".")
	if lastDot < 0 || lastDot == len(host)-1 {
		return false
	}
	return realTLDPattern.MatchString(host[lastDot+1:])
}

// StripPort removes a trailing :port from a host for comparison.
// SSH remotes omit the port while HTTPS remotes may include a non-standard port.
func StripPort(host string) string {
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		port := host[idx+1:]
		if port != "" && isAllDigits(port) {
			return host[:idx]
		}
	}
	return host
}

func isAllDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ValidationStatus is the result status of session repository validation.
type ValidationStatus string

const (
	StatusMatch          ValidationStatus = "match"
	StatusMismatch       ValidationStatus = "mismatch"
	StatusNotInRepo      ValidationStatus = "not_in_repo"
	StatusNoRepoRequired ValidationStatus = "no_repo_required"
	StatusError          ValidationStatus = "error"
)

// ValidationResult mirrors RepoValidationResult from teleport.tsx.
type ValidationResult struct {
	Status       ValidationStatus
	SessionRepo  string
	CurrentRepo  *string
	SessionHost  string
	CurrentHost  string
	ErrorMessage string
}

// ValidateSessionRepository compares the current repository against the session's
// git_repository source. It is pure: callers supply the already-detected current
// repository (or nil when not in a git repo).
func ValidateSessionRepository(session sessionsapi.SessionResource, current *ParsedRepository) ValidationResult {
	var currentRepo *string
	var currentHost string
	if current != nil {
		repo := current.OwnerName()
		currentRepo = &repo
		currentHost = current.Host
	}

	gitURL := gitRepositorySourceURL(session)
	if gitURL == "" {
		return ValidationResult{Status: StatusNoRepoRequired}
	}

	sessionParsed, sessionParsedOK := ParseGitRemote(gitURL)
	var sessionRepo string
	var sessionHost string
	if sessionParsedOK {
		sessionRepo = sessionParsed.OwnerName()
		sessionHost = sessionParsed.Host
	} else if ownerRepo, ok := ParseGitHubRepository(gitURL); ok {
		sessionRepo = ownerRepo
	}
	if sessionRepo == "" {
		return ValidationResult{Status: StatusNoRepoRequired}
	}

	if currentRepo == nil {
		return ValidationResult{
			Status:      StatusNotInRepo,
			SessionRepo: sessionRepo,
			SessionHost: sessionHost,
			CurrentRepo: nil,
		}
	}

	repoMatch := strings.EqualFold(*currentRepo, sessionRepo)
	hostMatch := true
	if current != nil && sessionParsedOK {
		hostMatch = strings.EqualFold(StripPort(current.Host), StripPort(sessionParsed.Host))
	}
	if repoMatch && hostMatch {
		return ValidationResult{
			Status:      StatusMatch,
			SessionRepo: sessionRepo,
			CurrentRepo: currentRepo,
		}
	}

	return ValidationResult{
		Status:      StatusMismatch,
		SessionRepo: sessionRepo,
		CurrentRepo: currentRepo,
		SessionHost: sessionHost,
		CurrentHost: currentHost,
	}
}

func gitRepositorySourceURL(session sessionsapi.SessionResource) string {
	for _, source := range session.SessionContext.Sources {
		if source.Type == "git_repository" && strings.TrimSpace(source.URL) != "" {
			return source.URL
		}
	}
	return ""
}
