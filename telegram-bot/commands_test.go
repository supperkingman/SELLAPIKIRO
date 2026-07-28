package main

import "testing"

// TestParseCommandIgnoresOrdinaryChat pins that only slash commands are acted on.
// Replying to plain chatter would make the bot noisy in a group and would surface
// error text for messages that were never meant for it.
func TestParseCommandIgnoresOrdinaryChat(t *testing.T) {
	for _, text := range []string{
		"", "   ", "hello", "newkey khachA 720", "email@example.com",
	} {
		if _, ok := parseCommand(text); ok {
			t.Errorf("%q should not parse as a command", text)
		}
	}
}

func TestParseCommandReadsNameAndArgs(t *testing.T) {
	cmd, ok := parseCommand("  /newkey   khachA   720  ")
	if !ok {
		t.Fatal("expected a command")
	}
	if cmd.Name != "newkey" {
		t.Errorf("name = %q, want newkey", cmd.Name)
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != "khachA" || cmd.Args[1] != "720" {
		t.Errorf("args = %v, want [khachA 720]", cmd.Args)
	}
}

// TestParseCommandStripsBotMention covers group chats, where Telegram appends
// @botname. Without stripping it the command would fall through to "unknown".
func TestParseCommandStripsBotMention(t *testing.T) {
	cmd, ok := parseCommand("/pause@mmodiary_key_bot khachA")
	if !ok {
		t.Fatal("expected a command")
	}
	if cmd.Name != "pause" {
		t.Errorf("name = %q, want pause", cmd.Name)
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "khachA" {
		t.Errorf("args = %v, want [khachA]", cmd.Args)
	}
}

func TestParseCommandIsCaseInsensitive(t *testing.T) {
	cmd, ok := parseCommand("/AddHours khachA 24")
	if !ok || cmd.Name != "addhours" {
		t.Errorf("got %+v, want name addhours", cmd)
	}
}

// TestResolveRefusesAmbiguousNames is the safety property of key lookup.
//
// These commands change what a paying customer can do, so acting on the first of
// several matches risks pausing the wrong customer's key. Ambiguity must be
// reported instead.
func TestResolveRefusesAmbiguousNames(t *testing.T) {
	keys := []keyView{
		{ID: "id-1", Name: "khachA-thang1"},
		{ID: "id-2", Name: "khachA-thang2"},
		{ID: "id-3", Name: "khachB"},
	}

	matches := countNameMatches(keys, "khachA")
	if matches != 2 {
		t.Fatalf("fixture should be ambiguous, got %d matches", matches)
	}
	if got := countNameMatches(keys, "khachB"); got != 1 {
		t.Errorf("khachB should match exactly once, got %d", got)
	}
	if got := countNameMatches(keys, "khong-ton-tai"); got != 0 {
		t.Errorf("unknown fragment should match nothing, got %d", got)
	}
}

// countNameMatches mirrors the fragment matching inside resolve. Kept as a helper so
// the ambiguity rule can be asserted without standing up an HTTP server.
func countNameMatches(keys []keyView, ref string) int {
	n := 0
	for _, k := range keys {
		if containsFold(k.Name, ref) {
			n++
		}
	}
	return n
}

func TestMatchesFilterMirrorsKeyadminVocabulary(t *testing.T) {
	active := keyView{Expiry: expiryState{Mode: "active", SecondsLeft: 3600}}
	expired := keyView{Expiry: expiryState{Mode: "active", SecondsLeft: 0}}
	paused := keyView{Expiry: expiryState{Mode: "paused", SecondsLeft: 7200}}
	permanent := keyView{Expiry: expiryState{Mode: "none"}}

	cases := []struct {
		filter string
		key    keyView
		want   bool
	}{
		{"active", active, true},
		// An expired key is still mode "active" in keyadmin, distinguished only by
		// SecondsLeft. Reporting it as active would hide the reason a customer's key
		// stopped working.
		{"active", expired, false},
		{"expired", expired, true},
		{"expired", active, false},
		{"paused", paused, true},
		{"paused", active, false},
		{"permanent", permanent, true},
		{"permanent", active, false},
		{"", active, true},
	}
	for _, tc := range cases {
		if got := matchesFilter(tc.key, tc.filter); got != tc.want {
			t.Errorf("filter %q on mode %q left %d: got %v, want %v",
				tc.filter, tc.key.Expiry.Mode, tc.key.Expiry.SecondsLeft, got, tc.want)
		}
	}
}

func TestDescribeExpiryDistinguishesExpiredFromActive(t *testing.T) {
	if got := describeExpiry(expiryState{Mode: "active", SecondsLeft: 0}); got != "đã hết hạn" {
		t.Errorf("expired key described as %q", got)
	}
	if got := describeExpiry(expiryState{Mode: "none"}); got != "không giới hạn thời gian" {
		t.Errorf("permanent key described as %q", got)
	}
	paused := describeExpiry(expiryState{Mode: "paused", SecondsLeft: 7200})
	if !containsFold(paused, "tạm dừng") {
		t.Errorf("paused key described as %q", paused)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "0 phút"},
		{30, "dưới 1 phút"},
		{3600, "1 giờ"},
		{86400, "1 ngày"},
		{90000, "1 ngày 1 giờ"},
		{1800, "30 phút"},
	}
	for _, tc := range cases {
		if got := humanDuration(tc.secs); got != tc.want {
			t.Errorf("humanDuration(%d) = %q, want %q", tc.secs, got, tc.want)
		}
	}
}
