// Package config — OpenAI Codex (ChatGPT backend) account pool storage.
// Separate from Kiro Account and GrokAccount so the three pools never mix.
package config

import (
	"fmt"
	"time"
)

// CodexAccount is a ChatGPT (OpenAI Codex) OAuth credential used by the codex proxy path.
// Imported from a 9router export (providerConnections where provider=codex).
type CodexAccount struct {
	ID           string `json:"id"`
	Email        string `json:"email,omitempty"`
	Nickname     string `json:"nickname,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	IDToken      string `json:"idToken,omitempty"`
	// ExpiresAt is Unix seconds (normalized from 9router ISO string on import).
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	ClientID  string `json:"clientId,omitempty"`
	// AuthMethod: typically "oauth" for ChatGPT.
	AuthMethod string `json:"authMethod,omitempty"`
	Enabled    bool   `json:"enabled"`

	// Codex/ChatGPT-specific identity (from providerSpecificData).
	ChatgptAccountId string `json:"chatgptAccountId,omitempty"`
	ChatgptPlanType  string `json:"chatgptPlanType,omitempty"`

	// Runtime stats
	RequestCount int   `json:"requestCount,omitempty"`
	ErrorCount   int   `json:"errorCount,omitempty"`
	LastUsed     int64 `json:"lastUsed,omitempty"`
	TotalTokens  int   `json:"totalTokens,omitempty"`
	// TotalCredits uses the same unit as ApiKeyEntry.CreditsUsed (customer billing unit).
	TotalCredits float64 `json:"totalCredits,omitempty"`
	BanStatus    string  `json:"banStatus,omitempty"`
	BanReason    string  `json:"banReason,omitempty"`

	// Per-account identity / egress.
	MachineId string `json:"machineId,omitempty"`
	ProxyURL  string `json:"proxyURL,omitempty"`

	// Upstream quota snapshot (best-effort).
	// QuotaStatus: ok | exhausted | unknown | error
	QuotaStatus    string  `json:"quotaStatus,omitempty"`
	QuotaMessage   string  `json:"quotaMessage,omitempty"`
	QuotaCheckedAt int64   `json:"quotaCheckedAt,omitempty"`
	QuotaRemaining float64 `json:"quotaRemaining,omitempty"`
	QuotaLimit     float64 `json:"quotaLimit,omitempty"`
}

// DefaultCodexClientID is the public OAuth client id used by the Codex CLI.
const DefaultCodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// GetCodexAccounts returns a copy of all Codex accounts.
func GetCodexAccounts() []CodexAccount {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	out := make([]CodexAccount, len(cfg.CodexAccounts))
	copy(out, cfg.CodexAccounts)
	return out
}

// GetEnabledCodexAccounts returns enabled Codex accounts only.
func GetEnabledCodexAccounts() []CodexAccount {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	var out []CodexAccount
	for _, a := range cfg.CodexAccounts {
		if a.Enabled {
			out = append(out, a)
		}
	}
	return out
}

// CodexAccountIDExists reports whether id already exists in the Codex pool.
func CodexAccountIDExists(id string) bool {
	if id == "" {
		return false
	}
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return false
	}
	for _, a := range cfg.CodexAccounts {
		if a.ID == id {
			return true
		}
	}
	return false
}

// AddCodexAccount appends a Codex account and persists (upsert on existing ID).
func AddCodexAccount(account CodexAccount) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	if account.ClientID == "" {
		account.ClientID = DefaultCodexClientID
	}
	if account.AuthMethod == "" {
		account.AuthMethod = "oauth"
	}
	if account.ID != "" {
		for i, a := range cfg.CodexAccounts {
			if a.ID == account.ID {
				// Preserve counters from existing entry unless new payload carries values.
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
				if account.MachineId == "" {
					account.MachineId = a.MachineId
				}
				if account.ProxyURL == "" {
					account.ProxyURL = a.ProxyURL
				}
				cfg.CodexAccounts[i] = account
				return Save()
			}
		}
	}
	cfg.CodexAccounts = append(cfg.CodexAccounts, account)
	return Save()
}

// UpdateCodexAccount replaces the account with matching id.
func UpdateCodexAccount(id string, account CodexAccount) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.CodexAccounts {
		if a.ID == id {
			account.ID = id
			cfg.CodexAccounts[i] = account
			return Save()
		}
	}
	return fmt.Errorf("codex account not found")
}

// DeleteCodexAccount removes a Codex account by id.
func DeleteCodexAccount(id string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.CodexAccounts {
		if a.ID == id {
			cfg.CodexAccounts = append(cfg.CodexAccounts[:i], cfg.CodexAccounts[i+1:]...)
			return Save()
		}
	}
	return nil
}

// UpdateCodexAccountToken persists refreshed tokens.
func UpdateCodexAccountToken(id, accessToken, refreshToken string, expiresAt int64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.CodexAccounts {
		if a.ID == id {
			cfg.CodexAccounts[i].AccessToken = accessToken
			if refreshToken != "" {
				cfg.CodexAccounts[i].RefreshToken = refreshToken
			}
			if expiresAt > 0 {
				cfg.CodexAccounts[i].ExpiresAt = expiresAt
			}
			return Save()
		}
	}
	return fmt.Errorf("codex account not found")
}

// SetCodexAccountEnabled toggles enabled + ban fields.
func SetCodexAccountEnabled(id string, enabled bool, reason string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.CodexAccounts {
		if a.ID == id {
			cfg.CodexAccounts[i].Enabled = enabled
			if !enabled {
				cfg.CodexAccounts[i].BanStatus = "DISABLED"
				cfg.CodexAccounts[i].BanReason = reason
			} else {
				cfg.CodexAccounts[i].BanStatus = ""
				cfg.CodexAccounts[i].BanReason = ""
			}
			return Save()
		}
	}
	return fmt.Errorf("codex account not found")
}

// UpdateCodexAccountStats updates runtime counters for a Codex account.
func UpdateCodexAccountStats(id string, requestCount, errorCount, totalTokens int, totalCredits float64, lastUsed int64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.CodexAccounts {
		if a.ID == id {
			cfg.CodexAccounts[i].RequestCount = requestCount
			cfg.CodexAccounts[i].ErrorCount = errorCount
			cfg.CodexAccounts[i].TotalTokens = totalTokens
			cfg.CodexAccounts[i].TotalCredits = totalCredits
			if lastUsed > 0 {
				cfg.CodexAccounts[i].LastUsed = lastUsed
			} else {
				cfg.CodexAccounts[i].LastUsed = time.Now().Unix()
			}
			return Save()
		}
	}
	return nil
}

// SetCodexAccountQuota persists last upstream quota probe for an account.
func SetCodexAccountQuota(id, status, message string, remaining, limit float64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.CodexAccounts {
		if a.ID != id {
			continue
		}
		cfg.CodexAccounts[i].QuotaStatus = status
		cfg.CodexAccounts[i].QuotaMessage = message
		cfg.CodexAccounts[i].QuotaCheckedAt = time.Now().Unix()
		cfg.CodexAccounts[i].QuotaRemaining = remaining
		cfg.CodexAccounts[i].QuotaLimit = limit
		return Save()
	}
	return fmt.Errorf("codex account not found")
}

// PatchCodexAccountFields updates editable admin fields without replacing tokens.
func PatchCodexAccountFields(id string, machineId, proxyURL *string, nickname, displayName *string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for i, a := range cfg.CodexAccounts {
		if a.ID != id {
			continue
		}
		if machineId != nil {
			cfg.CodexAccounts[i].MachineId = *machineId
		}
		if proxyURL != nil {
			cfg.CodexAccounts[i].ProxyURL = *proxyURL
		}
		if nickname != nil {
			cfg.CodexAccounts[i].Nickname = *nickname
		}
		if displayName != nil {
			cfg.CodexAccounts[i].DisplayName = *displayName
		}
		return Save()
	}
	return fmt.Errorf("codex account not found")
}

// GetCodexAccountByID returns a copy of one account, or nil.
func GetCodexAccountByID(id string) *CodexAccount {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	for _, a := range cfg.CodexAccounts {
		if a.ID == id {
			cp := a
			return &cp
		}
	}
	return nil
}
