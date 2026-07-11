// keyadmin_client.go - cau noi toi service keyadmin (port 8082) de tao API key.
// Tai su dung KEYADMIN_TOKEN (bearer) va API /api/keys/create da co san.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// keyAdminClient goi HTTP toi keyadmin.
type keyAdminClient struct {
	base   string
	token  string
	client *http.Client
}

func newKeyAdminClient(base, token string) *keyAdminClient {
	return &keyAdminClient{
		base:   strings.TrimRight(base, "/"),
		token:  token,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// createdKey mo ta 1 key vua tao tu keyadmin.
type createdKey struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// CreateKey tao 1 API key voi creditLimit cho truoc. Tra ve chuoi key "sk-...".
// name: ten hien thi trong admin kiro-go (vd: don hang + email khach).
func (c *keyAdminClient) CreateKey(name string, credits int) (createdKey, error) {
	payload := map[string]interface{}{
		"name":        name,
		"count":       1,
		"creditLimit": credits,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, c.base+"/api/keys/create", bytes.NewReader(body))
	if err != nil {
		return createdKey{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return createdKey{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return createdKey{}, fmt.Errorf("keyadmin create failed (%d): %s", resp.StatusCode, string(data))
	}

	var out struct {
		Created []createdKey `json:"created"`
		Errors  int          `json:"errors"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return createdKey{}, err
	}
	if len(out.Created) == 0 {
		return createdKey{}, errors.New("keyadmin khong tra ve key nao")
	}
	return out.Created[0], nil
}

// CreateKeyWithExpiry tao key kem thoi han (days/hours). 0/0 = vinh vien.
// keyadmin /api/keys/create da ho tro days+hours -> ma hoa marker #exp trong ten.
func (c *keyAdminClient) CreateKeyWithExpiry(name string, credits, days, hours int) (createdKey, error) {
	payload := map[string]interface{}{
		"name":        name,
		"count":       1,
		"creditLimit": credits,
	}
	if days > 0 {
		payload["days"] = days
	}
	if hours > 0 {
		payload["hours"] = hours
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, c.base+"/api/keys/create", bytes.NewReader(body))
	if err != nil {
		return createdKey{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return createdKey{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return createdKey{}, fmt.Errorf("keyadmin create failed (%d): %s", resp.StatusCode, string(data))
	}
	var out struct {
		Created []createdKey `json:"created"`
		Errors  int          `json:"errors"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return createdKey{}, err
	}
	if len(out.Created) == 0 {
		return createdKey{}, errors.New("keyadmin khong tra ve key nao")
	}
	return out.Created[0], nil
}

// keyExpiry: trang thai han dung keyadmin tinh san (doc thang, KHONG parse lai ten).
type keyExpiry struct {
	Mode        string `json:"mode"` // none | active | paused
	ExpiresAt   int64  `json:"expiresAt"`
	SecondsLeft int64  `json:"secondsLeft"`
}

// keyUsage mo ta usage cua 1 key doc tu keyadmin /api/keys.
type keyUsage struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Enabled     bool      `json:"enabled"`
	CreditLimit float64   `json:"creditLimit"`
	CreditsUsed float64   `json:"creditsUsed"`
	TokensUsed  int64     `json:"tokensUsed"`
	Expiry      keyExpiry `json:"expiry"`
}

// ListKeys lay toan bo key + usage tu keyadmin (dung cho dashboard/admin).
func (c *keyAdminClient) ListKeys() ([]keyUsage, error) {
	req, err := http.NewRequest(http.MethodGet, c.base+"/api/keys", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keyadmin list failed (%d)", resp.StatusCode)
	}
	var wrap struct {
		Keys []keyUsage `json:"keys"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return nil, err
	}
	return wrap.Keys, nil
}

// usageByID tra ve map keyID -> usage de tra cuu nhanh.
func (c *keyAdminClient) usageByID() (map[string]keyUsage, error) {
	keys, err := c.ListKeys()
	if err != nil {
		return nil, err
	}
	m := make(map[string]keyUsage, len(keys))
	for _, k := range keys {
		m[k.ID] = k
	}
	return m, nil
}

// postAction goi mot endpoint POST /api/keys/<action> voi payload JSON.
func (c *keyAdminClient) postAction(action string, payload map[string]interface{}) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, c.base+"/api/keys/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("keyadmin %s failed (%d): %s", action, resp.StatusCode, string(data))
	}
	return nil
}

// SetCredit dat creditLimit tuyet doi cho key.
func (c *keyAdminClient) SetCredit(id string, credit float64) error {
	return c.postAction("set-credit", map[string]interface{}{"id": id, "credit": credit})
}

// AddCredit cong/tru credit (delta am de tru).
func (c *keyAdminClient) AddCredit(id string, delta float64) error {
	return c.postAction("add-credit", map[string]interface{}{"id": id, "delta": delta})
}

// AddHours cong/tru thoi han theo gio (hours am de tru).
func (c *keyAdminClient) AddHours(id string, hours int) error {
	return c.postAction("add-hours", map[string]interface{}{"id": id, "hours": hours})
}

// SetExpiry DAT thoi han tuyet doi = now + days + hours. days=0 && hours=0 -> vinh vien.
func (c *keyAdminClient) SetExpiry(id string, days, hours int) error {
	return c.postAction("set-expiry", map[string]interface{}{"id": id, "days": days, "hours": hours})
}

// RenameKey doi ten key (giu nguyen marker han).
func (c *keyAdminClient) RenameKey(id, name string) error {
	return c.postAction("rename", map[string]interface{}{"id": id, "name": name})
}

// DeleteKey xoa key (fallback disable neu kiro-go khong ho tro).
func (c *keyAdminClient) DeleteKey(id string) error {
	return c.postAction("delete", map[string]interface{}{"id": id})
}
