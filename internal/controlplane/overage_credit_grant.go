package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// OverageCreditGrantInfo describes overage credit grant eligibility for the current user.
type OverageCreditGrantInfo struct {
	Available        bool    `json:"available"`
	Eligible         bool    `json:"eligible"`
	Granted          bool    `json:"granted"`
	AmountMinorUnits *int64  `json:"amount_minor_units"`
	Currency         *string `json:"currency"`
}

// FetchOverageCreditGrant fetches overage credit grant eligibility for an organization.
func (c *Client) FetchOverageCreditGrant(ctx context.Context, orgUUID string, opts ...CallOption) (*OverageCreditGrantInfo, error) {
	if err := requireOrgUUID(orgUUID); err != nil {
		return nil, err
	}

	var response OverageCreditGrantInfo
	path := "/api/oauth/organizations/" + url.PathEscape(orgUUID) + "/overage_credit_grant"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, &response, opts...); err != nil {
		return nil, err
	}
	return &response, nil
}

// FormatGrantAmount formats a supported overage grant amount for display.
func FormatGrantAmount(info OverageCreditGrantInfo) (string, bool) {
	if info.AmountMinorUnits == nil || info.Currency == nil {
		return "", false
	}
	if !strings.EqualFold(*info.Currency, "USD") {
		return "", false
	}

	amount := *info.AmountMinorUnits
	dollars := amount / 100
	cents := amount % 100
	if cents == 0 {
		return fmt.Sprintf("$%d", dollars), true
	}
	return fmt.Sprintf("$%d.%02d", dollars, cents), true
}
