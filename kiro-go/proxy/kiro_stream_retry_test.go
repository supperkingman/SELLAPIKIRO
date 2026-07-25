package proxy

import (
	"bytes"
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

// TestEmptyStreamIsNotRetriedAcrossEndpoints pins the retry scope.
//
// An empty stream is a deterministic refusal of the request itself, so every
// endpoint refuses it identically and retrying only multiplies the failures by the
// endpoint count while adding backoff to the client's wait. Field data showed
// roughly three logged failures per request before this was narrowed. A dropped or
// stalled connection is a property of the connection instead, so those stay
// retryable.
func TestEmptyStreamIsNotRetriedAcrossEndpoints(t *testing.T) {
	if !errors.Is(errKiroEmptyStream, errKiroEmptyStream) {
		t.Fatal("sanity: sentinel must match itself")
	}

	// Deterministic refusal: must not be retried.
	if retryableStreamFailure(errKiroEmptyStream) {
		t.Error("an empty stream must not be retried on another endpoint")
	}
	if retryableStreamFailure(fmt.Errorf("account kr79: %w", errKiroEmptyStream)) {
		t.Error("a wrapped empty stream must not be retried either")
	}

	// Connection-level failures: another endpoint has a real chance.
	for _, err := range []error{
		io.ErrUnexpectedEOF,
		errKiroStreamIdle,
		errKiroFirstFrameTimeout,
		errors.New("connection reset by peer"),
	} {
		if !retryableStreamFailure(err) {
			t.Errorf("expected %v to stay retryable", err)
		}
	}
}

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
