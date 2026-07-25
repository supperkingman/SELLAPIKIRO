package proxy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// parseEventStreamForTest parses a stream with no idle guard, for tests that feed
// an in-memory buffer and so have no request to cancel.
func parseEventStreamForTest(body interface {
	Read([]byte) (int, error)
}, callback *KiroStreamCallback) error {
	return parseEventStream(body, callback, nil)
}

// withStreamTimings shortens the idle/heartbeat windows so tests do not wait the
// production two minutes. Restores the previous values on cleanup.
func withStreamTimings(t *testing.T, idle, beat string) {
	t.Helper()
	oldIdle := os.Getenv("KIRO_STREAM_IDLE_TIMEOUT_SEC")
	oldBeat := os.Getenv("KIRO_STREAM_HEARTBEAT_SEC")
	os.Setenv("KIRO_STREAM_IDLE_TIMEOUT_SEC", idle)
	os.Setenv("KIRO_STREAM_HEARTBEAT_SEC", beat)
	t.Cleanup(func() {
		os.Setenv("KIRO_STREAM_IDLE_TIMEOUT_SEC", oldIdle)
		os.Setenv("KIRO_STREAM_HEARTBEAT_SEC", oldBeat)
	})
}

// serveStream runs an HTTP server that writes whatever the producer sends, then
// returns a live response body plus the guard watching it. A real server is used
// deliberately: the previous version of this watchdog passed tests against a fake
// reader while corrupting real streams, because only a real body reproduces the
// interaction between cancellation and a blocked read.
func serveStream(t *testing.T, onHeartbeat func(), producer func(w http.ResponseWriter, flush func())) (*http.Response, *idleGuard) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		producer(w, func() {
			if flusher != nil {
				flusher.Flush()
			}
		})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	if err != nil {
		cancel()
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("calling test server: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	guard := newIdleGuard(cancel, onHeartbeat)
	t.Cleanup(guard.close)
	return resp, guard
}

// TestIdleGuardTripsOnSilence covers the original bug: upstream starts answering,
// then goes quiet while holding the connection open. The read must be cut loose and
// reported as a stall rather than hanging.
//
// One frame is sent first so this exercises the mid-stream budget; silence before
// any frame is the prefill phase and has its own, much larger budget.
func TestIdleGuardTripsOnSilence(t *testing.T) {
	withStreamTimings(t, "1", "30")
	t.Setenv("KIRO_STREAM_FIRST_FRAME_TIMEOUT_SEC", "60")

	blocked := make(chan struct{})
	resp, guard := serveStream(t, nil, func(w http.ResponseWriter, flush func()) {
		w.WriteHeader(200)
		w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "started",
		}))
		flush()
		<-blocked // then stops sending
	})
	defer close(blocked)

	start := time.Now()
	err := parseEventStream(resp.Body, &KiroStreamCallback{}, guard)
	if !errors.Is(err, errKiroStreamIdle) {
		t.Fatalf("err = %v, want errKiroStreamIdle", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("guard took %v, expected to trip near the 1s idle timeout", elapsed)
	}
}

// TestIdleGuardDoesNotCutSlowButAliveStream is the regression test for the bug
// this rewrite fixes. The stream sends frames steadily but slower than the idle
// timeout would allow if the deadline were not reset by real progress. The broken
// version failed here in the worst way: it left a second reader on the body, so
// frames were split between readers and the content came out corrupted.
func TestIdleGuardDoesNotCutSlowButAliveStream(t *testing.T) {
	withStreamTimings(t, "2", "30")

	const frames = 8
	resp, guard := serveStream(t, nil, func(w http.ResponseWriter, flush func()) {
		w.WriteHeader(200)
		flush()
		for i := 0; i < frames; i++ {
			w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
				"content": "x",
			}))
			flush()
			time.Sleep(300 * time.Millisecond)
		}
	})

	var got string
	err := parseEventStream(resp.Body, &KiroStreamCallback{
		OnText: func(text string, isThinking bool) { got += text },
	}, guard)
	if err != nil {
		t.Fatalf("slow but alive stream failed: %v", err)
	}
	// Every frame must arrive exactly once. A second reader on the body shows up
	// here as missing or garbled content.
	if want := "xxxxxxxx"; got != want {
		t.Fatalf("content = %q, want %q (frames lost or interleaved)", got, want)
	}
}

// TestIdleGuardAllowsLongPrefillBeforeFirstFrame is the regression test for the
// second bug in this watchdog: a slow prefill was judged by the mid-stream idle
// budget and healthy requests were cut. Upstream stays silent while it digests a
// large prompt, then answers normally, so the wait before the first frame gets its
// own much larger budget.
func TestIdleGuardAllowsLongPrefillBeforeFirstFrame(t *testing.T) {
	// Mid-stream budget of 1s, first-frame budget of 30s: a 2s prefill must survive.
	withStreamTimings(t, "1", "30")
	t.Setenv("KIRO_STREAM_FIRST_FRAME_TIMEOUT_SEC", "30")

	resp, guard := serveStream(t, nil, func(w http.ResponseWriter, flush func()) {
		w.WriteHeader(200)
		flush()
		time.Sleep(2 * time.Second) // prefill: longer than the mid-stream budget
		w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "answer",
		}))
		flush()
	})

	var got string
	if err := parseEventStream(resp.Body, &KiroStreamCallback{
		OnText: func(text string, isThinking bool) { got += text },
	}, guard); err != nil {
		t.Fatalf("slow prefill was cut short: %v", err)
	}
	if got != "answer" {
		t.Fatalf("content = %q, want \"answer\"", got)
	}
}

// TestIdleGuardFirstFrameTimeoutReportsItsOwnError checks that a prefill which
// never finishes is still failed, and is reported distinctly from a mid-stream
// stall so the logs say which phase died.
func TestIdleGuardFirstFrameTimeoutReportsItsOwnError(t *testing.T) {
	withStreamTimings(t, "30", "30")
	t.Setenv("KIRO_STREAM_FIRST_FRAME_TIMEOUT_SEC", "1")

	blocked := make(chan struct{})
	resp, guard := serveStream(t, nil, func(w http.ResponseWriter, flush func()) {
		w.WriteHeader(200)
		flush()
		<-blocked // never starts answering
	})
	defer close(blocked)

	err := parseEventStream(resp.Body, &KiroStreamCallback{}, guard)
	if !errors.Is(err, errKiroFirstFrameTimeout) {
		t.Fatalf("err = %v, want errKiroFirstFrameTimeout", err)
	}
	// It must still classify as a retryable stall, or it would not fail over.
	if !isStreamStallOrAbortMessage(err.Error()) {
		t.Error("first-frame timeout should classify as a stall")
	}
	if !isTransientThrottleMessage(err.Error()) {
		t.Error("first-frame timeout should get the short cooldown, not a ban")
	}
}

// TestIdleGuardTightensAfterFirstFrame confirms the switch actually happens: once
// a frame has arrived, a silence beyond the mid-stream budget is a stall even
// though the first-frame budget is far from exhausted.
func TestIdleGuardTightensAfterFirstFrame(t *testing.T) {
	withStreamTimings(t, "1", "30")
	t.Setenv("KIRO_STREAM_FIRST_FRAME_TIMEOUT_SEC", "120")

	blocked := make(chan struct{})
	resp, guard := serveStream(t, nil, func(w http.ResponseWriter, flush func()) {
		w.WriteHeader(200)
		w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "partial",
		}))
		flush()
		<-blocked // goes quiet after starting
	})
	defer close(blocked)

	start := time.Now()
	err := parseEventStream(resp.Body, &KiroStreamCallback{}, guard)
	if err == nil {
		t.Fatal("expected a mid-stream stall to be reported")
	}
	// Must be the mid-stream error, not the first-frame one.
	if errors.Is(err, errKiroFirstFrameTimeout) {
		t.Errorf("reported a first-frame timeout after a frame had arrived: %v", err)
	}
	// And it must not have waited for the 120s first-frame budget.
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("took %v; the mid-stream budget was not applied after the first frame", elapsed)
	}
}

// TestIdleGuardHeartbeatsDoNotExtendDeadline pins the rule that keepalives are for
// intermediaries only. If a heartbeat reset the deadline, a dead upstream would be
// kept alive forever and never fail over.
func TestIdleGuardHeartbeatsDoNotExtendDeadline(t *testing.T) {
	withStreamTimings(t, "2", "1")
	t.Setenv("KIRO_STREAM_FIRST_FRAME_TIMEOUT_SEC", "60")

	var beats int32
	blocked := make(chan struct{})
	resp, guard := serveStream(t, func() { atomic.AddInt32(&beats, 1) },
		func(w http.ResponseWriter, flush func()) {
			w.WriteHeader(200)
			// Enter the mid-stream phase, where the short budget applies.
			w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
				"content": "started",
			}))
			flush()
			<-blocked
		})
	defer close(blocked)

	err := parseEventStream(resp.Body, &KiroStreamCallback{}, guard)
	if !errors.Is(err, errKiroStreamIdle) {
		t.Fatalf("err = %v, want errKiroStreamIdle despite heartbeats", err)
	}
	if got := atomic.LoadInt32(&beats); got < 1 {
		t.Fatalf("heartbeat fired %d times, want at least 1 while stalled", got)
	}
}

// TestIdleGuardPassesThroughHealthyStream confirms the guard is invisible when the
// stream behaves, including that it does not turn a clean end into an error.
func TestIdleGuardPassesThroughHealthyStream(t *testing.T) {
	withStreamTimings(t, "30", "10")

	resp, guard := serveStream(t, nil, func(w http.ResponseWriter, flush func()) {
		w.WriteHeader(200)
		w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "hello",
		}))
		flush()
	})

	var got string
	if err := parseEventStream(resp.Body, &KiroStreamCallback{
		OnText: func(text string, isThinking bool) { got += text },
	}, guard); err != nil {
		t.Fatalf("healthy stream failed: %v", err)
	}
	if got != "hello" {
		t.Fatalf("content = %q, want \"hello\"", got)
	}
}

// TestIdleGuardTranslateErrLeavesOtherErrorsAlone makes sure only the
// cancellation this guard caused is relabelled as a stall. Misreporting an
// unrelated error as a stall would send it down the wrong failover path.
func TestIdleGuardTranslateErrLeavesOtherErrorsAlone(t *testing.T) {
	g := &idleGuard{}

	other := errors.New("HTTP 401 unauthorized")
	if got := g.translateErr(other); got != other {
		t.Errorf("untripped guard rewrote %v to %v", other, got)
	}
	if got := g.translateErr(nil); got != nil {
		t.Errorf("nil error became %v", got)
	}

	// Once tripped, a cancellation becomes the stall error, but anything else does
	// not: the stream may have failed for its own reasons at the same moment.
	g.tripped.Store(true)
	if got := g.translateErr(context.Canceled); !errors.Is(got, errKiroStreamIdle) {
		t.Errorf("tripped guard returned %v, want errKiroStreamIdle", got)
	}
	if got := g.translateErr(other); got != other {
		t.Errorf("tripped guard rewrote unrelated error %v to %v", other, got)
	}

	// A nil guard is the no-watchdog case and must be safe to call.
	var nilGuard *idleGuard
	if got := nilGuard.translateErr(other); got != other {
		t.Errorf("nil guard rewrote %v to %v", other, got)
	}
}

// TestIdleGuardNilIsSafe covers the non-streaming callers that pass no guard.
func TestIdleGuardNilIsSafe(t *testing.T) {
	var g *idleGuard
	g.noteActivity()
	g.close()

	stream := bytes.NewReader(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
		"content": "ok",
	}))
	if err := parseEventStream(stream, &KiroStreamCallback{}, nil); err != nil {
		t.Fatalf("parsing without a guard failed: %v", err)
	}
}

// TestParseEventStreamRejectsContentlessStream covers upstream's soft-throttle
// shape: a clean HTTP 200 carrying only bookkeeping frames. Reporting that as
// success produced a completion with zero tokens and no text, which agent clients
// read as an empty response and retried with the identical payload.
func TestParseEventStreamRejectsContentlessStream(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(awsEventStreamFrame(t, "meteringEvent", map[string]interface{}{"usage": 1.0}))
	buf.Write(awsEventStreamFrame(t, "contextUsageEvent", map[string]interface{}{
		"contextUsagePercentage": 12.5,
	}))

	completed := false
	err := parseEventStreamForTest(bytes.NewReader(buf.Bytes()), &KiroStreamCallback{
		OnComplete: func(int, int) { completed = true },
	})
	if !errors.Is(err, errKiroEmptyStream) {
		t.Fatalf("err = %v, want errKiroEmptyStream", err)
	}
	// OnComplete must stay unfired: it would both surface an empty message to the
	// client and record a false success against the account.
	if completed {
		t.Fatal("OnComplete fired for a contentless stream")
	}
}

// TestParseEventStreamAcceptsToolOnlyStream guards the boundary: a reply made up
// purely of tool calls is legitimate output and must not be mistaken for empty.
func TestParseEventStreamAcceptsToolOnlyStream(t *testing.T) {
	stream := bytes.NewReader(awsEventStreamFrame(t, "toolUseEvent", map[string]interface{}{
		"toolUseId": "toolu_1",
		"name":      "read_file",
		"input":     "{}",
		"stop":      true,
	}))

	if err := parseEventStreamForTest(stream, &KiroStreamCallback{}); err != nil {
		t.Fatalf("tool-only stream rejected: %v", err)
	}
}

// TestIsStreamStallOrAbortMessage pins the classifier. The exclusions matter most:
// quota and auth errors need their own cooldown or ban, and retrying them as
// stalls would walk the failure across the whole account pool for nothing.
func TestIsStreamStallOrAbortMessage(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{errKiroEmptyStream.Error(), true},
		{errKiroStreamIdle.Error(), true},
		{"unexpected EOF", true},
		{"read tcp 1.2.3.4:443: connection reset by peer", true},
		{"write tcp: broken pipe", true},
		// Errors with dedicated handling must never be read as stalls.
		{"HTTP 429 quota exceeded", false},
		{"HTTP 401 unauthorized", false},
		{"HTTP 402 overage limit reached", false},
		{"account TEMPORARILY_SUSPENDED", false},
		{"no available kiro profile", false},
		{"HTTP 400 validation error", false},
	}
	for _, c := range cases {
		if got := isStreamStallOrAbortMessage(c.msg); got != c.want {
			t.Errorf("isStreamStallOrAbortMessage(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestIsTransientThrottleMessage checks the narrower marker that drives the short
// cooldown, so an ordinary transport abort does not silently benefit from it.
func TestIsTransientThrottleMessage(t *testing.T) {
	if !isTransientThrottleMessage(errKiroEmptyStream.Error()) {
		t.Error("empty-stream error should be a transient throttle")
	}
	if !isTransientThrottleMessage(errKiroStreamIdle.Error()) {
		t.Error("idle-stall error should be a transient throttle")
	}
	if isTransientThrottleMessage("unexpected EOF") {
		t.Error("a plain transport abort is not a transient throttle")
	}
}
