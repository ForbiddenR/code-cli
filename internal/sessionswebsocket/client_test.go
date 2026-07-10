package sessionswebsocket

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"code-cli/internal/sessionsapi"
)

func TestSubscribeURL(t *testing.T) {
	url, err := SubscribeURL("https://api.anthropic.com", "sess/1", "org_1")
	if err != nil {
		t.Fatalf("SubscribeURL() error = %v", err)
	}
	if url != "wss://api.anthropic.com/v1/sessions/ws/sess%2F1/subscribe?organization_uuid=org_1" {
		t.Fatalf("url = %q", url)
	}
}

func TestSubscribeURLPreservesBasePathAndQuery(t *testing.T) {
	url, err := SubscribeURL("https://example.com/base?existing=1", "sess_1", "org_1")
	if err != nil {
		t.Fatalf("SubscribeURL() error = %v", err)
	}
	if url != "wss://example.com/base/v1/sessions/ws/sess_1/subscribe?existing=1&organization_uuid=org_1" {
		t.Fatalf("url = %q", url)
	}
}

func TestSubscribeURLSupportsLocalHTTP(t *testing.T) {
	url, err := SubscribeURL("http://localhost:8080", "sess_1", "org_1")
	if err != nil {
		t.Fatalf("SubscribeURL() error = %v", err)
	}
	if url != "ws://localhost:8080/v1/sessions/ws/sess_1/subscribe?organization_uuid=org_1" {
		t.Fatalf("url = %q", url)
	}
}

func TestSubscribeURLValidation(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		sessionID string
		orgUUID   string
		want      string
	}{
		{name: "missing session", baseURL: "https://api.anthropic.com", orgUUID: "org_1", want: "session id is required"},
		{name: "missing organization", baseURL: "https://api.anthropic.com", sessionID: "sess_1", want: "organization uuid is required"},
		{name: "missing host", baseURL: "api.anthropic.com", sessionID: "sess_1", orgUUID: "org_1", want: "missing scheme or host"},
		{name: "unsupported scheme", baseURL: "ftp://api.anthropic.com", sessionID: "sess_1", orgUUID: "org_1", want: "unsupported websocket base url scheme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SubscribeURL(tt.baseURL, tt.sessionID, tt.orgUUID)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestAuthHeaders(t *testing.T) {
	headers := AuthHeaders("access")
	if got := headers.Get("Authorization"); got != "Bearer access" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := headers.Get("anthropic-version"); got != sessionsapi.AnthropicVersion {
		t.Fatalf("anthropic-version = %q", got)
	}
	withoutToken := AuthHeaders("")
	if got := withoutToken.Get("Authorization"); got != "" {
		t.Fatalf("Authorization without token = %q", got)
	}
}

func TestIsSessionsMessage(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "sdk message", data: `{"type":"assistant","message":{"content":[]}}`, want: true},
		{name: "control request", data: `{"type":"control_request","request_id":"req_1","request":{"subtype":"interrupt"}}`, want: true},
		{name: "unknown string type", data: `{"type":"future_type"}`, want: true},
		{name: "missing type", data: `{"message":"hello"}`, want: false},
		{name: "numeric type", data: `{"type":1}`, want: false},
		{name: "array", data: `[]`, want: false},
		{name: "invalid", data: `{`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSessionsMessage([]byte(tt.data)); got != tt.want {
				t.Fatalf("IsSessionsMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeSessionsMessage(t *testing.T) {
	message, ok, err := DecodeSessionsMessage([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"req_1"}}`))
	if err != nil {
		t.Fatalf("DecodeSessionsMessage() error = %v", err)
	}
	if !ok || string(message) == "" {
		t.Fatalf("message = %s, ok = %v", message, ok)
	}
	message[0] = '['
	if IsSessionsMessage(message) {
		t.Fatal("returned raw message aliases caller mutation")
	}

	_, ok, err = DecodeSessionsMessage([]byte(`{"message":"hello"}`))
	if err != nil || ok {
		t.Fatalf("non-session message ok = %v, err = %v", ok, err)
	}
	_, _, err = DecodeSessionsMessage([]byte(`{`))
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestReconnectDecisionForClose(t *testing.T) {
	tests := []struct {
		name                     string
		closeCode                int
		previousState            State
		reconnectAttempts        int
		sessionNotFoundRetries   int
		wantReconnect            bool
		wantClose                bool
		wantDelay                time.Duration
		wantReconnectAttempts    int
		wantSessionNotFoundRetry int
	}{
		{name: "already closed", closeCode: 1006, previousState: StateClosed, wantReconnectAttempts: 0},
		{name: "unauthorized closes", closeCode: UnauthorizedCloseCode, previousState: StateConnected, wantClose: true},
		{name: "session not found retries", closeCode: SessionNotFoundCloseCode, previousState: StateConnected, wantReconnect: true, wantDelay: ReconnectDelay, wantSessionNotFoundRetry: 1},
		{name: "session not found backoff grows", closeCode: SessionNotFoundCloseCode, previousState: StateConnected, sessionNotFoundRetries: 2, wantReconnect: true, wantDelay: 3 * ReconnectDelay, wantSessionNotFoundRetry: 3},
		{name: "session not found budget exhausted", closeCode: SessionNotFoundCloseCode, previousState: StateConnected, sessionNotFoundRetries: 3, wantClose: true, wantSessionNotFoundRetry: 4},
		{name: "connected transient retries", closeCode: 1006, previousState: StateConnected, wantReconnect: true, wantDelay: ReconnectDelay, wantReconnectAttempts: 1},
		{name: "connected transient budget exhausted", closeCode: 1006, previousState: StateConnected, reconnectAttempts: MaxReconnectAttempts, wantClose: true, wantReconnectAttempts: MaxReconnectAttempts},
		{name: "connecting does not reconnect", closeCode: 1006, previousState: StateConnecting, wantClose: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := ReconnectDecisionForClose(tt.closeCode, tt.previousState, tt.reconnectAttempts, tt.sessionNotFoundRetries)
			if decision.Reconnect != tt.wantReconnect || decision.Close != tt.wantClose || decision.Delay != tt.wantDelay || decision.ReconnectAttempts != tt.wantReconnectAttempts || decision.SessionNotFoundRetries != tt.wantSessionNotFoundRetry {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func TestControlRequestAndResponseMarshalling(t *testing.T) {
	input := map[string]any{"subtype": "set_model", "model": "claude-opus-4-8"}
	request := NewControlRequest("req_1", input)
	input["model"] = "mutated"
	data, err := MarshalMessage(request)
	if err != nil {
		t.Fatalf("MarshalMessage(request) error = %v", err)
	}
	assertJSONEqual(t, string(data), `{"type":"control_request","request_id":"req_1","request":{"model":"claude-opus-4-8","subtype":"set_model"}}`)

	interrupt := NewInterruptRequest("req_interrupt")
	data, err = MarshalMessage(interrupt)
	if err != nil {
		t.Fatalf("MarshalMessage(interrupt) error = %v", err)
	}
	assertJSONEqual(t, string(data), `{"type":"control_request","request_id":"req_interrupt","request":{"subtype":"interrupt"}}`)

	responseInput := map[string]any{"behavior": "allow"}
	success := NewSuccessResponse("req_2", responseInput)
	responseInput["behavior"] = "deny"
	data, err = MarshalMessage(success)
	if err != nil {
		t.Fatalf("MarshalMessage(success) error = %v", err)
	}
	assertJSONEqual(t, string(data), `{"type":"control_response","response":{"subtype":"success","request_id":"req_2","response":{"behavior":"allow"}}}`)

	errorResponse := NewErrorResponse("req_3", "unsupported control request subtype: unknown")
	data, err = MarshalMessage(errorResponse)
	if err != nil {
		t.Fatalf("MarshalMessage(error) error = %v", err)
	}
	assertJSONEqual(t, string(data), `{"type":"control_response","response":{"subtype":"error","request_id":"req_3","error":"unsupported control request subtype: unknown"}}`)
}

func TestConstantsMatchTypeScriptSessionWebSocket(t *testing.T) {
	if ReconnectDelay != 2*time.Second {
		t.Fatalf("ReconnectDelay = %v", ReconnectDelay)
	}
	if ForceReconnectDelay != 500*time.Millisecond {
		t.Fatalf("ForceReconnectDelay = %v", ForceReconnectDelay)
	}
	if PingInterval != 30*time.Second {
		t.Fatalf("PingInterval = %v", PingInterval)
	}
	if MaxReconnectAttempts != 5 || MaxSessionNotFoundRetries != 3 {
		t.Fatalf("retries = %d/%d", MaxReconnectAttempts, MaxSessionNotFoundRetries)
	}
}

func assertJSONEqual(t *testing.T, got string, want string) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	gotData, err := json.Marshal(gotValue)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantData, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotData) != string(wantData) {
		t.Fatalf("json = %s, want %s", gotData, wantData)
	}
}
