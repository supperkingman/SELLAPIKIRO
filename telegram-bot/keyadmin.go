package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// expiryState mirrors keyadmin's expiryState. Mode is one of none, active, paused.
type expiryState struct {
	Mode        string `json:"mode"`
	ExpiresAt   int64  `json:"expiresAt,omitempty"`
	SecondsLeft int64  `json:"secondsLeft"`
}

// keyView mirrors keyadmin's keyView: the key plus its decoded expiry, with the
// internal markers already stripped from Name.
type keyView struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Enabled     bool        `json:"enabled"`
	TokenLimit  int64       `json:"tokenLimit"`
	CreditLimit float64     `json:"creditLimit"`
	TokensUsed  int64       `json:"tokensUsed"`
	CreditsUsed float64     `json:"creditsUsed"`
	Expiry      expiryState `json:"expiry"`
}

// createdKey is one entry of the create response.
type createdKey struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type keyadminClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newKeyadminClient(cfg config) *keyadminClient {
	return &keyadminClient{
		baseURL: cfg.KeyadminURL,
		token:   cfg.KeyadminToken,
		http:    &http.Client{Timeout: cfg.RequestTimeout},
	}
}

// call posts to keyadmin and decodes the reply into out.
//
// Errors carry keyadmin's own message where there is one, because those messages
// are written for an operator ("key khong co han de tam dung") and are more useful
// to forward than a bare status code.
func (c *keyadminClient) call(ctx context.Context, path string, body, out interface{}) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}

	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keyadmin unreachable: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read reply: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("keyadmin returned HTTP %d", resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode reply: %w", err)
	}
	return nil
}

// CreateKeys makes count keys named base, valid for the given hours.
//
// hours of 0 means no expiry at all. That is a real state in keyadmin ("permanent"),
// so it is passed through rather than defaulted, but the command layer asks for an
// explicit duration to keep it from happening by accident.
func (c *keyadminClient) CreateKeys(ctx context.Context, base string, count, hours int) ([]createdKey, error) {
	req := map[string]interface{}{
		"name":  base,
		"count": count,
		"hours": hours,
	}
	var out struct {
		Created []createdKey `json:"created"`
		Keys    []createdKey `json:"keys"`
	}
	if err := c.call(ctx, "/api/keys/create", req, &out); err != nil {
		return nil, err
	}
	// The field name is not pinned by a shared type, so accept either spelling
	// rather than silently returning nothing if it differs.
	if len(out.Created) > 0 {
		return out.Created, nil
	}
	return out.Keys, nil
}

func (c *keyadminClient) AddHours(ctx context.Context, id string, hours int) (*keyView, error) {
	var v keyView
	err := c.call(ctx, "/api/keys/add-hours", map[string]interface{}{
		"id": id, "hours": hours,
	}, &v)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (c *keyadminClient) Pause(ctx context.Context, id string) (*keyView, error) {
	var v keyView
	if err := c.call(ctx, "/api/keys/pause", map[string]interface{}{"id": id}, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (c *keyadminClient) Resume(ctx context.Context, id string) (*keyView, error) {
	var v keyView
	if err := c.call(ctx, "/api/keys/resume", map[string]interface{}{"id": id}, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// List returns every key. keyadmin supports filters, but the bot pulls the whole
// list and filters locally so /info can match on a name fragment as well as an ID.
func (c *keyadminClient) List(ctx context.Context) ([]keyView, error) {
	var out struct {
		Keys []keyView `json:"keys"`
	}
	if err := c.call(ctx, "/api/keys", nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// Health reports whether keyadmin answers, used at startup so a misconfigured
// token or URL is visible immediately rather than on the first command.
func (c *keyadminClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// describeExpiry renders an expiry state for a human.
func describeExpiry(e expiryState) string {
	switch e.Mode {
	case "paused":
		return fmt.Sprintf("tạm dừng, còn %s", humanDuration(e.SecondsLeft))
	case "active":
		if e.SecondsLeft <= 0 {
			return "đã hết hạn"
		}
		exp := time.Unix(e.ExpiresAt, 0).Local().Format("2006-01-02 15:04")
		return fmt.Sprintf("còn %s (hết hạn %s)", humanDuration(e.SecondsLeft), exp)
	default:
		return "không giới hạn thời gian"
	}
}

// humanDuration renders seconds as days/hours/minutes, which is how the operator
// thinks about key top-ups.
func humanDuration(secs int64) string {
	if secs <= 0 {
		return "0 phút"
	}
	d := secs / 86400
	h := (secs % 86400) / 3600
	m := (secs % 3600) / 60

	var parts []string
	if d > 0 {
		parts = append(parts, fmt.Sprintf("%d ngày", d))
	}
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%d giờ", h))
	}
	if m > 0 && d == 0 {
		// Minutes only matter when the total is short; alongside days they are noise.
		parts = append(parts, fmt.Sprintf("%d phút", m))
	}
	if len(parts) == 0 {
		return "dưới 1 phút"
	}
	return strings.Join(parts, " ")
}
