package remotesession

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"code-cli/internal/sessionsapi"
	"code-cli/internal/sessionswebsocket"
)

func TestCreateConfig(t *testing.T) {
	getToken := func() string { return "access" }
	config := CreateConfig("sess_1", getToken, "org_1", true, true)
	if config.SessionID != "sess_1" || config.GetAccessToken() != "access" || config.OrgUUID != "org_1" || !config.HasInitialPrompt || !config.ViewerOnly {
		t.Fatalf("config = %#v", config)
	}
}

func TestConnectUsesTransportAndCallback(t *testing.T) {
	transport := &stubTransport{}
	connected := false
	manager := NewManager(Config{SessionID: "sess_1"}, Callbacks{OnConnected: func() { connected = true }}, transport, nil)

	if err := manager.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if transport.connects != 1 || !connected {
		t.Fatalf("connects = %d, connected = %v", transport.connects, connected)
	}
}

func TestConnectReportsTransportError(t *testing.T) {
	transport := &stubTransport{connectErr: errors.New("dial failed")}
	var callbackErr error
	manager := NewManager(Config{}, Callbacks{OnError: func(err error) { callbackErr = err }}, transport, nil)

	err := manager.Connect(context.Background())
	if err == nil || err.Error() != "dial failed" || callbackErr == nil || callbackErr.Error() != "dial failed" {
		t.Fatalf("err = %v, callbackErr = %v", err, callbackErr)
	}
}

func TestHandleMessageForwardsSDKMessages(t *testing.T) {
	var got json.RawMessage
	manager := NewManager(Config{}, Callbacks{OnMessage: func(message json.RawMessage) { got = message }}, nil, nil)
	input := json.RawMessage(`{"type":"assistant","message":{"content":[]}}`)

	if err := manager.HandleMessage(input); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if string(got) != string(input) {
		t.Fatalf("message = %s", got)
	}
	got[0] = '['
	if string(input) == string(got) {
		t.Fatal("message callback received aliased raw message")
	}
}

func TestHandlePermissionRequestStoresAndCallbacks(t *testing.T) {
	var callbackRequest PermissionRequest
	var callbackRequestID string
	manager := NewManager(Config{}, Callbacks{OnPermissionRequest: func(request PermissionRequest, requestID string) {
		callbackRequest = request
		callbackRequestID = requestID
	}}, nil, nil)

	err := manager.HandleMessage(json.RawMessage(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"pwd"},"tool_use_id":"tool_1","agent_id":"agent_1"}}`))
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	pending, ok := manager.PendingPermissionRequest("req_1")
	if !ok {
		t.Fatal("pending request not stored")
	}
	if callbackRequestID != "req_1" || callbackRequest.ToolName != "Bash" || pending.ToolUseID != "tool_1" || pending.Input["command"] != "pwd" {
		t.Fatalf("callback = %#v/%q pending = %#v", callbackRequest, callbackRequestID, pending)
	}
	pending.Input["command"] = "mutated"
	stored, _ := manager.PendingPermissionRequest("req_1")
	if stored.Input["command"] != "pwd" {
		t.Fatalf("stored request was mutated: %#v", stored)
	}
}

func TestHandleUnsupportedControlRequestSendsError(t *testing.T) {
	transport := &stubTransport{}
	manager := NewManager(Config{}, Callbacks{}, transport, nil)

	err := manager.HandleMessage(json.RawMessage(`{"type":"control_request","request_id":"req_1","request":{"subtype":"unknown_subtype"}}`))
	if err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(transport.controlResponses) != 1 {
		t.Fatalf("responses = %#v", transport.controlResponses)
	}
	response := transport.controlResponses[0]
	if response.Response.Subtype != "error" || response.Response.RequestID != "req_1" || response.Response.Error != "Unsupported control request subtype: unknown_subtype" {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandleControlCancelRequestDeletesPendingAndCallbacks(t *testing.T) {
	var cancelledRequestID string
	var cancelledToolUseID string
	manager := NewManager(Config{}, Callbacks{OnPermissionCancelled: func(requestID string, toolUseID string) {
		cancelledRequestID = requestID
		cancelledToolUseID = toolUseID
	}}, nil, nil)
	if err := manager.HandleMessage(json.RawMessage(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"tool_1"}}`)); err != nil {
		t.Fatalf("permission HandleMessage() error = %v", err)
	}

	if err := manager.HandleMessage(json.RawMessage(`{"type":"control_cancel_request","request_id":"req_1"}`)); err != nil {
		t.Fatalf("cancel HandleMessage() error = %v", err)
	}
	if cancelledRequestID != "req_1" || cancelledToolUseID != "tool_1" {
		t.Fatalf("cancelled = %q/%q", cancelledRequestID, cancelledToolUseID)
	}
	if _, ok := manager.PendingPermissionRequest("req_1"); ok {
		t.Fatal("pending request was not removed")
	}
}

func TestControlResponseMessagesAreIgnored(t *testing.T) {
	called := false
	manager := NewManager(Config{}, Callbacks{OnMessage: func(json.RawMessage) { called = true }}, nil, nil)
	if err := manager.HandleMessage(json.RawMessage(`{"type":"control_response","response":{"subtype":"success","request_id":"req_1"}}`)); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if called {
		t.Fatal("control response was forwarded as SDK message")
	}
}

func TestRespondToPermissionRequestSendsSuccessAndClearsPending(t *testing.T) {
	transport := &stubTransport{}
	manager := NewManager(Config{}, Callbacks{}, transport, nil)
	if err := manager.HandleMessage(json.RawMessage(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Edit","input":{"file":"a.go"},"tool_use_id":"tool_1"}}`)); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}

	err := manager.RespondToPermissionRequest("req_1", RemotePermissionResponse{Behavior: "allow", UpdatedInput: map[string]any{"file": "b.go"}})
	if err != nil {
		t.Fatalf("RespondToPermissionRequest() error = %v", err)
	}
	if _, ok := manager.PendingPermissionRequest("req_1"); ok {
		t.Fatal("pending request still exists")
	}
	if len(transport.controlResponses) != 1 {
		t.Fatalf("responses = %#v", transport.controlResponses)
	}
	response := transport.controlResponses[0]
	if response.Response.Subtype != "success" || response.Response.RequestID != "req_1" || response.Response.Response["behavior"] != "allow" {
		t.Fatalf("response = %#v", response)
	}
	updatedInput := response.Response.Response["updatedInput"].(map[string]any)
	if updatedInput["file"] != "b.go" {
		t.Fatalf("updatedInput = %#v", updatedInput)
	}
}

func TestRespondToPermissionRequestSendsDenyMessage(t *testing.T) {
	transport := &stubTransport{}
	manager := NewManager(Config{}, Callbacks{}, transport, nil)
	if err := manager.HandleMessage(json.RawMessage(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Edit","input":{},"tool_use_id":"tool_1"}}`)); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}

	err := manager.RespondToPermissionRequest("req_1", RemotePermissionResponse{Behavior: "deny", Message: "blocked"})
	if err != nil {
		t.Fatalf("RespondToPermissionRequest() error = %v", err)
	}
	response := transport.controlResponses[0].Response.Response
	if response["behavior"] != "deny" || response["message"] != "blocked" {
		t.Fatalf("response = %#v", response)
	}
}

func TestRespondToMissingPermissionRequestReportsError(t *testing.T) {
	var callbackErr error
	manager := NewManager(Config{}, Callbacks{OnError: func(err error) { callbackErr = err }}, &stubTransport{}, nil)
	err := manager.RespondToPermissionRequest("missing", RemotePermissionResponse{Behavior: "allow"})
	if err == nil || callbackErr == nil {
		t.Fatalf("err = %v callbackErr = %v", err, callbackErr)
	}
}

func TestSendMessageUsesEventSender(t *testing.T) {
	sender := &stubEventSender{ok: true}
	manager := NewManager(Config{SessionID: "sess_1"}, Callbacks{}, nil, sender)
	ok, err := manager.SendMessage(context.Background(), "hello", sessionsapi.SendEventOptions{UUID: "uuid_1"})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if !ok || sender.sessionID != "sess_1" || sender.content != "hello" || sender.opts.UUID != "uuid_1" {
		t.Fatalf("ok = %v sender = %#v", ok, sender)
	}
}

func TestSendMessageReportsFalseAndErrors(t *testing.T) {
	sender := &stubEventSender{ok: false}
	var errorsSeen []error
	manager := NewManager(Config{SessionID: "sess_1"}, Callbacks{OnError: func(err error) { errorsSeen = append(errorsSeen, err) }}, nil, sender)
	ok, err := manager.SendMessage(context.Background(), "hello", sessionsapi.SendEventOptions{})
	if err != nil || ok || len(errorsSeen) != 1 {
		t.Fatalf("ok = %v err = %v errors = %v", ok, err, errorsSeen)
	}

	sender.err = errors.New("send failed")
	ok, err = manager.SendMessage(context.Background(), "hello", sessionsapi.SendEventOptions{})
	if err == nil || ok || len(errorsSeen) != 2 {
		t.Fatalf("ok = %v err = %v errors = %v", ok, err, errorsSeen)
	}
}

func TestCancelSessionHonorsViewerOnly(t *testing.T) {
	transport := &stubTransport{}
	viewer := NewManager(Config{ViewerOnly: true}, Callbacks{}, transport, nil)
	if err := viewer.CancelSession("req_1"); err != nil {
		t.Fatalf("viewer CancelSession() error = %v", err)
	}
	if len(transport.controlRequests) != 0 {
		t.Fatalf("viewer sent requests = %#v", transport.controlRequests)
	}

	manager := NewManager(Config{}, Callbacks{}, transport, nil)
	if err := manager.CancelSession("req_2"); err != nil {
		t.Fatalf("CancelSession() error = %v", err)
	}
	if len(transport.controlRequests) != 1 || transport.controlRequests[0].RequestID != "req_2" || transport.controlRequests[0].Request["subtype"] != "interrupt" {
		t.Fatalf("requests = %#v", transport.controlRequests)
	}
}

func TestConnectionHelpers(t *testing.T) {
	transport := &stubTransport{connected: true}
	manager := NewManager(Config{SessionID: "sess_1"}, Callbacks{}, transport, nil)
	if !manager.IsConnected() || manager.SessionID() != "sess_1" {
		t.Fatalf("connected = %v session = %q", manager.IsConnected(), manager.SessionID())
	}
	manager.Reconnect()
	if transport.reconnects != 1 {
		t.Fatalf("reconnects = %d", transport.reconnects)
	}
	if err := manager.HandleMessage(json.RawMessage(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Edit","input":{},"tool_use_id":"tool_1"}}`)); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	manager.Disconnect()
	if transport.closes != 1 {
		t.Fatalf("closes = %d", transport.closes)
	}
	if _, ok := manager.PendingPermissionRequest("req_1"); ok {
		t.Fatal("pending request survived disconnect")
	}
}

type stubTransport struct {
	connects         int
	connectErr       error
	connected        bool
	closes           int
	reconnects       int
	controlRequests  []sessionswebsocket.ControlRequest
	controlResponses []sessionswebsocket.ControlResponse
}

func (t *stubTransport) Connect(context.Context) error {
	t.connects++
	if t.connectErr != nil {
		return t.connectErr
	}
	t.connected = true
	return nil
}

func (t *stubTransport) Close() {
	t.closes++
	t.connected = false
}

func (t *stubTransport) Reconnect() {
	t.reconnects++
}

func (t *stubTransport) IsConnected() bool {
	return t.connected
}

func (t *stubTransport) SendControlRequest(request sessionswebsocket.ControlRequest) error {
	t.controlRequests = append(t.controlRequests, request)
	return nil
}

func (t *stubTransport) SendControlResponse(response sessionswebsocket.ControlResponse) error {
	t.controlResponses = append(t.controlResponses, response)
	return nil
}

type stubEventSender struct {
	ok        bool
	err       error
	sessionID string
	content   sessionsapi.RemoteMessageContent
	opts      sessionsapi.SendEventOptions
}

func (s *stubEventSender) SendEventToRemoteSession(_ context.Context, sessionID string, messageContent sessionsapi.RemoteMessageContent, opts sessionsapi.SendEventOptions) (bool, error) {
	s.sessionID = sessionID
	s.content = messageContent
	s.opts = opts
	return s.ok, s.err
}
