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
