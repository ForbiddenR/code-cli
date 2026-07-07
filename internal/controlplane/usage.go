package controlplane

import (
	"context"
	"net/http"
)

// RateLimit describes quota utilization for one reset window.
type RateLimit struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

// ExtraUsage describes optional extra-usage credit utilization.
type ExtraUsage struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
}

// Utilization is the control-plane usage response.
type Utilization struct {
	FiveHour          *RateLimit  `json:"five_hour,omitempty"`
	SevenDay          *RateLimit  `json:"seven_day,omitempty"`
	SevenDayOAuthApps *RateLimit  `json:"seven_day_oauth_apps,omitempty"`
	SevenDayOpus      *RateLimit  `json:"seven_day_opus,omitempty"`
	SevenDaySonnet    *RateLimit  `json:"seven_day_sonnet,omitempty"`
	ExtraUsage        *ExtraUsage `json:"extra_usage,omitempty"`
}

// FetchUtilization fetches usage utilization for the authenticated caller.
func (c *Client) FetchUtilization(ctx context.Context, opts ...CallOption) (*Utilization, error) {
	var response Utilization
	if err := c.doJSON(ctx, http.MethodGet, "/api/oauth/usage", nil, nil, &response, opts...); err != nil {
		return nil, err
	}
	return &response, nil
}
