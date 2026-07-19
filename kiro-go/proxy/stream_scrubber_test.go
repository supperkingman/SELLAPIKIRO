package proxy

import "testing"

// TestStreamScrubberSplitToken verifies that a sensitive provider token split
// across multiple streaming deltas is still scrubbed before reaching the client.
func TestStreamScrubberSplitToken(t *testing.T) {
	// Simulate "I am Grok" arriving as "I am Gr" + "ok" on the Claude disguise path.
	s := newStreamScrubber(true, disguiseClaude)
	var out string
	out += s.Scrub("I am Gr")
	out += s.Scrub("ok, built by xAI.")
	out += s.Flush()

	if containsAny(out, []string{"Grok", "grok", "xAI", "xai"}) {
		t.Fatalf("leaked provider token across split deltas: %q", out)
	}
	if !containsAny(out, []string{"Claude"}) {
		t.Fatalf("expected disguised identity in output, got %q", out)
	}
}

// TestStreamScrubberSplitTokenGPT verifies the same protection on the GPT path
// (Grok hidden, gpt identity kept) with "Grok" split as "Gro"+"k".
func TestStreamScrubberSplitTokenGPT(t *testing.T) {
	s := newStreamScrubber(true, disguiseGPT)
	var out string
	out += s.Scrub("Hello from Gro")
	out += s.Scrub("k here.")
	out += s.Flush()
	if containsAny(out, []string{"Grok", "grok"}) {
		t.Fatalf("leaked Grok across split deltas on GPT path: %q", out)
	}
}

// TestStreamScrubberNoWhitespaceCap ensures a long whitespace-free stream still
// makes forward progress instead of buffering forever.
func TestStreamScrubberNoWhitespaceCap(t *testing.T) {
	s := newStreamScrubber(true, disguiseClaude)
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	got := s.Scrub(long)
	if got == "" {
		t.Fatalf("expected progress on long whitespace-free delta, got empty")
	}
	got += s.Flush()
	if len(got) != len(long) {
		t.Fatalf("scrubber changed benign content length: in=%d out=%d", len(long), len(got))
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
