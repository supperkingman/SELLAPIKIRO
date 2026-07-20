package proxy

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestResolveCodexModel(t *testing.T) {
	tests := []struct {
		name       string
		client     string
		wantModel  string
		wantEffort string
	}{
		{name: "bare model defaults high", client: "gpt-5.6-sol", wantModel: "gpt-5.6-sol", wantEffort: "high"},
		{name: "cx prefix", client: "cx/gpt-5.6-luna-low", wantModel: "gpt-5.6-luna", wantEffort: "low"},
		{name: "codex slash prefix", client: "codex/gpt-5.6-terra-medium", wantModel: "gpt-5.6-terra", wantEffort: "medium"},
		{name: "codex dash prefix", client: "codex-gpt-5.6-sol-xhigh", wantModel: "gpt-5.6-sol", wantEffort: "xhigh"},
		{name: "thinking alias", client: "gpt-5.6-sol-thinking", wantModel: "gpt-5.6-sol", wantEffort: "xhigh"},
		{name: "thinking before explicit effort", client: "gpt-5.6-sol-thinking-high", wantModel: "gpt-5.6-sol", wantEffort: "high"},
		{name: "thinking after explicit effort", client: "gpt-5.6-sol-high-thinking", wantModel: "gpt-5.6-sol", wantEffort: "high"},
		{name: "max tier", client: "gpt-5.6-sol-max", wantModel: "gpt-5.6-sol", wantEffort: "max"},
		// Codex rejects effort=minimal (HTTP 400); the -minimal alias must resolve
		// to the nearest supported tier "low" so the request still succeeds.
		{name: "minimal maps to low", client: "gpt-5.6-sol-minimal", wantModel: "gpt-5.6-sol", wantEffort: "low"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model, effort := ResolveCodexModel(tc.client)
			if model != tc.wantModel || effort != tc.wantEffort {
				t.Fatalf("ResolveCodexModel(%q) = (%q, %q), want (%q, %q)", tc.client, model, effort, tc.wantModel, tc.wantEffort)
			}
		})
	}
}

// TestClampCodexEffort guards the single choke point that prevents an
// unsupported reasoning.effort from reaching ChatGPT Codex (which would 400 and
// drop the account from rotation). Live probing confirmed none/low/medium/high/
// xhigh/max are accepted and "minimal" is not.
func TestClampCodexEffort(t *testing.T) {
	cases := map[string]string{
		"none":    "none",
		"minimal": "low",
		"low":     "low",
		"medium":  "medium",
		"high":    "high",
		"xhigh":   "xhigh",
		"max":     "max",
		"MAX":     "max",
		" high ":  "high",
		"":        "high",
		"bogus":   "high",
	}
	for in, want := range cases {
		if got := clampCodexEffort(in); got != want {
			t.Errorf("clampCodexEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseCodexRateLimit verifies the x-codex-* usage headers are parsed and
// that exhaustion / reset-window logic matches what the live endpoint reports.
func TestParseCodexRateLimit(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "100")
	h.Set("x-codex-secondary-used-percent", "0")
	h.Set("x-codex-primary-reset-after-seconds", "557411")
	h.Set("x-codex-credits-has-credits", "False")
	h.Set("x-codex-credits-unlimited", "False")
	rl := parseCodexRateLimit(h)
	if !rl.present {
		t.Fatal("expected present=true")
	}
	if !rl.exhausted() {
		t.Fatal("expected exhausted=true at 100% used")
	}
	// cooldown should track the real reset window (~6.4 days), clamped <= 24h.
	if rl.cooldownFor() != 24*time.Hour {
		t.Fatalf("expected cooldown clamped to 24h, got %s", rl.cooldownFor())
	}

	// Healthy account: low usage, has credits.
	h2 := http.Header{}
	h2.Set("x-codex-primary-used-percent", "12")
	h2.Set("x-codex-credits-has-credits", "True")
	rl2 := parseCodexRateLimit(h2)
	if rl2.exhausted() {
		t.Fatal("expected not exhausted at 12% used")
	}

	// No headers at all: must not report exhausted (avoid false cooldown).
	if parseCodexRateLimit(http.Header{}).exhausted() {
		t.Fatal("empty headers must not be exhausted")
	}

	// Empty pay-as-you-go credits pool but usage window not full (typical of a
	// healthy subscription/plus account) => NOT exhausted. Credits are ignored.
	h3 := http.Header{}
	h3.Set("x-codex-primary-used-percent", "30")
	h3.Set("x-codex-credits-has-credits", "False")
	h3.Set("x-codex-credits-unlimited", "False")
	if parseCodexRateLimit(h3).exhausted() {
		t.Fatal("healthy plus account with empty credits pool must NOT be exhausted")
	}

	// Short reset window is floored to 1m to avoid hot retry loops.
	h4 := http.Header{}
	h4.Set("x-codex-primary-used-percent", "100")
	h4.Set("x-codex-primary-reset-after-seconds", "5")
	if got := parseCodexRateLimit(h4).cooldownFor(); got != time.Minute {
		t.Fatalf("expected 1m floor, got %s", got)
	}
}

// TestCodexLongTermExhausted verifies the disable-vs-cooldown decision: a weekly
// limit (reset days away) or no credits is long-term (disable), while a short
// ~5h window is not (cooldown + auto-recover).
func TestCodexLongTermExhausted(t *testing.T) {
	// Weekly limit: 100% used, resets in ~6.4 days => long-term.
	weekly := http.Header{}
	weekly.Set("x-codex-primary-used-percent", "100")
	weekly.Set("x-codex-primary-reset-after-seconds", "557411")
	if !parseCodexRateLimit(weekly).longTermExhausted() {
		t.Fatal("weekly limit should be long-term exhausted")
	}

	// Short 5h window: 100% used, resets in ~5h => NOT long-term.
	short := http.Header{}
	short.Set("x-codex-primary-used-percent", "100")
	short.Set("x-codex-primary-reset-after-seconds", strconv.Itoa(5*60*60))
	if parseCodexRateLimit(short).longTermExhausted() {
		t.Fatal("5h window should NOT be long-term exhausted")
	}

	// Empty credits pool but window under 100% (healthy plus) => NOT long-term.
	noCredits := http.Header{}
	noCredits.Set("x-codex-primary-used-percent", "30")
	noCredits.Set("x-codex-credits-has-credits", "False")
	noCredits.Set("x-codex-credits-unlimited", "False")
	if parseCodexRateLimit(noCredits).longTermExhausted() {
		t.Fatal("healthy plus account with empty credits must NOT be long-term exhausted")
	}

	// Healthy account is neither.
	ok := http.Header{}
	ok.Set("x-codex-primary-used-percent", "20")
	ok.Set("x-codex-credits-has-credits", "True")
	if parseCodexRateLimit(ok).longTermExhausted() {
		t.Fatal("healthy account must not be long-term exhausted")
	}
}

// TestFixCodexFunctionCallIDs verifies function_call ids are normalized to begin
// with "fc" (ChatGPT Codex rejects other prefixes with HTTP 400) while call_id
// and non-function_call items are left untouched.
func TestFixCodexFunctionCallIDs(t *testing.T) {
	input := []map[string]interface{}{
		{"type": "message", "role": "user", "content": "hi"},
		{"type": "function_call", "id": "call_1", "call_id": "call_1", "name": "Bash", "arguments": "{}"},
		{"type": "function_call", "id": "toolu_abc", "call_id": "toolu_abc", "name": "Bash", "arguments": "{}"},
		{"type": "function_call", "call_id": "call_9", "name": "Bash", "arguments": "{}"},
		{"type": "function_call", "id": "fc_ok", "call_id": "call_2", "name": "Bash", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "call_1", "output": "done"},
	}
	out := fixCodexFunctionCallIDs(input)

	// function_call ids must all begin with "fc".
	for i, item := range out {
		if item["type"] == "function_call" {
			id, _ := item["id"].(string)
			if len(id) < 2 || id[:2] != "fc" {
				t.Errorf("input[%d] function_call id = %q, want prefix fc", i, id)
			}
		}
	}
	// call_id must be preserved (pairs call with its output).
	if out[1]["call_id"] != "call_1" || out[2]["call_id"] != "toolu_abc" {
		t.Errorf("call_id must be preserved, got %v / %v", out[1]["call_id"], out[2]["call_id"])
	}
	// Already-valid fc id is unchanged.
	if out[4]["id"] != "fc_ok" {
		t.Errorf("valid fc id changed: got %v", out[4]["id"])
	}
	// message item is untouched.
	if _, hasID := out[0]["id"]; hasID {
		t.Errorf("message item should not gain an id")
	}
}
