package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const DefaultReferralCampaign ReferralCampaign = "claude_code_guest_pass"

type ReferralCampaign string

// ReferralCodeDetails describes the shareable referral code returned by eligibility.
type ReferralCodeDetails struct {
	ReferralLink string           `json:"referral_link,omitempty"`
	Campaign     ReferralCampaign `json:"campaign,omitempty"`
}

// ReferrerRewardInfo describes the reward offered to the referrer.
type ReferrerRewardInfo struct {
	AmountMinorUnits int64  `json:"amount_minor_units"`
	Currency         string `json:"currency"`
}

// ReferralEligibilityResponse describes guest-pass referral eligibility.
type ReferralEligibilityResponse struct {
	Eligible            bool                 `json:"eligible"`
	RemainingPasses     *int                 `json:"remaining_passes,omitempty"`
	ReferralCodeDetails *ReferralCodeDetails `json:"referral_code_details,omitempty"`
	ReferrerReward      *ReferrerRewardInfo  `json:"referrer_reward,omitempty"`
}

// ReferralRedemptionsResponse describes current guest-pass redemptions.
type ReferralRedemptionsResponse struct {
	Redemptions []json.RawMessage `json:"redemptions,omitempty"`
	Limit       int               `json:"limit,omitempty"`
}

// FetchReferralEligibility fetches guest-pass referral eligibility for an organization.
func (c *Client) FetchReferralEligibility(ctx context.Context, orgUUID string, campaign ReferralCampaign, opts ...CallOption) (*ReferralEligibilityResponse, error) {
	if err := requireOrgUUID(orgUUID); err != nil {
		return nil, err
	}
	if campaign == "" {
		campaign = DefaultReferralCampaign
	}

	query := url.Values{}
	query.Set("campaign", string(campaign))

	var response ReferralEligibilityResponse
	path := "/api/oauth/organizations/" + url.PathEscape(orgUUID) + "/referral/eligibility"
	options := appendOrganizationHeader(orgUUID, append([]CallOption{WithTimeout(5 * time.Second)}, opts...))
	if err := c.doJSON(ctx, http.MethodGet, path, query, nil, &response, options...); err != nil {
		return nil, err
	}
	return &response, nil
}

// FetchReferralRedemptions fetches guest-pass referral redemptions for an organization.
func (c *Client) FetchReferralRedemptions(ctx context.Context, orgUUID string, campaign ReferralCampaign, opts ...CallOption) (*ReferralRedemptionsResponse, error) {
	if err := requireOrgUUID(orgUUID); err != nil {
		return nil, err
	}
	if campaign == "" {
		campaign = DefaultReferralCampaign
	}

	query := url.Values{}
	query.Set("campaign", string(campaign))

	var response ReferralRedemptionsResponse
	path := "/api/oauth/organizations/" + url.PathEscape(orgUUID) + "/referral/redemptions"
	options := appendOrganizationHeader(orgUUID, append([]CallOption{WithTimeout(10 * time.Second)}, opts...))
	if err := c.doJSON(ctx, http.MethodGet, path, query, nil, &response, options...); err != nil {
		return nil, err
	}
	return &response, nil
}

// FormatCreditAmount formats a referrer reward amount for display.
func FormatCreditAmount(reward ReferrerRewardInfo) string {
	symbol := currencySymbol(reward.Currency)
	amount := reward.AmountMinorUnits
	dollars := amount / 100
	cents := amount % 100
	if cents == 0 {
		return fmt.Sprintf("%s%d", symbol, dollars)
	}
	return fmt.Sprintf("%s%d.%02d", symbol, dollars, cents)
}

func currencySymbol(currency string) string {
	switch currency {
	case "USD":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	case "BRL":
		return "R$"
	case "CAD":
		return "CA$"
	case "AUD":
		return "A$"
	case "NZD":
		return "NZ$"
	case "SGD":
		return "S$"
	default:
		return currency + " "
	}
}
