package controlplane

import (
	"context"
	"net/http"
	"time"
)

// MetricsEnabledResponse describes whether metrics logging is enabled for the current organization.
type MetricsEnabledResponse struct {
	MetricsLoggingEnabled bool `json:"metrics_logging_enabled"`
}

// FetchMetricsEnabled checks whether metrics logging is enabled for the authenticated caller.
func (c *Client) FetchMetricsEnabled(ctx context.Context, opts ...CallOption) (*MetricsEnabledResponse, error) {
	var response MetricsEnabledResponse
	options := append([]CallOption{
		WithTimeout(5 * time.Second),
		WithHeader("Content-Type", "application/json"),
	}, opts...)
	if err := c.doJSON(ctx, http.MethodGet, "/api/claude_code/organizations/metrics_enabled", nil, nil, &response, options...); err != nil {
		return nil, err
	}
	return &response, nil
}
