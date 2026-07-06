package anthropicapi

import (
	"context"
	"sync"

	"code-cli/internal/core"

	"github.com/anthropics/anthropic-sdk-go"
)

type sdkStreamReader interface {
	Next() bool
	Current() anthropic.MessageStreamEventUnion
	Err() error
	Close() error
}

type sdkStream struct {
	ctx       context.Context
	cancel    context.CancelFunc
	stream    sdkStreamReader
	events    chan StreamEvent
	closeOnce sync.Once
}

func newSDKStream(ctx context.Context, cancel context.CancelFunc, stream sdkStreamReader) *sdkStream {
	s := &sdkStream{
		ctx:    ctx,
		cancel: cancel,
		stream: stream,
		events: make(chan StreamEvent, 16),
	}
	go s.pump()
	return s
}

func (s *sdkStream) Events() <-chan StreamEvent {
	return s.events
}

func (s *sdkStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		err = s.stream.Close()
	})
	return err
}

func (s *sdkStream) pump() {
	defer close(s.events)
	defer s.cancel()

	for s.stream.Next() {
		event, err := normalizeStreamEvent(s.stream.Current())
		if err != nil {
			s.send(StreamEvent{Type: StreamEventError, Error: err})
			continue
		}
		s.send(event)
	}
	if err := s.stream.Err(); err != nil && s.ctx.Err() == nil {
		s.send(StreamEvent{Type: StreamEventError, Error: ClassifyError(err)})
	}
}

func (s *sdkStream) send(event StreamEvent) {
	select {
	case <-s.ctx.Done():
	case s.events <- event:
	}
}

func normalizeStreamEvent(event anthropic.MessageStreamEventUnion) (StreamEvent, error) {
	switch event.Type {
	case "message_start":
		message, err := normalizeMessage(&event.Message)
		if err != nil {
			return StreamEvent{}, err
		}
		return StreamEvent{Type: StreamEventMessageStart, Message: message}, nil
	case "content_block_start":
		block, err := normalizeStreamContentBlock(event.ContentBlock)
		if err != nil {
			return StreamEvent{}, err
		}
		return StreamEvent{Type: StreamEventContentBlockStart, Index: int(event.Index), Block: &block}, nil
	case "content_block_delta":
		delta := ContentDelta{
			Type:        event.Delta.Type,
			Text:        event.Delta.Text,
			PartialJSON: event.Delta.PartialJSON,
			Thinking:    event.Delta.Thinking,
			Signature:   event.Delta.Signature,
		}
		return StreamEvent{Type: StreamEventContentBlockDelta, Index: int(event.Index), Delta: &delta}, nil
	case "content_block_stop":
		return StreamEvent{Type: StreamEventContentBlockStop, Index: int(event.Index)}, nil
	case "message_delta":
		messageDelta := MessageDelta{
			StopReason:   coreStopReason(event.Delta.StopReason),
			StopSequence: event.Delta.StopSequence,
		}
		usage := normalizeDeltaUsage(event.Usage)
		return StreamEvent{Type: StreamEventMessageDelta, MessageDelta: &messageDelta, Usage: &usage}, nil
	case "message_stop":
		return StreamEvent{Type: StreamEventMessageStop}, nil
	default:
		return StreamEvent{Type: StreamEventType(event.Type)}, nil
	}
}

func coreStopReason(reason anthropic.StopReason) core.StopReason {
	return core.StopReason(reason)
}
