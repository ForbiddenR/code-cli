package anthropicapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"code-cli/internal/core"
)

var ErrStreamIdleTimeout = errors.New("stream read idle timeout")

type idleTimer interface {
	Stop() bool
}

type idleTimerFactory func(time.Duration, func()) idleTimer

const streamingRequestHeader = "x-code-cli-streaming-request"

type idleRoundTripper struct {
	base       http.RoundTripper
	streamBase http.RoundTripper
	timeout    time.Duration
}

func newHTTPClient(config core.APIConfig) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	streamBase := base.Clone()
	streamBase.ResponseHeaderTimeout = config.ResponseHeaderTimeout
	return &http.Client{
		Transport: idleRoundTripper{
			base:       base,
			streamBase: streamBase,
			timeout:    config.StreamReadIdleTimeout,
		},
	}
}

func (transport idleRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Header.Get(streamingRequestHeader) == "" {
		return transport.base.RoundTrip(request)
	}

	streamRequest := request.Clone(request.Context())
	streamRequest.Header = request.Header.Clone()
	streamRequest.Header.Del(streamingRequestHeader)
	response, err := transport.streamBase.RoundTrip(streamRequest)
	if err != nil || response == nil || response.Body == nil || transport.timeout <= 0 {
		return response, err
	}
	response.Body = newIdleReadCloser(request.Context(), response.Body, transport.timeout)
	return response, nil
}

type idleReadCloser struct {
	body            io.ReadCloser
	timeout         time.Duration
	newTimer        idleTimerFactory
	timer           idleTimer
	timerGeneration uint64
	done            chan struct{}
	doneOnce        sync.Once
	closeOnce       sync.Once
	mu              sync.Mutex
	timedOut        bool
	stopped         bool
}

func newIdleReadCloser(ctx context.Context, body io.ReadCloser, timeout time.Duration) *idleReadCloser {
	return newIdleReadCloserWithTimer(ctx, body, timeout, func(delay time.Duration, callback func()) idleTimer {
		return time.AfterFunc(delay, callback)
	})
}

func newIdleReadCloserWithTimer(
	ctx context.Context,
	body io.ReadCloser,
	timeout time.Duration,
	newTimer idleTimerFactory,
) *idleReadCloser {
	reader := &idleReadCloser{
		body:     body,
		timeout:  timeout,
		newTimer: newTimer,
		done:     make(chan struct{}),
	}
	reader.mu.Lock()
	reader.scheduleTimerLocked()
	reader.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			_ = reader.Close()
		case <-reader.done:
		}
	}()
	return reader
}

func (reader *idleReadCloser) Read(buffer []byte) (int, error) {
	n, err := reader.body.Read(buffer)

	reader.mu.Lock()
	timedOut := reader.timedOut
	if n > 0 && !reader.stopped && !timedOut {
		reader.scheduleTimerLocked()
	}
	reader.mu.Unlock()

	if timedOut {
		return n, fmt.Errorf("%w after %s", ErrStreamIdleTimeout, reader.timeout)
	}
	if errors.Is(err, io.EOF) {
		reader.stop()
	}
	return n, err
}

func (reader *idleReadCloser) Close() error {
	reader.stop()
	var err error
	reader.closeOnce.Do(func() {
		err = reader.body.Close()
	})
	return err
}

func (reader *idleReadCloser) expire(generation uint64) {
	reader.mu.Lock()
	if reader.stopped || generation != reader.timerGeneration {
		reader.mu.Unlock()
		return
	}
	reader.timedOut = true
	reader.stopped = true
	reader.mu.Unlock()

	reader.closeDone()
	reader.closeOnce.Do(func() {
		_ = reader.body.Close()
	})
}

func (reader *idleReadCloser) scheduleTimerLocked() {
	if reader.timer != nil {
		reader.timer.Stop()
	}
	reader.timerGeneration++
	generation := reader.timerGeneration
	reader.timer = reader.newTimer(reader.timeout, func() {
		reader.expire(generation)
	})
}

func (reader *idleReadCloser) stop() {
	reader.mu.Lock()
	if reader.stopped {
		reader.mu.Unlock()
		return
	}
	reader.stopped = true
	reader.timer.Stop()
	reader.mu.Unlock()
	reader.closeDone()
}

func (reader *idleReadCloser) closeDone() {
	reader.doneOnce.Do(func() {
		close(reader.done)
	})
}
