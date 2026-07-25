package proxy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"kiro-go/config"
)

// withEffortFormat turns the effort wire format on for one test and clears it
// afterwards. Needed because the shipped default is "off".
//
// This sets the in-memory override rather than calling UpdateEffortConfig, which
// persists to disk: there is no config file in a unit test, and a stored value
// would leak into every test that runs after this one.
func withEffortFormat(t *testing.T, format string) {
	t.Helper()
	effortFormatOverride.Store(&format)
	t.Cleanup(func() { effortFormatOverride.Store(nil) })
}

// TestStripEffortSuffix is the highest-risk piece of this feature: every other
// consumer matches on the model name, so a suffix left attached becomes an unknown
// model id upstream, and a name wrongly truncated routes to the wrong model.
func TestStripEffortSuffix(t *testing.T) {
	cases := []struct {
		in        string
		wantModel string
		wantLevel string
	}{
		{"claude-opus-5-max", "claude-opus-5", effortMax},
		{"claude-opus-5-xhigh", "claude-opus-5", effortXHigh},
		{"claude-opus-5-high", "claude-opus-5", effortHigh},
		{"claude-opus-5-medium", "claude-opus-5", effortMedium},
		{"claude-opus-5-low", "claude-opus-5", effortLow},
		// Thinking and effort must compose: a customer should not have to give up
		// extended thinking to pick an effort level.
		{"claude-opus-5-thinking-max", "claude-opus-5-thinking", effortMax},
		// Hyphenless spellings users type even though we never advertise them.
		{"claude-opus-5-x-high", "claude-opus-5", effortXHigh},
		// No suffix: returned untouched.
		{"claude-opus-5", "claude-opus-5", ""},
		{"claude-sonnet-4.6", "claude-sonnet-4.6", ""},
		// A version number ending in a digit must not be mistaken for a level.
		{"claude-opus-4.8", "claude-opus-4.8", ""},
	}
	for _, c := range cases {
		gotModel, gotLevel := stripEffortSuffix(c.in)
		if gotModel != c.wantModel || gotLevel != c.wantLevel {
			t.Errorf("stripEffortSuffix(%q) = (%q, %q), want (%q, %q)",
				c.in, gotModel, gotLevel, c.wantModel, c.wantLevel)
		}
	}
}

// TestStripEffortSuffixThenParseModel proves the ordering requirement: the bare
// name coming out of stripEffortSuffix still resolves to a real Kiro model. If the
// suffix were stripped later, this is the comparison that would fail.
func TestStripEffortSuffixThenParseModel(t *testing.T) {
	base, level := stripEffortSuffix("claude-opus-4-8-max")
	if level != effortMax {
		t.Fatalf("level = %q, want max", level)
	}
	resolved, thinking := ParseModelAndThinking(base, "-thinking")
	if thinking {
		t.Error("thinking should be false without the thinking suffix")
	}
	// The dashed form must still normalize to the dotted Kiro id.
	if !strings.Contains(resolved, "4.8") {
		t.Errorf("resolved = %q, want the normalized 4.8 form", resolved)
	}
}

// TestNormalizeEffort covers the aliases. "minimal" and "none" collapse to low
// rather than being dropped: dropping them would fall through to the default,
// which is max, i.e. the opposite of what the client asked for.
func TestNormalizeEffort(t *testing.T) {
	cases := map[string]string{
		"max": effortMax, "MAX": effortMax, " max ": effortMax,
		"maximum": effortMax, "ultra": effortMax,
		"xhigh": effortXHigh, "x-high": effortXHigh, "very-high": effortXHigh,
		"high": effortHigh, "medium": effortMedium, "low": effortLow,
		"minimal": effortLow, "none": effortLow,
		"bogus": "", "": "",
	}
	for in, want := range cases {
		if got := normalizeEffort(in); got != want {
			t.Errorf("normalizeEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveEffortPriority pins the documented precedence: model suffix beats a
// native body hint, which beats the configured default.
func TestResolveEffortPriority(t *testing.T) {
	// The default format is off, so enable it: this test is about priority between
	// the three sources, not about whether the feature is switched on.
	withEffortFormat(t, "reasoning")

	// Suffix wins over a conflicting native hint.
	if lvl, ok := resolveEffort("claude-opus-5", effortLow, "max"); !ok || lvl != effortLow {
		t.Errorf("suffix should win: got (%q, %v), want (low, true)", lvl, ok)
	}
	// Native hint applies when there is no suffix.
	if lvl, ok := resolveEffort("claude-opus-5", "", "high"); !ok || lvl != effortHigh {
		t.Errorf("native hint should apply: got (%q, %v), want (high, true)", lvl, ok)
	}
	// Neither present: falls back to the configured default, max by default.
	if lvl, ok := resolveEffort("claude-opus-5", "", ""); !ok || lvl != effortMax {
		t.Errorf("default should apply: got (%q, %v), want (max, true)", lvl, ok)
	}
}

// TestResolveEffortSkipsNonClaudeModels guards the disguise path: a Claude request
// cascaded to Grok or Codex must not carry a field those APIs do not define.
func TestResolveEffortSkipsNonClaudeModels(t *testing.T) {
	for _, model := range []string{"grok-4.5", "gpt-4o", "gpt-5.6-sol", ""} {
		if _, ok := resolveEffort(model, effortMax, ""); ok {
			t.Errorf("effort should not be attached to %q", model)
		}
	}
}

// TestApplyEffortToPayloadReasoningShape checks the wire shape and, importantly,
// that a request without effort is left byte-identical to before this feature.
func TestApplyEffortToPayloadReasoningShape(t *testing.T) {
	withEffortFormat(t, "reasoning")

	payload := &KiroPayload{}
	applyEffortToPayload(payload, effortHigh)

	fields := payload.ConversationState.CurrentMessage.UserInputMessage.AdditionalModelRequestFields
	reasoning, ok := fields["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("reasoning block missing, got %#v", fields)
	}
	if reasoning["effort"] != effortHigh {
		t.Errorf("effort = %v, want high", reasoning["effort"])
	}
	if payload.EffortLevel != effortHigh {
		t.Errorf("EffortLevel = %q, want high", payload.EffortLevel)
	}

	// An empty level must be a no-op, so requests that never asked for effort keep
	// exactly the body they had before.
	untouched := &KiroPayload{}
	applyEffortToPayload(untouched, "")
	if untouched.ConversationState.CurrentMessage.UserInputMessage.AdditionalModelRequestFields != nil {
		t.Error("empty level should not attach any fields")
	}
}

// TestReduceEffortWalksDown covers the ladder used when a stream keeps getting cut
// off, including the bottom rung where there is nothing left to give up.
func TestReduceEffortWalksDown(t *testing.T) {
	steps := []struct{ from, want string }{
		{effortMax, effortXHigh},
		{effortXHigh, effortHigh},
		{effortHigh, effortMedium},
		{effortMedium, effortLow},
		{effortLow, ""},
	}
	for _, s := range steps {
		if got := reduceEffort(s.from); got != s.want {
			t.Errorf("reduceEffort(%q) = %q, want %q", s.from, got, s.want)
		}
	}
}

// TestReduceEffortOnPayload verifies the in-place step down and that it reports
// failure at the bottom, so a caller cannot loop forever lowering effort.
func TestReduceEffortOnPayload(t *testing.T) {
	withEffortFormat(t, "reasoning")

	payload := &KiroPayload{}
	applyEffortToPayload(payload, effortMax)

	from, to, ok := reduceEffortOnPayload(payload)
	if !ok || from != effortMax || to != effortXHigh {
		t.Fatalf("got (%q, %q, %v), want (max, xhigh, true)", from, to, ok)
	}
	if payload.EffortLevel != effortXHigh {
		t.Errorf("payload level = %q, want xhigh", payload.EffortLevel)
	}

	applyEffortToPayload(payload, effortLow)
	if _, _, ok := reduceEffortOnPayload(payload); ok {
		t.Error("reducing below low should report failure")
	}

	// No effort attached at all: nothing to reduce.
	if _, _, ok := reduceEffortOnPayload(&KiroPayload{}); ok {
		t.Error("a payload without effort should report failure")
	}
}

// TestStripEffortFromPayload covers the retry-without-effort path.
func TestStripEffortFromPayload(t *testing.T) {
	payload := &KiroPayload{}
	applyEffortToPayload(payload, effortMax)
	stripEffortFromPayload(payload)

	if payload.ConversationState.CurrentMessage.UserInputMessage.AdditionalModelRequestFields != nil {
		t.Error("effort fields should be gone")
	}
	if payload.EffortLevel != "" {
		t.Errorf("EffortLevel = %q, want empty", payload.EffortLevel)
	}
}

// TestIsEffortRejection400 checks the retry trigger. A 400 unrelated to our added
// fields must not trigger a pointless retry, and a genuine schema complaint must.
func TestIsEffortRejection400(t *testing.T) {
	shouldRetry := []string{
		"HTTP 400: unexpected field additionalModelRequestFields",
		"HTTP 400 validation error: output_config not supported",
		"invalid request: unknown field reasoning.effort",
		"HTTP 400 unsupported parameter: thinking",
	}
	for _, msg := range shouldRetry {
		if !isEffortRejection400(msg) {
			t.Errorf("isEffortRejection400(%q) = false, want true", msg)
		}
	}

	shouldNot := []string{
		"HTTP 400 CONTENT_LENGTH_EXCEEDS_THRESHOLD",
		"HTTP 400: malformed tool schema",
		"HTTP 429 quota exceeded",
		"upstream returned empty stream (HTTP 200): transient throttle/stall",
	}
	for _, msg := range shouldNot {
		if isEffortRejection400(msg) {
			t.Errorf("isEffortRejection400(%q) = true, want false", msg)
		}
	}
}

// TestIsEffortRejectionCoversEmptyStream is the regression test for the failure
// that reached production. Upstream does not reject an unrecognised effort shape
// with a 400; it answers HTTP 200 and closes the stream with no frames. The
// original safety net only looked for a 400, so it never fired and every Claude
// request broke instead.
func TestIsEffortRejectionCoversEmptyStream(t *testing.T) {
	if !isEffortRejection(errKiroEmptyStream) {
		t.Error("an empty stream must be treated as a possible effort rejection")
	}
	// Wrapped, as it arrives through the call stack.
	if !isEffortRejection(fmt.Errorf("account kr77: %w", errKiroEmptyStream)) {
		t.Error("a wrapped empty stream must still be recognised")
	}
	// The 400 forms still count.
	if !isEffortRejection(errors.New("HTTP 400: unexpected field additionalModelRequestFields")) {
		t.Error("a 400 naming our fields must still be recognised")
	}
	// Unrelated failures must not trigger a retry: retrying an auth or quota error
	// without effort just wastes a request and delays failover.
	for _, msg := range []string{
		"HTTP 401 unauthorized",
		"HTTP 429 quota exhausted",
		"HTTP 400 CONTENT_LENGTH_EXCEEDS_THRESHOLD",
	} {
		if isEffortRejection(errors.New(msg)) {
			t.Errorf("isEffortRejection(%q) = true, want false", msg)
		}
	}
	if isEffortRejection(nil) {
		t.Error("nil must not be treated as a rejection")
	}
}

// TestEffortDefaultsToOff pins the safer default. Effort must not alter requests
// until an operator has confirmed a format upstream accepts, because a wrong guess
// fails silently rather than loudly.
func TestEffortDefaultsToOff(t *testing.T) {
	if got := config.GetEffortConfig().EffortFormat; got != "off" {
		t.Errorf("default effortFormat = %q, want \"off\"", got)
	}
	// With the format off, nothing should be attached even at max.
	if _, enabled := resolveEffort("claude-opus-5", effortMax, ""); enabled {
		t.Error("effort must not be applied while the format is off")
	}
}

// TestBuildEffortVariants confirms customers are offered every level, in both the
// plain and thinking forms, and that the switch actually switches it off.
func TestBuildEffortVariants(t *testing.T) {
	variants := buildEffortVariants("claude-opus-5", "-thinking", true, true)
	if len(variants) != len(EffortLevels())*2 {
		t.Fatalf("got %d variants, want %d", len(variants), len(EffortLevels())*2)
	}

	ids := make(map[string]bool, len(variants))
	for _, v := range variants {
		if id, ok := v["id"].(string); ok {
			ids[id] = true
		}
	}
	for _, want := range []string{
		"claude-opus-5-max", "claude-opus-5-low",
		"claude-opus-5-thinking-max", "claude-opus-5-thinking-low",
	} {
		if !ids[want] {
			t.Errorf("missing advertised variant %q", want)
		}
	}

	if got := buildEffortVariants("claude-opus-5", "-thinking", true, false); got != nil {
		t.Errorf("disabled exposure should advertise nothing, got %d", len(got))
	}
}
