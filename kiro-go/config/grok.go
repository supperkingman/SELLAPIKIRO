// Package config — Grok CLI (Grok Build) account pool storage.
// Separate from Kiro Account so the two pools never mix.
package config

import (
	"fmt"
	"time"
)

// GrokAccount is an xAI / Grok Build OAuth credential used by the grok-cli proxy path.
// Imported from 9router export (providerConnections where provider=grok-cli).
type GrokAccount struct {
	ID           string `json:"id"`
	Email        string `json:"email,omitempty"`
	Nickname     string `json:"nickname,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	// ExpiresAt is Unix seconds (normalized from 9router ISO string on import).
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	Scope     string `json:"scope,omitempty"`
	ClientID  string `json:"clientId,omitempty"`
	// AuthMethod: typically "device_code" for Grok Build.
	AuthMethod string `json:"authMethod,omitempty"`
	UserID     string `json:"userId,omitempty"`
	IDToken    string `json:"idToken,omitempty"`
	Enabled    bool   `json:"enabled"`

	// Runtime stats
	RequestCount int   `json:"requestCount,omitempty"`
	ErrorCount   int   `json:"errorCount,omitempty"`
	LastUsed     int64 `json:"lastUsed,omitempty"`
	TotalTokens  int   `json:"totalTokens,omitempty"`
	// TotalCredits uses the same unit as ApiKeyEntry.CreditsUsed:
	// 1 credit = 1000 tokens (input+output). See proxy/grok.go TokensToCredits.
	TotalCredits float64 `json:"totalCredits,omitempty"`
	BanStatus    string  `json:"banStatus,omitempty"`
	BanReason    string  `json:"banReason,omitempty"`
}

// DefaultGrokClientID is the public OAuth client id used by Grok Build / grok-cli (9router).
const DefaultGrokClientID = "b1a00492-073a-47ea-816f-4c329264a828"

// GetGrokAccounts returns a copy of all Grok accounts.
func GetGrokAccounts() []GrokAccount {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	out := make([]GrokAccount, len(cfg.GrokAccounts))
	copy(out, cfg.GrokAccounts)
	return out
}

// GetEnabledGrokAccounts returns enabled Grok accounts only.
func GetEnabledGrokAccounts() []GrokAccount {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	var out []GrokAccount
	for _, a := range cfg.GrokAccounts {
		if a.Enabled {
			out = append(out, a)
		}
	}
	return out
}

// GrokAccountIDExists reports whether id already exists in the Grok pool.
func GrokAccountIDExists(id string) bool {
	if id == "" {
		return false
	}
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return false
	}
	for _, a := range cfg.GrokAccounts {
		if a.ID == id {
			return true
		}
	}
	return false
}

// AddGrokAccount appends a Grok account and persists.
// If account.ID already exists, it updates tokens/profile fields and keeps runtime stats
// (requestCount/totalTokens/...) so re-import from 9router is safe (upsert).
func AddGrokAccount(account GrokAccount) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	if account.ClientID == "" {
		account.ClientID = DefaultGrokClientID
	}
	if account.AuthMethod == "" {
		account.AuthMethod = "device_code"
	}
	if account.ID != "" {
		for i, a := range cfg.GrokAccounts {
			if a.ID == account.ID {
				// Preserve counters from existing entry unless new payload carries higher values.
				if account.RequestCount == 0 {
					account.RequestCount = a.RequestCount
				}
				if account.ErrorCount == 0 {
					account.ErrorCount = a.ErrorCount
				}
				if account.TotalTokens == 0 {
					account.TotalTokens = a.TotalTokens
				}
				if account.TotalCredits == 0 {
					account.TotalCredits = a.TotalCredits
				}
				if account.LastUsed == 0 {
					account.LastUsed = a.LastUsed
				}
				if account.BanStatus == "" {
					account.BanStatus = a.BanStatus
					account.BanReason = a.BanReason
				}
				cfg.GrokAccounts[i] = account
				return Save()
			}
		}
	}
	cfg.GrokAccounts = append(cfg.GrokAccounts, account)
	return Save()
}

// UpdateGrokAccount replaces the account with matching id.
func UpdateGrokAccount(id string, account GrokAccount) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.GrokAccounts {
		if a.ID == id {
			account.ID = id
			cfg.GrokAccounts[i] = account
			return Save()
		}
	}
	return fmt.Errorf("grok account not found")
}

// DeleteGrokAccount removes a Grok account by id.
func DeleteGrokAccount(id string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.GrokAccounts {
		if a.ID == id {
			cfg.GrokAccounts = append(cfg.GrokAccounts[:i], cfg.GrokAccounts[i+1:]...)
			return Save()
		}
	}
	return nil
}

// UpdateGrokAccountToken persists refreshed tokens.
func UpdateGrokAccountToken(id, accessToken, refreshToken string, expiresAt int64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.GrokAccounts {
		if a.ID == id {
			cfg.GrokAccounts[i].AccessToken = accessToken
			if refreshToken != "" {
				cfg.GrokAccounts[i].RefreshToken = refreshToken
			}
			if expiresAt > 0 {
				cfg.GrokAccounts[i].ExpiresAt = expiresAt
			}
			return Save()
		}
	}
	return fmt.Errorf("grok account not found")
}

// SetGrokAccountEnabled toggles enabled + ban fields.
func SetGrokAccountEnabled(id string, enabled bool, reason string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.GrokAccounts {
		if a.ID == id {
			cfg.GrokAccounts[i].Enabled = enabled
			if !enabled {
				cfg.GrokAccounts[i].BanStatus = "DISABLED"
				cfg.GrokAccounts[i].BanReason = reason
			} else {
				cfg.GrokAccounts[i].BanStatus = ""
				cfg.GrokAccounts[i].BanReason = ""
			}
			return Save()
		}
	}
	return fmt.Errorf("grok account not found")
}

// UpdateGrokAccountStats updates runtime counters for a Grok account.
func UpdateGrokAccountStats(id string, requestCount, errorCount, totalTokens int, totalCredits float64, lastUsed int64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.GrokAccounts {
		if a.ID == id {
			cfg.GrokAccounts[i].RequestCount = requestCount
			cfg.GrokAccounts[i].ErrorCount = errorCount
			cfg.GrokAccounts[i].TotalTokens = totalTokens
			cfg.GrokAccounts[i].TotalCredits = totalCredits
			if lastUsed > 0 {
				cfg.GrokAccounts[i].LastUsed = lastUsed
			} else {
				cfg.GrokAccounts[i].LastUsed = time.Now().Unix()
			}
			return Save()
		}
	}
	return nil
}

// GetGrokAccountByID returns a copy of one account, or nil.
func GetGrokAccountByID(id string) *GrokAccount {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	for _, a := range cfg.GrokAccounts {
		if a.ID == id {
			cp := a
			return &cp
		}
	}
	return nil
}
