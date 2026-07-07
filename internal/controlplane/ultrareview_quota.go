package controlplane

import (
	"context"
	"net/http"
)

// UltrareviewQuota describes the caller's current ultrareview quota.
type UltrareviewQuota struct {
	ReviewsUsed      int  `json:"reviews_used"`
	ReviewsLimit     int  `json:"reviews_limit"`
	ReviewsRemaining int  `json:"reviews_remaining"`
	IsOverage        bool `json:"is_overage"`
}

// FetchUltrareviewQuota fetches the ultrareview quota for an organization.
func (c *Client) FetchUltrareviewQuota(ctx context.Context, orgUUID string, opts ...CallOption) (*UltrareviewQuota, error) {
	if err := requireOrgUUID(orgUUID); err != nil {
		return nil, err
	}

	var response UltrareviewQuota
	options := appendOrganizationHeader(orgUUID, opts)
	if err := c.doJSON(ctx, http.MethodGet, "/v1/ultrareview/quota", nil, nil, &response, options...); err != nil {
		return nil, err
	}
	return &response, nil
}
