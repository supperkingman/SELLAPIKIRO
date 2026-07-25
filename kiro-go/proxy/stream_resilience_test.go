package proxy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// blockingReader delivers its payload, then blocks forever, simulating an
// upstream that accepts a request and then goes silent mid-stream.
type blockingReader struct {
	data    []byte
	pos     int
	release chan struct{}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.pos < len(r.data) {
		n := copy(p, r.data[r.pos:])
		r.pos += n
		return n, nil
	}
	<-r.release
	return 0, io.EOF
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

// TestReadFullWithIdleTimeoutTripsOnSilence covers the case that previously hung:
// upstream stops sending bytes but keeps the connection open. Without the
// watchdog the read blocks until some network layer happens to drop it.
func TestReadFullWithIdleTimeoutTripsOnSilence(t *testing.T) {
	withStreamTimings(t, "1", "10")

	release := make(chan struct{})
	defer close(release)
	r := &blockingReader{release: release}

	start := time.Now()
	_, err := readFullWithIdleTimeout(r, make([]byte, 12), nil)
	if !errors.Is(err, errKiroStreamIdle) {
		t.Fatalf("err = %v, want errKiroStreamIdle", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("watchdog took %v, expected to trip near the 1s idle timeout", elapsed)
	}
}

// TestReadFullWithIdleTimeoutHeartbeatsWhileBlocked verifies keepalives are
// emitted during a stall, and that they do NOT extend the idle deadline: a
// genuinely dead stream must still fail so the request can fail over.
func TestReadFullWithIdleTimeoutHeartbeatsWhileBlocked(t *testing.T) {
	withStreamTimings(t, "2", "1")

	release := make(chan struct{})
	defer close(release)
	r := &blockingReader{release: release}

	var beats int32
	_, err := readFullWithIdleTimeout(r, make([]byte, 12), func() {
		atomic.AddInt32(&beats, 1)
	})
	if !errors.Is(err, errKiroStreamIdle) {
		t.Fatalf("err = %v, want errKiroStreamIdle despite heartbeats", err)
	}
	if got := atomic.LoadInt32(&beats); got < 1 {
		t.Fatalf("heartbeat fired %d times, want at least 1 while blocked", got)
	}
}

// TestReadFullWithIdleTimeoutPassesThroughData confirms the watchdog is
// transparent on a healthy read.
func TestReadFullWithIdleTimeoutPassesThroughData(t *testing.T) {
	withStreamTimings(t, "30", "10")

	want := []byte("twelve bytes")
	buf := make([]byte, len(want))
	n, err := readFullWithIdleTimeout(bytes.NewReader(want), buf, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(want) || !bytes.Equal(buf, want) {
		t.Fatalf("read %d bytes %q, want %d bytes %q", n, buf, len(want), want)
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
	err := parseEventStream(bytes.NewReader(buf.Bytes()), &KiroStreamCallback{
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

	if err := parseEventStream(stream, &KiroStreamCallback{}); err != nil {
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
