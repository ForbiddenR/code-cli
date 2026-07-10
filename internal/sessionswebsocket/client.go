package sessionswebsocket

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code-cli/internal/sessionsapi"
)

const (
	// ReconnectDelay matches the base SessionsWebSocket reconnect delay.
	ReconnectDelay = 2 * time.Second
	// ForceReconnectDelay matches the explicit reconnect delay before opening a new socket.
	ForceReconnectDelay = 500 * time.Millisecond
	// PingInterval matches the keepalive ping interval used by the TypeScript client.
	PingInterval = 30 * time.Second
	// MaxReconnectAttempts is the general reconnect budget after transient closes.
	MaxReconnectAttempts = 5
	// MaxSessionNotFoundRetries is the special 4001 retry budget used during compaction windows.
	MaxSessionNotFoundRetries = 3
	// SessionNotFoundCloseCode is the close code that can be transient during compaction.
	SessionNotFoundCloseCode = 4001
	// UnauthorizedCloseCode is a permanent server-side rejection.
	UnauthorizedCloseCode = 4003
)

type State string

const (
	StateConnecting State = "connecting"
	StateConnected  State = "connected"
	StateClosed     State = "closed"
)

// Callbacks mirrors the observable events surfaced by the TypeScript SessionsWebSocket.
type Callbacks struct {
	OnMessage      func(json.RawMessage)
	OnClose        func()
	OnError        func(error)
	OnConnected    func()
	OnReconnecting func()
}

// Config contains the values needed to subscribe to a remote session stream.
type Config struct {
	BaseURL        string
	SessionID      string
	OrgUUID        string
	GetAccessToken func() string
	Callbacks      Callbacks
}

// AuthHeaders returns the OAuth headers used when opening the Sessions WebSocket.
func AuthHeaders(accessToken string) http.Header {
	headers := http.Header{}
	if accessToken != "" {
		headers.Set("Authorization", "Bearer "+accessToken)
	}
	headers.Set("anthropic-version", sessionsapi.AnthropicVersion)
	return headers
}

// SubscribeURL builds /v1/sessions/ws/{sessionID}/subscribe with the organization UUID query parameter.
func SubscribeURL(baseURL string, sessionID string, orgUUID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("session id is required")
	}
	if strings.TrimSpace(orgUUID) == "" {
		return "", fmt.Errorf("organization uuid is required")
	}
	if baseURL == "" {
		baseURL = sessionsapi.DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("parse base url: missing scheme or host")
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
		// Already a WebSocket URL.
	default:
		return "", fmt.Errorf("unsupported websocket base url scheme: %s", parsed.Scheme)
	}
	escapedPath := joinPath(parsed.EscapedPath(), "/v1/sessions/ws/"+url.PathEscape(sessionID)+"/subscribe")
	query := parsed.Query()
	query.Set("organization_uuid", orgUUID)
	prefix := parsed.Scheme + "://"
	if parsed.User != nil {
		prefix += parsed.User.String() + "@"
	}
	prefix += parsed.Host
	if encodedQuery := query.Encode(); encodedQuery != "" {
		return prefix + escapedPath + "?" + encodedQuery, nil
	}
	return prefix + escapedPath, nil
}

// IsSessionsMessage reports whether raw JSON has an object shape with a string type field.
func IsSessionsMessage(data []byte) bool {
	var message struct {
		Type any `json:"type"`
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return false
	}
	_, ok := message.Type.(string)
	return ok
}

// DecodeSessionsMessage validates a raw Sessions WebSocket message and returns the original JSON.
func DecodeSessionsMessage(data []byte) (json.RawMessage, bool, error) {
	if !json.Valid(data) {
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, false, err
		}
	}
	if !IsSessionsMessage(data) {
		return nil, false, nil
	}
	return append(json.RawMessage(nil), data...), true, nil
}

// ReconnectDecision describes how a close event should be handled.
type ReconnectDecision struct {
	Reconnect              bool
	Close                  bool
	Delay                  time.Duration
	ReconnectAttempts      int
	SessionNotFoundRetries int
	Reason                 string
}

// ReconnectDecisionForClose mirrors the SessionsWebSocket close-code retry policy.
func ReconnectDecisionForClose(closeCode int, previousState State, reconnectAttempts int, sessionNotFoundRetries int) ReconnectDecision {
	if previousState == StateClosed {
		return ReconnectDecision{Close: false, ReconnectAttempts: reconnectAttempts, SessionNotFoundRetries: sessionNotFoundRetries, Reason: "already closed"}
	}
	if closeCode == UnauthorizedCloseCode {
		return ReconnectDecision{Close: true, ReconnectAttempts: reconnectAttempts, SessionNotFoundRetries: sessionNotFoundRetries, Reason: "permanent close code"}
	}
	if closeCode == SessionNotFoundCloseCode {
		nextSessionRetries := sessionNotFoundRetries + 1
		if nextSessionRetries > MaxSessionNotFoundRetries {
			return ReconnectDecision{Close: true, ReconnectAttempts: reconnectAttempts, SessionNotFoundRetries: nextSessionRetries, Reason: "session not found retry budget exhausted"}
		}
		return ReconnectDecision{Reconnect: true, Delay: ReconnectDelay * time.Duration(nextSessionRetries), ReconnectAttempts: reconnectAttempts, SessionNotFoundRetries: nextSessionRetries, Reason: "session not found"}
	}
	if previousState == StateConnected && reconnectAttempts < MaxReconnectAttempts {
		nextReconnectAttempts := reconnectAttempts + 1
		return ReconnectDecision{Reconnect: true, Delay: ReconnectDelay, ReconnectAttempts: nextReconnectAttempts, SessionNotFoundRetries: sessionNotFoundRetries, Reason: "transient close"}
	}
	return ReconnectDecision{Close: true, ReconnectAttempts: reconnectAttempts, SessionNotFoundRetries: sessionNotFoundRetries, Reason: "not reconnecting"}
}

// ControlRequest is an outbound control request sent over the Sessions WebSocket.
type ControlRequest struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Request   map[string]any `json:"request"`
}

// NewControlRequest creates a control_request envelope.
func NewControlRequest(requestID string, request map[string]any) ControlRequest {
	return ControlRequest{Type: "control_request", RequestID: requestID, Request: cloneMap(request)}
}

// NewInterruptRequest creates the interrupt control request used by remote session cancellation.
func NewInterruptRequest(requestID string) ControlRequest {
	return NewControlRequest(requestID, map[string]any{"subtype": "interrupt"})
}

// ControlResponse is an outbound control response sent over the Sessions WebSocket.
type ControlResponse struct {
	Type     string              `json:"type"`
	Response ControlResponseBody `json:"response"`
}

// ControlResponseBody is the success or error response payload inside a control response.
type ControlResponseBody struct {
	Subtype                   string           `json:"subtype"`
	RequestID                 string           `json:"request_id"`
	Response                  map[string]any   `json:"response,omitempty"`
	Error                     string           `json:"error,omitempty"`
	PendingPermissionRequests []ControlRequest `json:"pending_permission_requests,omitempty"`
}

// NewSuccessResponse creates a successful control_response envelope.
func NewSuccessResponse(requestID string, response map[string]any) ControlResponse {
	return ControlResponse{Type: "control_response", Response: ControlResponseBody{Subtype: "success", RequestID: requestID, Response: cloneMap(response)}}
}

// NewErrorResponse creates an error control_response envelope.
func NewErrorResponse(requestID string, message string) ControlResponse {
	return ControlResponse{Type: "control_response", Response: ControlResponseBody{Subtype: "error", RequestID: requestID, Error: message}}
}

// MarshalMessage serializes a WebSocket control or SDK message.
func MarshalMessage(message any) ([]byte, error) {
	data, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal websocket message: %w", err)
	}
	return data, nil
}

func joinPath(basePath string, suffix string) string {
	basePath = strings.TrimRight(basePath, "/")
	if basePath == "" {
		return suffix
	}
	return basePath + suffix
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	cloned := make(map[string]any, len(values))
	maps.Copy(cloned, values)
	return cloned
}
