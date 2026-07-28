package main

import (
	"strings"
	"testing"
)

// setEnv points the process environment at a known state for one test.
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{
		"TELEGRAM_BOT_TOKEN", "TELEGRAM_ADMIN_IDS",
		"KEYADMIN_URL", "KEYADMIN_TOKEN", "SITE_LABEL",
	} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// TestLoadConfigRequiresAnAllowlist is the most important test in this package.
//
// A Telegram bot's username is discoverable and anyone can start a chat with it, so
// the allowlist is the only thing between a stranger and the API keys. Starting
// without one must be impossible, not merely discouraged.
func TestLoadConfigRequiresAnAllowlist(t *testing.T) {
	cases := []struct {
		name string
		ids  string
	}{
		{"missing", ""},
		{"blank", "   "},
		{"only separators", ",,,"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "test-token",
				"KEYADMIN_TOKEN":     "test-keyadmin",
				"TELEGRAM_ADMIN_IDS": tc.ids,
			})
			if _, err := loadConfig(); err == nil {
				t.Fatal("expected startup to be refused without a usable allowlist")
			}
		})
	}
}

// TestLoadConfigRejectsMalformedIDs pins that a typo is reported instead of being
// dropped. Silently ignoring a bad entry could leave a shorter allowlist than the
// operator believes they configured.
func TestLoadConfigRejectsMalformedIDs(t *testing.T) {
	setEnv(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "test-token",
		"KEYADMIN_TOKEN":     "test-keyadmin",
		"TELEGRAM_ADMIN_IDS": "12345,not-a-number",
	})
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected a non-numeric chat ID to be rejected")
	}
	if !strings.Contains(err.Error(), "not-a-number") {
		t.Errorf("error should name the offending entry, got: %v", err)
	}
}

func TestLoadConfigRequiresTokens(t *testing.T) {
	t.Run("bot token", func(t *testing.T) {
		setEnv(t, map[string]string{
			"TELEGRAM_ADMIN_IDS": "1",
			"KEYADMIN_TOKEN":     "x",
		})
		if _, err := loadConfig(); err == nil {
			t.Error("expected a missing bot token to be fatal")
		}
	})
	t.Run("keyadmin token", func(t *testing.T) {
		setEnv(t, map[string]string{
			"TELEGRAM_ADMIN_IDS": "1",
			"TELEGRAM_BOT_TOKEN": "x",
		})
		if _, err := loadConfig(); err == nil {
			t.Error("expected a missing keyadmin token to be fatal")
		}
	})
}

func TestLoadConfigParsesAllowlistAndDefaults(t *testing.T) {
	setEnv(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "test-token",
		"KEYADMIN_TOKEN":     "test-keyadmin",
		"TELEGRAM_ADMIN_IDS": " 111 , 222,333 ",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range []int64{111, 222, 333} {
		if !cfg.AdminIDs[id] {
			t.Errorf("chat %d should be allowed", id)
		}
	}
	if len(cfg.AdminIDs) != 3 {
		t.Errorf("expected 3 admins, got %d", len(cfg.AdminIDs))
	}
	if cfg.KeyadminURL == "" || cfg.SiteLabel == "" {
		t.Error("URL and label should fall back to defaults")
	}
}

// TestIsAdminFailsClosed covers the check used on every incoming message.
func TestIsAdminFailsClosed(t *testing.T) {
	cfg := config{AdminIDs: map[int64]bool{111: true}}

	if !cfg.isAdmin(111) {
		t.Error("an allowlisted chat must be accepted")
	}
	if cfg.isAdmin(222) {
		t.Error("a chat outside the allowlist must be refused")
	}
	// Zero is what an absent chat ID decodes to, so it must never be treated as
	// allowed even if something odd arrives from Telegram.
	if cfg.isAdmin(0) {
		t.Error("a zero chat ID must be refused")
	}
	// An empty allowlist must refuse everyone rather than allow everyone. loadConfig
	// prevents this state, but the check is the last line of defence if it is ever
	// constructed some other way.
	empty := config{AdminIDs: map[int64]bool{}}
	if empty.isAdmin(111) {
		t.Error("an empty allowlist must refuse everyone")
	}
	// Negative IDs are how Telegram identifies group chats; one that is not
	// allowlisted must be refused like any other.
	if cfg.isAdmin(-100123) {
		t.Error("a non-allowlisted group chat must be refused")
	}
}
