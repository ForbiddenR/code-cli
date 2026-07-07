package controlplane

import (
	"context"
	"net/http"
	"time"
)

// AccountSettings describes Grove-related account settings.
type AccountSettings struct {
	GroveEnabled        *bool   `json:"grove_enabled"`
	GroveNoticeViewedAt *string `json:"grove_notice_viewed_at"`
}

// GroveConfig describes Grove notice configuration returned by the control plane.
type GroveConfig struct {
	GroveEnabled            bool `json:"grove_enabled"`
	DomainExcluded          bool `json:"domain_excluded"`
	NoticeIsGracePeriod     bool `json:"notice_is_grace_period"`
	NoticeReminderFrequency *int `json:"notice_reminder_frequency"`
}

type groveConfigWireResponse struct {
	GroveEnabled            bool  `json:"grove_enabled"`
	DomainExcluded          *bool `json:"domain_excluded"`
	NoticeIsGracePeriod     *bool `json:"notice_is_grace_period"`
	NoticeReminderFrequency *int  `json:"notice_reminder_frequency"`
}

type groveSettingsUpdateRequest struct {
	GroveEnabled bool `json:"grove_enabled"`
}

// FetchGroveSettings fetches the current user's Grove account settings.
func (c *Client) FetchGroveSettings(ctx context.Context, opts ...CallOption) (*AccountSettings, error) {
	var response AccountSettings
	if err := c.doJSON(ctx, http.MethodGet, "/api/oauth/account/settings", nil, nil, &response, opts...); err != nil {
		return nil, err
	}
	return &response, nil
}

// MarkGroveNoticeViewed marks the Grove notice as viewed for the current account.
func (c *Client) MarkGroveNoticeViewed(ctx context.Context, opts ...CallOption) error {
	return c.doJSON(ctx, http.MethodPost, "/api/oauth/account/grove_notice_viewed", nil, map[string]any{}, nil, opts...)
}

// UpdateGroveSettings updates the current user's Grove account settings.
func (c *Client) UpdateGroveSettings(ctx context.Context, groveEnabled bool, opts ...CallOption) error {
	return c.doJSON(ctx, http.MethodPatch, "/api/oauth/account/settings", nil, groveSettingsUpdateRequest{GroveEnabled: groveEnabled}, nil, opts...)
}

// FetchGroveNoticeConfig fetches Grove notice configuration.
func (c *Client) FetchGroveNoticeConfig(ctx context.Context, opts ...CallOption) (*GroveConfig, error) {
	var wire groveConfigWireResponse
	options := append([]CallOption{WithTimeout(3 * time.Second)}, opts...)
	if err := c.doJSON(ctx, http.MethodGet, "/api/claude_code_grove", nil, nil, &wire, options...); err != nil {
		return nil, err
	}

	response := GroveConfig{
		GroveEnabled:            wire.GroveEnabled,
		NoticeIsGracePeriod:     true,
		NoticeReminderFrequency: wire.NoticeReminderFrequency,
	}
	if wire.DomainExcluded != nil {
		response.DomainExcluded = *wire.DomainExcluded
	}
	if wire.NoticeIsGracePeriod != nil {
		response.NoticeIsGracePeriod = *wire.NoticeIsGracePeriod
	}
	return &response, nil
}

// CalculateShouldShowGrove determines whether the Grove notice should be shown.
func CalculateShouldShowGrove(settings *AccountSettings, config *GroveConfig, showIfAlreadyViewed bool, now time.Time) bool {
	if settings == nil || config == nil {
		return false
	}
	if settings.GroveEnabled != nil {
		return false
	}
	if showIfAlreadyViewed {
		return true
	}
	if !config.NoticeIsGracePeriod {
		return true
	}
	if config.NoticeReminderFrequency != nil && settings.GroveNoticeViewedAt != nil {
		viewedAt, err := parseGroveViewedAt(*settings.GroveNoticeViewedAt)
		if err != nil {
			return false
		}
		daysSinceViewed := int(now.Sub(viewedAt).Hours() / 24)
		return daysSinceViewed >= *config.NoticeReminderFrequency
	}
	return settings.GroveNoticeViewedAt == nil
}

func parseGroveViewedAt(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return time.Parse("2006-01-02", value)
}
