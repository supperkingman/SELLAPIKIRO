package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

// TestParseEventStreamTrackedReportsEmission covers the distinction the endpoint
// retry depends on. Retrying is only safe while nothing has been emitted, so a
// wrong answer here either duplicates client-visible output or fails a request
// that was recoverable.
func TestParseEventStreamTrackedReportsEmission(t *testing.T) {
	t.Run("text frame means emitted", func(t *testing.T) {
		body := awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "hello",
		})
		emitted, err := parseEventStreamTracked(bytes.NewReader(body), &KiroStreamCallback{}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !emitted {
			t.Error("a stream carrying text must report emitted=true")
		}
	})

	t.Run("metadata only means nothing emitted", func(t *testing.T) {
		// The shape observed in production: upstream reports context usage and closes
		// without generating a token. Nothing reached the client, so the request is
		// safe to retry on another endpoint.
		body := awsEventStreamFrame(t, "contextUsageEvent", map[string]interface{}{
			"contextUsagePercentage": 15.7,
		})
		emitted, err := parseEventStreamTracked(bytes.NewReader(body), &KiroStreamCallback{}, nil)
		if !errors.Is(err, errKiroEmptyStream) {
			t.Fatalf("expected errKiroEmptyStream, got %v", err)
		}
		if emitted {
			t.Error("a metadata-only stream must report emitted=false so it can be retried")
		}
	})

	t.Run("tool use counts as emitted", func(t *testing.T) {
		// A tool call is client-visible output even though it carries no text, so a
		// retry after one would duplicate the call.
		body := awsEventStreamFrame(t, "toolUseEvent", map[string]interface{}{
			"toolUseId": "t1",
			"name":      "read_file",
			"input":     "{}",
			"stop":      true,
		})
		emitted, _ := parseEventStreamTracked(bytes.NewReader(body), &KiroStreamCallback{}, nil)
		if !emitted {
			t.Error("a tool use must report emitted=true")
		}
	})

	t.Run("stream cut after content still reports emitted", func(t *testing.T) {
		// Bytes already went out, so the caller must surface the error instead of
		// retrying and concatenating a second partial answer.
		body := awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "partial",
		})
		truncated := append(body, 0x00, 0x00, 0x01) // half a prelude
		emitted, _ := parseEventStreamTracked(bytes.NewReader(truncated), &KiroStreamCallback{}, nil)
		if !emitted {
			t.Error("a stream cut after content must still report emitted=true")
		}
	})
}

// TestIsRetryableStreamError pins which no-output failures earn another attempt.
//
// A second attempt is refused where it cannot plausibly do better: a cancelled
// context was aborted deliberately, and a timeout has already spent its full
// waiting budget, so retrying would only double the client's wait for the same
// outcome. Our own stall sentinels are timeouts in that sense too.
//
// An empty stream is retryable. It was briefly excluded after field data showed it
// repeating, but that data came from retrying a DIFFERENT endpoint two seconds
// later; the retry now goes to the same endpoint after a short pause, which is a
// materially different attempt.
func TestIsRetryableStreamError(t *testing.T) {
	retryable := []error{
		errKiroEmptyStream,
		fmt.Errorf("account kr79: %w", errKiroEmptyStream),
		io.ErrUnexpectedEOF,
		errors.New("connection reset by peer"),
	}
	for _, err := range retryable {
		if !isRetryableStreamError(err) {
			t.Errorf("expected %v to be retryable", err)
		}
	}

	notRetryable := []error{
		nil,
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("wrapped: %w", context.Canceled),
		errKiroStreamIdle,
		errKiroFirstFrameTimeout,
		timeoutError{},
	}
	for _, err := range notRetryable {
		if isRetryableStreamError(err) {
			t.Errorf("expected %v to not be retryable", err)
		}
	}
}

// timeoutError stands in for a transport timeout, which reports itself through
// net.Error rather than through a sentinel value.
type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }
func (timeoutError) Temporary() bool { return true }

// TestParseEventStreamWrapperUnchanged pins that the original entry point behaves
// as before, since non-streaming callers and existing tests still use it.
func TestParseEventStreamWrapperUnchanged(t *testing.T) {
	body := awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
		"content": "hi",
	})
	var got string
	err := parseEventStream(bytes.NewReader(body), &KiroStreamCallback{
		OnText: func(s string, _ bool) { got += s },
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hi" {
		t.Errorf("text = %q, want %q", got, "hi")
	}
}
