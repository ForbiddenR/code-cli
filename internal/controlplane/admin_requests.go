package controlplane

import (
	"context"
	"net/http"
	"net/url"
)

type AdminRequestType string

const (
	AdminRequestLimitIncrease AdminRequestType = "limit_increase"
	AdminRequestSeatUpgrade   AdminRequestType = "seat_upgrade"
)

type AdminRequestStatus string

const (
	AdminRequestPending   AdminRequestStatus = "pending"
	AdminRequestApproved  AdminRequestStatus = "approved"
	AdminRequestDismissed AdminRequestStatus = "dismissed"
)

// AdminRequestSeatUpgradeDetails contains optional seat-upgrade request details.
type AdminRequestSeatUpgradeDetails struct {
	Message         *string `json:"message,omitempty"`
	CurrentSeatTier *string `json:"current_seat_tier,omitempty"`
}

// AdminRequestCreateParams is the create-admin-request payload.
type AdminRequestCreateParams struct {
	RequestType AdminRequestType                `json:"request_type"`
	Details     *AdminRequestSeatUpgradeDetails `json:"details"`
}

// AdminRequest is one admin request returned by the control plane.
type AdminRequest struct {
	UUID          string                          `json:"uuid"`
	Status        AdminRequestStatus              `json:"status"`
	RequesterUUID *string                         `json:"requester_uuid,omitempty"`
	CreatedAt     string                          `json:"created_at"`
	RequestType   AdminRequestType                `json:"request_type"`
	Details       *AdminRequestSeatUpgradeDetails `json:"details"`
}

// AdminRequestEligibility describes whether a request type is available.
type AdminRequestEligibility struct {
	RequestType AdminRequestType `json:"request_type"`
	IsAllowed   bool             `json:"is_allowed"`
}

// CreateAdminRequest creates an admin request for an organization.
func (c *Client) CreateAdminRequest(ctx context.Context, orgUUID string, params AdminRequestCreateParams, opts ...CallOption) (*AdminRequest, error) {
	var response AdminRequest
	path := "/api/oauth/organizations/" + url.PathEscape(orgUUID) + "/admin_requests"
	options := appendOrganizationHeader(orgUUID, opts)
	if err := c.doJSON(ctx, http.MethodPost, path, nil, params, &response, options...); err != nil {
		return nil, err
	}
	return &response, nil
}

// GetMyAdminRequests lists admin requests for the authenticated user.
func (c *Client) GetMyAdminRequests(ctx context.Context, orgUUID string, requestType AdminRequestType, statuses []AdminRequestStatus, opts ...CallOption) ([]AdminRequest, error) {
	query := url.Values{}
	query.Set("request_type", string(requestType))
	for _, status := range statuses {
		query.Add("statuses", string(status))
	}

	var response []AdminRequest
	path := "/api/oauth/organizations/" + url.PathEscape(orgUUID) + "/admin_requests/me"
	options := appendOrganizationHeader(orgUUID, opts)
	if err := c.doJSON(ctx, http.MethodGet, path, query, nil, &response, options...); err != nil {
		return nil, err
	}
	return response, nil
}

// CheckAdminRequestEligibility checks whether an admin request type is allowed.
func (c *Client) CheckAdminRequestEligibility(ctx context.Context, orgUUID string, requestType AdminRequestType, opts ...CallOption) (*AdminRequestEligibility, error) {
	query := url.Values{}
	query.Set("request_type", string(requestType))

	var response AdminRequestEligibility
	path := "/api/oauth/organizations/" + url.PathEscape(orgUUID) + "/admin_requests/eligibility"
	options := appendOrganizationHeader(orgUUID, opts)
	if err := c.doJSON(ctx, http.MethodGet, path, query, nil, &response, options...); err != nil {
		return nil, err
	}
	return &response, nil
}

func appendOrganizationHeader(orgUUID string, opts []CallOption) []CallOption {
	if orgUUID == "" {
		return opts
	}
	options := make([]CallOption, 0, len(opts)+1)
	options = append(options, opts...)
	options = append(options, WithHeader("x-organization-uuid", orgUUID))
	return options
}
