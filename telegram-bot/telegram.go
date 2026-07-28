package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tgUpdate is the subset of Telegram's Update we act on. Everything else
// (edited messages, channel posts, callbacks) is ignored: fewer entry points means
// fewer ways to reach the key operations.
type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64 `json:"message_id"`
		Text      string `json:"text"`
		Chat      struct {
			ID       int64  `json:"id"`
			Type     string `json:"type"`
			Username string `json:"username"`
		} `json:"chat"`
		From *struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"from"`
	} `json:"message"`
}

type telegramClient struct {
	token string
	http  *http.Client
}

func newTelegramClient(cfg config) *telegramClient {
	return &telegramClient{
		token: cfg.BotToken,
		// Comfortably longer than the long-poll window, so the poll itself decides
		// when to return rather than the transport cutting it short.
		http: &http.Client{Timeout: cfg.PollTimeout + 30*time.Second},
	}
}

func (t *telegramClient) endpoint(method string) string {
	return "https://api.telegram.org/bot" + t.token + "/" + method
}

// GetUpdates long-polls for messages after offset.
//
// allowed_updates is restricted to plain messages, so Telegram does not even send
// the update types this bot has no handler for.
func (t *telegramClient) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]tgUpdate, error) {
	form := url.Values{}
	form.Set("timeout", fmt.Sprintf("%d", int(timeout.Seconds())))
	form.Set("allowed_updates", `["message"]`)
	if offset > 0 {
		form.Set("offset", fmt.Sprintf("%d", offset))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.endpoint("getUpdates"), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// The body can echo the bot token in some error shapes, so report only the
		// status rather than forwarding it into logs.
		return nil, fmt.Errorf("getUpdates returned HTTP %d", resp.StatusCode)
	}

	var out struct {
		OK          bool       `json:"ok"`
		Result      []tgUpdate `json:"result"`
		Description string     `json:"description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode updates: %w", err)
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram rejected getUpdates: %s", out.Description)
	}
	return out.Result, nil
}

// Send delivers a reply. Text is sent as plain text, not Markdown or HTML: key
// names are operator-supplied and would otherwise be parsed as formatting, which
// silently mangles or drops characters in the very strings that must be exact.
func (t *telegramClient) Send(ctx context.Context, chatID int64, text string) error {
	const maxLen = 4000 // Telegram's limit is 4096; leave room for the suffix.
	for len(text) > 0 {
		chunk := text
		if len(chunk) > maxLen {
			// Split on a line boundary so a key is never cut in half across messages.
			cut := strings.LastIndex(chunk[:maxLen], "\n")
			if cut <= 0 {
				cut = maxLen
			}
			chunk, text = chunk[:cut], strings.TrimLeft(text[cut:], "\n")
		} else {
			text = ""
		}
		if err := t.sendOne(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (t *telegramClient) sendOne(ctx context.Context, chatID int64, text string) error {
	form := url.Values{}
	form.Set("chat_id", fmt.Sprintf("%d", chatID))
	form.Set("text", text)
	form.Set("disable_web_page_preview", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.endpoint("sendMessage"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sendMessage returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// GetMe verifies the token at startup, so a bad token surfaces immediately instead
// of looking like an idle bot.
func (t *telegramClient) GetMe(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint("getMe"), nil)
	if err != nil {
		return "", err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("getMe returned HTTP %d (token rejected?)", resp.StatusCode)
	}
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || !out.OK {
		return "", fmt.Errorf("getMe reply not usable")
	}
	return out.Result.Username, nil
}
