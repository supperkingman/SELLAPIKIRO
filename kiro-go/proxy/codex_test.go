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
