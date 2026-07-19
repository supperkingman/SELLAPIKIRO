package proxy

import "testing"

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
