package pool

import (
	"kiro-go/config"
	"time"
)

// Warm-up for newly added Codex accounts.
//
// A brand-new ChatGPT/Codex account that is hammered with dense, concurrent
// traffic the instant it is added looks like automated abuse to OpenAI and gets
// flagged/locked early. To avoid that, a freshly added account is ramped up
// gradually: for the first day it is limited to a small number of concurrent
// requests and a minimum spacing between requests, both of which relax as the
// account ages. Accounts with AddedAt==0 (legacy accounts imported before this
// feature) are treated as fully warmed for back-compat.

// codexWarmupStage returns the max concurrent in-flight requests and the minimum
// spacing between requests for an account, based on how long ago it was added.
// warming=false means "no limits" (fully warmed or legacy account).
func codexWarmupStage(addedAt int64) (maxConcurrent int, minSpacing time.Duration, warming bool) {
	if addedAt <= 0 {
		return 0, 0, false
	}
	age := time.Now().Unix() - addedAt
	switch {
	case age < 300: // first 5 minutes: 1 request at a time, 30s apart
		return 1, 30 * time.Second, true
	case age < 1800: // 5–30 min: 1 at a time, 12s apart
		return 1, 12 * time.Second, true
	case age < 7200: // 30 min–2 h: up to 2 concurrent, 6s apart
		return 2, 6 * time.Second, true
	case age < 21600: // 2–6 h: up to 3 concurrent, 3s apart
		return 3, 3 * time.Second, true
	case age < 86400: // 6–24 h: up to 5 concurrent, 1s apart
		return 5, 1 * time.Second, true
	default: // >24 h: fully warmed
		return 0, 0, false
	}
}

// codexWarmupAllowsLocked reports whether an account may take a new request right
// now under its warm-up constraints. Requires p.mu held (read or write).
// Fully warmed / legacy accounts always return true.
func (p *CodexPool) codexWarmupAllowsLocked(acc *config.CodexAccount) bool {
	maxC, minSpacing, warming := codexWarmupStage(acc.AddedAt)
	if !warming {
		return true
	}
	if maxC > 0 && p.inFlightLocked(acc.ID) >= maxC {
		return false
	}
	if minSpacing > 0 {
		if last := p.lastUsedLocked(acc.ID); last > 0 {
			if time.Now().Unix()-last < int64(minSpacing.Seconds()) {
				return false
			}
		}
	}
	return true
}

// lastUsedLocked returns the last-used unix time for an account. Requires p.mu held.
func (p *CodexPool) lastUsedLocked(id string) int64 {
	if p.stats != nil {
		if s, ok := p.stats[id]; ok && s != nil {
			return s.lastUsed
		}
	}
	if acc := config.GetCodexAccountByID(id); acc != nil {
		return acc.LastUsed
	}
	return 0
}

// CodexWarmupInfo is a read-only view of an account's warm-up state for admin UI.
type CodexWarmupInfo struct {
	Warming       bool  `json:"warming"`
	MaxConcurrent int   `json:"maxConcurrent,omitempty"`
	MinSpacingSec int   `json:"minSpacingSec,omitempty"`
	AgeSec        int64 `json:"ageSec,omitempty"`
}

// WarmupInfo returns the current warm-up state for an account id (admin display).
func (p *CodexPool) WarmupInfo(id string) CodexWarmupInfo {
	acc := p.GetByID(id)
	if acc == nil {
		return CodexWarmupInfo{}
	}
	maxC, spacing, warming := codexWarmupStage(acc.AddedAt)
	info := CodexWarmupInfo{Warming: warming, MaxConcurrent: maxC, MinSpacingSec: int(spacing.Seconds())}
	if acc.AddedAt > 0 {
		info.AgeSec = time.Now().Unix() - acc.AddedAt
	}
	return info
}
