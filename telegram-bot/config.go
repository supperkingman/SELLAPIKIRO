package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// config holds everything the bot needs to run. All of it comes from the
// environment so no secret is ever committed.
type config struct {
	// BotToken authenticates us to Telegram. Treat as a password: it grants full
	// control of the bot, including reading every message sent to it.
	BotToken string

	// AdminIDs is the allowlist of Telegram chat IDs permitted to issue commands.
	// This is the ONLY thing standing between a stranger and your API keys: a bot
	// username is discoverable, so anyone can start a chat with it. An empty
	// allowlist therefore refuses everyone rather than allowing everyone.
	AdminIDs map[int64]bool

	// KeyadminURL is the internal address of the keyadmin service. It stays on
	// loopback (or the compose network) so key management never crosses the public
	// internet.
	KeyadminURL string

	// KeyadminToken is the bearer token keyadmin expects.
	KeyadminToken string

	// SiteLabel is shown in replies so that, if this bot is ever pointed at more
	// than one deployment, it is obvious which site a key belongs to.
	SiteLabel string

	// PollTimeout is the long-poll duration for getUpdates. Telegram holds the
	// request open until an update arrives, which is cheaper and lower latency than
	// repeated short polls.
	PollTimeout time.Duration

	// RequestTimeout bounds calls to keyadmin.
	RequestTimeout time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		BotToken:       strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		KeyadminURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("KEYADMIN_URL")), "/"),
		KeyadminToken:  strings.TrimSpace(os.Getenv("KEYADMIN_TOKEN")),
		SiteLabel:      strings.TrimSpace(os.Getenv("SITE_LABEL")),
		AdminIDs:       map[int64]bool{},
		PollTimeout:    50 * time.Second,
		RequestTimeout: 30 * time.Second,
	}

	if cfg.BotToken == "" {
		return cfg, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if cfg.KeyadminURL == "" {
		cfg.KeyadminURL = "http://keyadmin:8082"
	}
	if cfg.KeyadminToken == "" {
		return cfg, fmt.Errorf("KEYADMIN_TOKEN is required")
	}
	if cfg.SiteLabel == "" {
		// Compose passes SITE_LABEL through empty when the operator has not set one,
		// so fall back to the site name they already configured rather than making
		// them repeat it.
		cfg.SiteLabel = strings.TrimSpace(os.Getenv("SITE_NAME"))
	}
	if cfg.SiteLabel == "" {
		cfg.SiteLabel = "kiro"
	}

	raw := strings.TrimSpace(os.Getenv("TELEGRAM_ADMIN_IDS"))
	if raw == "" {
		// Refusing to start beats starting with an open door. A bot that answers
		// everyone would hand out API keys to anyone who found its username.
		return cfg, fmt.Errorf("TELEGRAM_ADMIN_IDS is required: without it the bot would accept commands from anyone")
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("TELEGRAM_ADMIN_IDS contains a non-numeric entry %q", part)
		}
		cfg.AdminIDs[id] = true
	}
	if len(cfg.AdminIDs) == 0 {
		return cfg, fmt.Errorf("TELEGRAM_ADMIN_IDS parsed to an empty list")
	}
	return cfg, nil
}

// isAdmin reports whether a chat may issue commands.
//
// Deliberately fails closed: an unknown ID, a zero ID, or an empty allowlist all
// return false.
func (c config) isAdmin(chatID int64) bool {
	if len(c.AdminIDs) == 0 || chatID == 0 {
		return false
	}
	return c.AdminIDs[chatID]
}
