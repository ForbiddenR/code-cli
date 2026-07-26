package webfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (t *WebFetchTool) checkDomain(ctx context.Context, domain string) error {
	domain = strings.ToLower(domain)
	if t.config.DomainCache.Get(domain) {
		return nil
	}
	endpoint, err := url.Parse(t.config.DomainInfoURL)
	if err != nil {
		return fmt.Errorf("domain check URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("domain", domain)
	endpoint.RawQuery = query.Encode()

	requestCtx, cancel := context.WithTimeout(ctx, DomainCheckTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return domainCheckFailed(domain)
	}
	response, err := t.config.PreflightClient.Do(request)
	if err != nil {
		return domainCheckFailed(domain)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return domainCheckFailed(domain)
	}
	var result struct {
		CanFetch bool `json:"can_fetch"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return domainCheckFailed(domain)
	}
	if !result.CanFetch {
		return fmt.Errorf("Claude Code is unable to fetch from %s", domain)
	}
	t.config.DomainCache.Set(domain)
	return nil
}

func domainCheckFailed(domain string) error {
	return fmt.Errorf("Unable to verify if domain %s is safe to fetch. This may be due to network restrictions or enterprise security policies blocking claude.ai.", domain)
}

func (t *WebFetchTool) fetch(ctx context.Context, initialURL *url.URL, prompt string) (FetchedContent, error) {
	current := cloneURL(initialURL)
	for redirects := 0; ; {
		requestCtx, cancel := context.WithTimeout(ctx, FetchTimeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, current.String(), nil)
		if err != nil {
			cancel()
			return FetchedContent{}, err
		}
		request.Header.Set("Accept", "text/markdown, text/html, */*")
		request.Header.Set("User-Agent", t.config.UserAgent)
		response, err := t.config.HTTPClient.Do(request)
		if err != nil {
			cancel()
			return FetchedContent{}, err
		}

		if isRedirectStatus(response.StatusCode) {
			location := response.Header.Get("Location")
			response.Body.Close()
			cancel()
			if location == "" {
				return FetchedContent{}, errors.New("Redirect missing Location header")
			}
			target, err := current.Parse(location)
			if err != nil {
				return FetchedContent{}, err
			}
			if !canFollowRedirect(current, target) {
				message := redirectMessage(current.String(), target.String(), response.StatusCode, prompt)
				return FetchedContent{
					Content:     message,
					Bytes:       len([]byte(message)),
					Code:        response.StatusCode,
					CodeText:    redirectStatusText(response.StatusCode),
					ContentType: "text/plain",
					Redirect:    true,
				}, nil
			}
			redirects++
			if redirects > MaxRedirects {
				return FetchedContent{}, fmt.Errorf("Too many redirects (exceeded %d)", MaxRedirects)
			}
			current = target
			continue
		}

		content, err := t.readResponse(requestCtx, response, current)
		response.Body.Close()
		cancel()
		return content, err
	}
}

func (t *WebFetchTool) readResponse(ctx context.Context, response *http.Response, fetchedURL *url.URL) (FetchedContent, error) {
	if response.StatusCode == http.StatusForbidden && response.Header.Get("X-Proxy-Error") == "blocked-by-allowlist" {
		domain := strings.ToLower(fetchedURL.Hostname())
		message, _ := json.Marshal(struct {
			ErrorType string `json:"error_type"`
			Domain    string `json:"domain"`
			Message   string `json:"message"`
		}{
			ErrorType: "EGRESS_BLOCKED",
			Domain:    domain,
			Message:   fmt.Sprintf("Access to %s is blocked by the network egress proxy.", domain),
		})
		return FetchedContent{}, errors.New(string(message))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return FetchedContent{}, fmt.Errorf("Request failed with status code %d", response.StatusCode)
	}
	if response.ContentLength > MaxHTTPContentLength {
		return FetchedContent{}, fmt.Errorf("Response exceeds maximum size of %d bytes", MaxHTTPContentLength)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxHTTPContentLength+1))
	if err != nil {
		return FetchedContent{}, err
	}
	if len(raw) > MaxHTTPContentLength {
		return FetchedContent{}, fmt.Errorf("Response exceeds maximum size of %d bytes", MaxHTTPContentLength)
	}

	contentType := response.Header.Get("Content-Type")
	content := strings.ToValidUTF8(string(raw), "�")
	persistedPath := ""
	persistedSize := 0
	if isBinaryContentType(contentType) && t.config.Persister != nil {
		extension := extensionForContentType(contentType)
		path, persistErr := t.config.Persister.Persist(ctx, "webfetch."+extension, contentType, raw)
		if persistErr == nil {
			persistedPath = path
			persistedSize = len(raw)
		}
	}
	if containsHTML(contentType) {
		content, err = t.config.Converter.Convert(content)
		if err != nil {
			return FetchedContent{}, fmt.Errorf("convert HTML to Markdown: %w", err)
		}
	}
	return FetchedContent{
		Content:       content,
		Bytes:         len(raw),
		Code:          response.StatusCode,
		CodeText:      http.StatusText(response.StatusCode),
		ContentType:   contentType,
		PersistedPath: persistedPath,
		PersistedSize: persistedSize,
	}, nil
}

func isRedirectStatus(code int) bool {
	return code == http.StatusMovedPermanently || code == http.StatusFound || code == http.StatusTemporaryRedirect || code == http.StatusPermanentRedirect
}

func canFollowRedirect(from, to *url.URL) bool {
	return from.Scheme == to.Scheme &&
		effectivePort(from) == effectivePort(to) &&
		to.User == nil &&
		normalizedHostname(from.Hostname()) == normalizedHostname(to.Hostname())
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if value.Scheme == "http" {
		return "80"
	}
	if value.Scheme == "https" {
		return "443"
	}
	return ""
}

func normalizedHostname(hostname string) string {
	return strings.TrimPrefix(strings.ToLower(hostname), "www.")
}

func redirectStatusText(code int) string {
	switch code {
	case http.StatusMovedPermanently:
		return "Moved Permanently"
	case http.StatusFound:
		return "Found"
	case http.StatusTemporaryRedirect:
		return "Temporary Redirect"
	case http.StatusPermanentRedirect:
		return "Permanent Redirect"
	default:
		return http.StatusText(code)
	}
}

func redirectMessage(originalURL, redirectURL string, status int, prompt string) string {
	return fmt.Sprintf("REDIRECT DETECTED: The URL redirects to a different host.\n\nOriginal URL: %s\nRedirect URL: %s\nStatus: %d %s\n\nTo complete your request, I need to fetch content from the redirected URL. Please use WebFetch again with these parameters:\n- url: \"%s\"\n- prompt: \"%s\"", originalURL, redirectURL, status, redirectStatusText(status), redirectURL, prompt)
}

func containsHTML(contentType string) bool {
	return strings.Contains(contentType, "text/html")
}
