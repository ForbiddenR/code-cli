package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
)

// AdditionalModelOption is one additional model choice returned by bootstrap.
type AdditionalModelOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// BootstrapResponse contains optional startup metadata from the control plane.
type BootstrapResponse struct {
	ClientData             json.RawMessage         `json:"client_data,omitempty"`
	AdditionalModelOptions []AdditionalModelOption `json:"additional_model_options,omitempty"`
}

type bootstrapWireResponse struct {
	ClientData             json.RawMessage `json:"client_data"`
	AdditionalModelOptions []struct {
		Model       string `json:"model"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"additional_model_options"`
}

// FetchBootstrap fetches startup metadata without persisting it.
func (c *Client) FetchBootstrap(ctx context.Context, opts ...CallOption) (*BootstrapResponse, error) {
	var wire bootstrapWireResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/claude_cli/bootstrap", nil, nil, &wire, opts...); err != nil {
		return nil, err
	}

	response := BootstrapResponse{ClientData: wire.ClientData}
	if string(response.ClientData) == "null" {
		response.ClientData = nil
	}
	if len(wire.AdditionalModelOptions) > 0 {
		response.AdditionalModelOptions = make([]AdditionalModelOption, 0, len(wire.AdditionalModelOptions))
		for _, option := range wire.AdditionalModelOptions {
			response.AdditionalModelOptions = append(response.AdditionalModelOptions, AdditionalModelOption{
				Value:       option.Model,
				Label:       option.Name,
				Description: option.Description,
			})
		}
	}
	return &response, nil
}
