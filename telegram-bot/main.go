package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := loadConfig()
	if err != nil {
		// Configuration problems are fatal on purpose. The alternative, starting with
		// a missing allowlist or token, would be a bot that either answers nobody or
		// answers everybody.
		log.Fatalf("[bot] configuration: %v", err)
	}

	tg := newTelegramClient(cfg)
	api := newKeyadminClient(cfg)
	h := &handler{cfg: cfg, api: api}

	// Verify both dependencies before entering the loop, so a wrong token or a
	// keyadmin that is not reachable shows up now rather than as a silent bot.
	startCtx, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
	username, err := tg.GetMe(startCtx)
	if err != nil {
		cancelStart()
		log.Fatalf("[bot] telegram rejected the token: %v", err)
	}
	if err := api.Health(startCtx); err != nil {
		// Not fatal: keyadmin may still be starting alongside us under compose, and
		// the bot recovers on its own once it comes up.
		log.Printf("[bot] warning: keyadmin not reachable yet: %v", err)
	}
	cancelStart()

	log.Printf("[bot] running as @%s for site %q, %d admin(s) allowed",
		username, cfg.SiteLabel, len(cfg.AdminIDs))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	run(ctx, cfg, tg, h)
	log.Printf("[bot] shutting down")
}

// run is the poll loop. Kept separate from main so the wiring above stays readable.
func run(ctx context.Context, cfg config, tg *telegramClient, h *handler) {
	var offset int64
	// Backoff for transient Telegram or network failures, so a sustained outage does
	// not turn into a hot loop against their API.
	backoff := time.Second
	const maxBackoff = time.Minute

	for {
		if ctx.Err() != nil {
			return
		}

		updates, err := tg.GetUpdates(ctx, offset, cfg.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[bot] getUpdates failed, retrying in %s: %v", backoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for _, u := range updates {
			// Advance past this update even if handling it fails. Leaving the offset
			// behind would make one bad message replay forever, blocking every message
			// after it.
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			handleUpdate(ctx, cfg, tg, h, u)
		}
	}
}

// handleUpdate processes one message.
func handleUpdate(ctx context.Context, cfg config, tg *telegramClient, h *handler, u tgUpdate) {
	if u.Message == nil || u.Message.Text == "" {
		return
	}
	chatID := u.Message.Chat.ID

	if !cfg.isAdmin(chatID) {
		// Deliberately silent. Replying would confirm the bot is live and managing
		// keys, which is information a stranger should not get for free. Logged so
		// probing is still visible to the operator.
		who := ""
		if u.Message.From != nil {
			who = u.Message.From.Username
		}
		log.Printf("[bot] ignored message from unauthorised chat %d (@%s)", chatID, who)
		return
	}

	cmd, ok := parseCommand(u.Message.Text)
	if !ok {
		return
	}

	// Log the command but never its output: replies to /newkey contain live API keys,
	// and container logs are far easier to read than the keystore.
	log.Printf("[bot] chat %d ran /%s with %d arg(s)", chatID, cmd.Name, len(cmd.Args))

	cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	reply := h.Handle(cmdCtx, cmd)
	if reply == "" {
		return
	}
	if err := tg.Send(cmdCtx, chatID, reply); err != nil {
		log.Printf("[bot] failed to reply to chat %d: %v", chatID, err)
	}
}
