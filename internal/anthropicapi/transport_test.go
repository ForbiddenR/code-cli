package anthropicapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"code-cli/internal/core"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type manualIdleTimer struct {
	mu       sync.Mutex
	callback func()
	delay    time.Duration
	stopped  bool
}

func (timer *manualIdleTimer) Stop() bool {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	wasActive := !timer.stopped
	timer.stopped = true
	return wasActive
}

func (timer *manualIdleTimer) Fire() {
	timer.mu.Lock()
	if timer.stopped {
		timer.mu.Unlock()
		return
	}
	callback := timer.callback
	timer.mu.Unlock()
	callback()
}

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (body *blockingReadCloser) Read([]byte) (int, error) {
	<-body.closed
	return 0, errors.New("body closed")
}

func (body *blockingReadCloser) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

type readCloser struct {
	io.Reader
	closed bool
}

func (body *readCloser) Close() error {
	body.closed = true
	return nil
}

func TestIdleReadCloserExpiresBlockedRead(t *testing.T) {
	body := newBlockingReadCloser()
	var timer *manualIdleTimer
	reader := newIdleReadCloserWithTimer(context.TODO(), body, 90*time.Second, func(delay time.Duration, callback func()) idleTimer {
		timer = &manualIdleTimer{callback: callback, delay: delay}
		return timer
	})
	defer reader.Close()

	result := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		result <- err
	}()
	timer.Fire()

	select {
	case err := <-result:
		if !errors.Is(err, ErrStreamIdleTimeout) {
			t.Fatalf("Read() error = %v, want %v", err, ErrStreamIdleTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("idle expiry did not release blocked read")
	}
}

func TestIdleReadCloserResetsAfterSuccessfulRead(t *testing.T) {
	body := &readCloser{Reader: bytes.NewBufferString("ok")}
	var timers []*manualIdleTimer
	reader := newIdleReadCloserWithTimer(context.TODO(), body, 90*time.Second, func(delay time.Duration, callback func()) idleTimer {
		timer := &manualIdleTimer{callback: callback, delay: delay}
		timers = append(timers, timer)
		return timer
	})
	defer reader.Close()

	buffer := make([]byte, 1)
	if n, err := reader.Read(buffer); n != 1 || err != nil {
		t.Fatalf("Read() = %d, %v", n, err)
	}
	if len(timers) != 2 || timers[1].delay != 90*time.Second {
		t.Fatalf("scheduled timers = %#v", timers)
	}
	timers[0].mu.Lock()
	firstStopped := timers[0].stopped
	timers[0].mu.Unlock()
	if !firstStopped {
		t.Fatal("successful read did not stop the previous idle timer")
	}

	// Simulate the stopped timer callback already being in flight when the read
	// rescheduled the watchdog. Its generation must no longer close the body.
	timers[0].callback()
	if n, err := reader.Read(buffer); n != 1 || err != nil || string(buffer[:n]) != "k" {
		t.Fatalf("Read() after stale callback = %d, %v, %q", n, err, buffer[:n])
	}
}

func TestIdleReadCloserStopsOnEOFAndClose(t *testing.T) {
	body := &readCloser{Reader: bytes.NewReader(nil)}
	var timer *manualIdleTimer
	reader := newIdleReadCloserWithTimer(context.TODO(), body, time.Minute, func(delay time.Duration, callback func()) idleTimer {
		timer = &manualIdleTimer{callback: callback, delay: delay}
		return timer
	})

	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read() error = %v, want EOF", err)
	}
	timer.mu.Lock()
	stopped := timer.stopped
	timer.mu.Unlock()
	if !stopped {
		t.Fatal("EOF did not stop idle timer")
	}
	if err := reader.Close(); err != nil || !body.closed {
		t.Fatalf("Close() error = %v closed=%v", err, body.closed)
	}
}

func TestIdleReadCloserContextCancellationIsNotTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := newBlockingReadCloser()
	reader := newIdleReadCloser(ctx, body, time.Hour)
	defer reader.Close()

	result := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		result <- err
	}()
	cancel()

	select {
	case err := <-result:
		if errors.Is(err, ErrStreamIdleTimeout) {
			t.Fatalf("context cancellation reported as idle timeout: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not release blocked read")
	}
}

func TestIdleRoundTripperOnlyWrapsMarkedStreamingRequests(t *testing.T) {
	nonStreamBody := &readCloser{Reader: bytes.NewReader(nil)}
	streamBody := &readCloser{Reader: bytes.NewReader(nil)}
	streamHeader := ""
	transport := idleRoundTripper{
		base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: nonStreamBody}, nil
		}),
		streamBase: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			streamHeader = request.Header.Get(streamingRequestHeader)
			return &http.Response{StatusCode: http.StatusOK, Body: streamBody}, nil
		}),
		timeout: time.Minute,
	}

	nonStreamRequest, err := http.NewRequestWithContext(context.TODO(), http.MethodPost, "https://example.invalid", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	nonStreamResponse, err := transport.RoundTrip(nonStreamRequest)
	if err != nil || nonStreamResponse.Body != nonStreamBody {
		t.Fatalf("non-stream response = %#v, %v", nonStreamResponse, err)
	}

	streamRequest := nonStreamRequest.Clone(context.TODO())
	streamRequest.Header.Set(streamingRequestHeader, "1")
	streamResponse, err := transport.RoundTrip(streamRequest)
	if err != nil {
		t.Fatalf("stream RoundTrip() error = %v", err)
	}
	defer streamResponse.Body.Close()
	if _, ok := streamResponse.Body.(*idleReadCloser); !ok {
		t.Fatalf("stream body = %T, want idleReadCloser", streamResponse.Body)
	}
	if streamHeader != "" {
		t.Fatalf("private streaming header reached base transport: %q", streamHeader)
	}
}

func TestNewHTTPClientConfiguresTransportTimeouts(t *testing.T) {
	config := core.APIConfig{
		ResponseHeaderTimeout: 17 * time.Second,
		StreamReadIdleTimeout: 23 * time.Second,
	}.WithDefaults()
	client := newHTTPClient(config)
	if client.Timeout != 0 {
		t.Fatalf("HTTP client timeout = %s, want no overall timeout", client.Timeout)
	}
	wrapper, ok := client.Transport.(idleRoundTripper)
	if !ok {
		t.Fatalf("transport = %T, want idleRoundTripper", client.Transport)
	}
	if wrapper.timeout != 23*time.Second {
		t.Fatalf("stream idle timeout = %s", wrapper.timeout)
	}
	base, ok := wrapper.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport = %T", wrapper.base)
	}
	if base.ResponseHeaderTimeout == 17*time.Second {
		t.Fatal("non-streaming transport received the streaming header timeout")
	}
	streamBase, ok := wrapper.streamBase.(*http.Transport)
	if !ok {
		t.Fatalf("stream base transport = %T", wrapper.streamBase)
	}
	if streamBase.ResponseHeaderTimeout != 17*time.Second {
		t.Fatalf("response header timeout = %s", streamBase.ResponseHeaderTimeout)
	}
}

func TestClassifyErrorStreamIdleTimeout(t *testing.T) {
	err := errors.New("wrapped: " + ErrStreamIdleTimeout.Error())
	if errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatal("plain text unexpectedly matches sentinel")
	}

	got := ClassifyError(errors.Join(ErrStreamIdleTimeout, errors.New("after 90s")))
	if got.Kind != core.APIErrorTimeout || !got.Retryable || !errors.Is(got, ErrStreamIdleTimeout) {
		t.Fatalf("idle timeout classified as %#v", got)
	}
}
