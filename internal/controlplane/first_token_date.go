package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// FirstTokenDateResponse contains the optional first Claude Code token date.
type FirstTokenDateResponse struct {
	FirstTokenDate *string `json:"first_token_date"`
}

// FetchClaudeCodeFirstTokenDate fetches and validates the first Claude Code token date.
func (c *Client) FetchClaudeCodeFirstTokenDate(ctx context.Context, opts ...CallOption) (*FirstTokenDateResponse, error) {
	var response FirstTokenDateResponse
	options := append([]CallOption{WithTimeout(10 * time.Second)}, opts...)
	if err := c.doJSON(ctx, http.MethodGet, "/api/organization/claude_code_first_token_date", nil, nil, &response, options...); err != nil {
		return nil, err
	}
	if response.FirstTokenDate != nil && !validDate(*response.FirstTokenDate) {
		return nil, fmt.Errorf("invalid first_token_date: %s", *response.FirstTokenDate)
	}
	return &response, nil
}

func validDate(value string) bool {
	if value == "" {
		return false
	}
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return true
	}
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return true
	}
	return false
}
