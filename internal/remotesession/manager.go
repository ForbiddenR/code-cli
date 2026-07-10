package remotesession

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"code-cli/internal/sessionsapi"
	"code-cli/internal/sessionswebsocket"
)

// Config contains the immutable remote session settings used by a manager.
type Config struct {
	SessionID        string
	GetAccessToken   func() string
	OrgUUID          string
	HasInitialPrompt bool
	ViewerOnly       bool
}

// RemotePermissionResponse is the simplified permission result sent back to CCR.
type RemotePermissionResponse struct {
	Behavior     string         `json:"behavior"`
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
	Message      string         `json:"message,omitempty"`
}

// PermissionRequest is the can_use_tool control request subset handled by remote sessions.
type PermissionRequest struct {
	Subtype               string           `json:"subtype"`
	ToolName              string           `json:"tool_name"`
	Input                 map[string]any   `json:"input"`
	PermissionSuggestions []map[string]any `json:"permission_suggestions,omitempty"`
	BlockedPath           string           `json:"blocked_path,omitempty"`
	DecisionReason        string           `json:"decision_reason,omitempty"`
	Title                 string           `json:"title,omitempty"`
	DisplayName           string           `json:"display_name,omitempty"`
	ToolUseID             string           `json:"tool_use_id"`
	AgentID               string           `json:"agent_id,omitempty"`
	Description           string           `json:"description,omitempty"`
}

// Callbacks mirrors the events surfaced by RemoteSessionManager.ts.
type Callbacks struct {
	OnMessage             func(json.RawMessage)
	OnPermissionRequest   func(PermissionRequest, string)
	OnPermissionCancelled func(requestID string, toolUseID string)
	OnConnected           func()
	OnDisconnected        func()
	OnReconnecting        func()
	OnError               func(error)
}

// ControlTransport is the WebSocket control surface needed by the manager.
type ControlTransport interface {
	Connect(ctx context.Context) error
	Close()
	Reconnect()
	IsConnected() bool
	SendControlRequest(sessionswebsocket.ControlRequest) error
	SendControlResponse(sessionswebsocket.ControlResponse) error
}

// EventSender sends user messages to the remote session HTTP event endpoint.
type EventSender interface {
	SendEventToRemoteSession(ctx context.Context, sessionID string, messageContent sessionsapi.RemoteMessageContent, opts sessionsapi.SendEventOptions) (bool, error)
}

// Manager coordinates WebSocket control messages and HTTP user-message sends for one remote session.
type Manager struct {
	config                    Config
	callbacks                 Callbacks
	transport                 ControlTransport
	eventSender               EventSender
	pendingPermissionRequests map[string]PermissionRequest
}

// NewManager creates a remote session manager with injected transport and event sender dependencies.
func NewManager(config Config, callbacks Callbacks, transport ControlTransport, eventSender EventSender) *Manager {
	return &Manager{
		config:                    config,
		callbacks:                 callbacks,
		transport:                 transport,
		eventSender:               eventSender,
		pendingPermissionRequests: make(map[string]PermissionRequest),
	}
}

// CreateConfig mirrors createRemoteSessionConfig from the TypeScript implementation.
func CreateConfig(sessionID string, getAccessToken func() string, orgUUID string, hasInitialPrompt bool, viewerOnly bool) Config {
	return Config{SessionID: sessionID, GetAccessToken: getAccessToken, OrgUUID: orgUUID, HasInitialPrompt: hasInitialPrompt, ViewerOnly: viewerOnly}
}

// Connect opens the control transport and emits the connected callback on success.
func (m *Manager) Connect(ctx context.Context) error {
	if m.transport == nil {
		return fmt.Errorf("remote session transport is required")
	}
	if err := m.transport.Connect(ctx); err != nil {
		m.emitError(err)
		return err
	}
	callEmpty(m.callbacks.OnConnected)
	return nil
}

// HandleMessage processes one message received from the Sessions WebSocket.
func (m *Manager) HandleMessage(data json.RawMessage) error {
	messageType, err := messageType(data)
	if err != nil {
		return err
	}
	switch messageType {
	case "control_request":
		return m.handleControlRequest(data)
	case "control_cancel_request":
		return m.handleControlCancelRequest(data)
	case "control_response":
		return nil
	default:
		callMessage(m.callbacks.OnMessage, data)
		return nil
	}
}

// SendMessage posts one user message to the remote session event endpoint.
func (m *Manager) SendMessage(ctx context.Context, content sessionsapi.RemoteMessageContent, opts sessionsapi.SendEventOptions) (bool, error) {
	if m.eventSender == nil {
		err := fmt.Errorf("remote session event sender is required")
		m.emitError(err)
		return false, err
	}
	ok, err := m.eventSender.SendEventToRemoteSession(ctx, m.config.SessionID, content, opts)
	if err != nil {
		m.emitError(err)
		return ok, err
	}
	if !ok {
		m.emitError(fmt.Errorf("failed to send message to session %s", m.config.SessionID))
	}
	return ok, nil
}

// RespondToPermissionRequest sends a permission response for a pending can_use_tool request.
func (m *Manager) RespondToPermissionRequest(requestID string, result RemotePermissionResponse) error {
	if _, ok := m.pendingPermissionRequests[requestID]; !ok {
		err := fmt.Errorf("no pending permission request with id: %s", requestID)
		m.emitError(err)
		return err
	}
	delete(m.pendingPermissionRequests, requestID)
	response := map[string]any{"behavior": result.Behavior}
	if result.Behavior == "allow" {
		response["updatedInput"] = cloneMap(result.UpdatedInput)
	} else {
		response["message"] = result.Message
	}
	return m.sendControlResponse(sessionswebsocket.NewSuccessResponse(requestID, response))
}

// IsConnected reports whether the control transport is connected.
func (m *Manager) IsConnected() bool {
	return m.transport != nil && m.transport.IsConnected()
}

// CancelSession sends the interrupt control request used by Ctrl+C/Escape remote cancellation.
func (m *Manager) CancelSession(requestID string) error {
	if m.config.ViewerOnly {
		return nil
	}
	if m.transport == nil {
		return fmt.Errorf("remote session transport is required")
	}
	return m.transport.SendControlRequest(sessionswebsocket.NewInterruptRequest(requestID))
}

// SessionID returns the configured remote session ID.
func (m *Manager) SessionID() string {
	return m.config.SessionID
}

// Disconnect closes the control transport and clears pending permission requests.
func (m *Manager) Disconnect() {
	if m.transport != nil {
		m.transport.Close()
	}
	m.pendingPermissionRequests = make(map[string]PermissionRequest)
}

// Reconnect forces the control transport to reconnect.
func (m *Manager) Reconnect() {
	if m.transport != nil {
		m.transport.Reconnect()
	}
}

// PendingPermissionRequest returns a copy of a pending permission request for tests and integrations.
func (m *Manager) PendingPermissionRequest(requestID string) (PermissionRequest, bool) {
	request, ok := m.pendingPermissionRequests[requestID]
	if !ok {
		return PermissionRequest{}, false
	}
	request.Input = cloneMap(request.Input)
	return request, true
}

func (m *Manager) handleControlRequest(data json.RawMessage) error {
	var envelope struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode control request: %w", err)
	}
	var subtype struct {
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(envelope.Request, &subtype); err != nil {
		return fmt.Errorf("decode control request subtype: %w", err)
	}
	if subtype.Subtype != "can_use_tool" {
		return m.sendControlResponse(sessionswebsocket.NewErrorResponse(envelope.RequestID, "Unsupported control request subtype: "+subtype.Subtype))
	}
	var request PermissionRequest
	if err := json.Unmarshal(envelope.Request, &request); err != nil {
		return fmt.Errorf("decode permission request: %w", err)
	}
	request.Input = cloneMap(request.Input)
	m.pendingPermissionRequests[envelope.RequestID] = request
	if m.callbacks.OnPermissionRequest != nil {
		m.callbacks.OnPermissionRequest(request, envelope.RequestID)
	}
	return nil
}

func (m *Manager) handleControlCancelRequest(data json.RawMessage) error {
	var cancel struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(data, &cancel); err != nil {
		return fmt.Errorf("decode control cancel request: %w", err)
	}
	pending := m.pendingPermissionRequests[cancel.RequestID]
	delete(m.pendingPermissionRequests, cancel.RequestID)
	if m.callbacks.OnPermissionCancelled != nil {
		m.callbacks.OnPermissionCancelled(cancel.RequestID, pending.ToolUseID)
	}
	return nil
}

func (m *Manager) sendControlResponse(response sessionswebsocket.ControlResponse) error {
	if m.transport == nil {
		return fmt.Errorf("remote session transport is required")
	}
	if err := m.transport.SendControlResponse(response); err != nil {
		m.emitError(err)
		return err
	}
	return nil
}

func (m *Manager) emitError(err error) {
	if err != nil && m.callbacks.OnError != nil {
		m.callbacks.OnError(err)
	}
}

func messageType(data []byte) (string, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return "", fmt.Errorf("decode session message: %w", err)
	}
	if envelope.Type == "" {
		return "", fmt.Errorf("session message type is required")
	}
	return envelope.Type, nil
}

func callMessage(callback func(json.RawMessage), message json.RawMessage) {
	if callback != nil {
		callback(append(json.RawMessage(nil), message...))
	}
}

func callEmpty(callback func()) {
	if callback != nil {
		callback()
	}
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	cloned := make(map[string]any, len(values))
	maps.Copy(cloned, values)
	return cloned
}
