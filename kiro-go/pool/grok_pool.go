package pool

import (
	"kiro-go/config"
	"sync"
	"strings"
	"sync/atomic"
	"time"
)

// GrokPool is a simple round-robin pool for Grok CLI accounts.
type GrokPool struct {
	mu       sync.RWMutex
	accounts []config.GrokAccount
	index    uint64
	// in-memory stats (flushed occasionally via UpdateStats)
	stats map[string]*grokRuntimeStats
	// temporary soft-ban: account id -> unix expiry (not persisted; survives until process restart)
	cooldownUntil map[string]int64
	// sticky maps customer (API key id) → Grok account id.
	// Same customer keeps the same upstream account across turns so multi-turn /
	// tool-loop context stays coherent (less "dumber" mid-session rotation).
	// New customers are still load-balanced via round-robin assignment.
	sticky map[string]string // customerKey → accountID
}

type grokRuntimeStats struct {
	requestCount int
	errorCount   int
	totalTokens  int
	totalCredits float64
	lastUsed     int64
}

var (
	grokPool     *GrokPool
	grokPoolOnce sync.Once
)

// GetGrokPool returns the singleton Grok pool.
func GetGrokPool() *GrokPool {
	grokPoolOnce.Do(func() {
		grokPool = &GrokPool{stats: make(map[string]*grokRuntimeStats), sticky: make(map[string]string), cooldownUntil: make(map[string]int64)}
		grokPool.Reload()
	})
	return grokPool
}

// Reload reloads enabled accounts from config.
func (p *GrokPool) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = config.GetEnabledGrokAccounts()
}

// Count returns total loaded (enabled) accounts.
func (p *GrokPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.accounts)
}

// AvailableCount is same as Count for Grok (no model filter).
func (p *GrokPool) AvailableCount() int {
	return p.Count()
}

// GetNext returns the next enabled account (round-robin), or nil.
func (p *GrokPool) GetNext() *config.GrokAccount {
	return p.GetNextExcluding(nil)
}

// GetNextExcluding skips ids present in excluded (pure round-robin).
func (p *GrokPool) GetNextExcluding(excluded map[string]bool) *config.GrokAccount {
	return p.pickRoundRobin(excluded)
}

// GetNextForCustomer returns a sticky Grok account for this customer (API key id).
//
// Policy:
//  1. If customer already pinned to an enabled account not in excluded → reuse it
//     (no mid-session rotation → better multi-turn / tool quality).
//  2. Otherwise round-robin pick a free account and pin the customer to it.
//  3. Empty customerKey → pure round-robin (no pin).
//
// Failover: caller excludes the failed id and calls again; sticky is cleared for
// that customer when the pinned account is excluded/disabled.
func (p *GrokPool) GetNextForCustomer(customerKey string, excluded map[string]bool) *config.GrokAccount {
	customerKey = strings.TrimSpace(customerKey)
	if customerKey == "" {
		return p.pickRoundRobin(excluded)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sticky == nil {
		p.sticky = make(map[string]string)
	}

	// 1) Prefer sticky pin
	if accID, ok := p.sticky[customerKey]; ok && accID != "" {
		if excluded == nil || !excluded[accID] {
			if acc := p.findEnabledLocked(accID); acc != nil {
				cp := *acc
				return &cp
			}
		}
		// pinned account dead / excluded → drop pin
		delete(p.sticky, customerKey)
	}

	// 2) Assign new via RR (must hold lock carefully: pick needs atomic index)
	// Release pattern: do RR under same lock using local index advance
	acc := p.pickRoundRobinLocked(excluded)
	if acc == nil {
		return nil
	}
	p.sticky[customerKey] = acc.ID
	return acc
}

// ClearStickyForAccount removes pins pointing at a disabled/dead account.
func (p *GrokPool) ClearStickyForAccount(accountID string) {
	if accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.sticky {
		if v == accountID {
			delete(p.sticky, k)
		}
	}
}

// ClearStickyCustomer drops pin for one customer (e.g. after hard fail).
func (p *GrokPool) ClearStickyCustomer(customerKey string) {
	customerKey = strings.TrimSpace(customerKey)
	if customerKey == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sticky, customerKey)
}

func (p *GrokPool) pickRoundRobin(excluded map[string]bool) *config.GrokAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickRoundRobinLocked(excluded)
}

// pickRoundRobinLocked requires p.mu held (R or W).
func (p *GrokPool) pickRoundRobinLocked(excluded map[string]bool) *config.GrokAccount {
	n := len(p.accounts)
	if n == 0 {
		return nil
	}
	for i := 0; i < n; i++ {
		idx := int(atomic.AddUint64(&p.index, 1)-1) % n
		acc := p.accounts[idx]
		if excluded != nil && excluded[acc.ID] {
			continue
		}
		if p.isCoolingDownLocked(acc.ID) {
			continue
		}
		if !acc.Enabled {
			continue
		}
		cp := acc
		return &cp
	}
	return nil
}

func (p *GrokPool) findEnabledLocked(id string) *config.GrokAccount {
	for i := range p.accounts {
		if p.accounts[i].ID == id && p.accounts[i].Enabled && !p.isCoolingDownLocked(id) {
			return &p.accounts[i]
		}
	}
	return nil
}
// GetByID returns a copy of the account from the pool.
func (p *GrokPool) GetByID(id string) *config.GrokAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.ID == id {
			cp := a
			return &cp
		}
	}
	// Fall back to full config (may be disabled).
	return config.GetGrokAccountByID(id)
}

// UpdateToken updates tokens in memory + config.
func (p *GrokPool) UpdateToken(id, access, refresh string, expiresAt int64) {
	p.mu.Lock()
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			p.accounts[i].AccessToken = access
			if refresh != "" {
				p.accounts[i].RefreshToken = refresh
			}
			if expiresAt > 0 {
				p.accounts[i].ExpiresAt = expiresAt
			}
			break
		}
	}
	p.mu.Unlock()
	_ = config.UpdateGrokAccountToken(id, access, refresh, expiresAt)
}

// Disable marks account disabled in config and reloads.
// Prefer Cooldown for transient 401/402 so the pool is not permanently emptied.
func (p *GrokPool) Disable(id, reason string) {
	_ = config.SetGrokAccountEnabled(id, false, reason)
	p.ClearStickyForAccount(id)
	p.Reload()
}

// Cooldown soft-bans an account in-memory for duration (default 10m). Does NOT set Enabled=false.
func (p *GrokPool) Cooldown(id, reason string, d time.Duration) {
	if id == "" {
		return
	}
	if d <= 0 {
		d = 10 * time.Minute
	}
	until := time.Now().Add(d).Unix()
	p.mu.Lock()
	if p.cooldownUntil == nil {
		p.cooldownUntil = make(map[string]int64)
	}
	p.cooldownUntil[id] = until
	p.mu.Unlock()
	p.ClearStickyForAccount(id)
	// log via reason only if non-empty - config package not used
	_ = reason
}

func (p *GrokPool) isCoolingDownLocked(id string) bool {
	if p.cooldownUntil == nil {
		return false
	}
	until, ok := p.cooldownUntil[id]
	if !ok {
		return false
	}
	if time.Now().Unix() >= until {
		delete(p.cooldownUntil, id)
		return false
	}
	return true
}

// RecordSuccess increments success stats in memory.
func (p *GrokPool) RecordSuccess(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.ensureStats(id)
	s.requestCount++
	s.lastUsed = time.Now().Unix()
}

// RecordError increments error stats.
func (p *GrokPool) RecordError(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.ensureStats(id)
	s.errorCount++
	s.requestCount++
	s.lastUsed = time.Now().Unix()
}

// UpdateStats adds tokens/credits and persists snapshot.
func (p *GrokPool) UpdateStats(id string, tokens int, credits float64) {
	p.mu.Lock()
	s := p.ensureStats(id)
	s.totalTokens += tokens
	s.totalCredits += credits
	s.lastUsed = time.Now().Unix()
	// merge with any config baseline
	acc := config.GetGrokAccountByID(id)
	req, errc, tok, cred := s.requestCount, s.errorCount, s.totalTokens, s.totalCredits
	if acc != nil {
		// Prefer cumulative: runtime stats already absolute if we seed from config on first use.
		_ = acc
	}
	last := s.lastUsed
	p.mu.Unlock()
	_ = config.UpdateGrokAccountStats(id, req, errc, tok, cred, last)
}

func (p *GrokPool) ensureStats(id string) *grokRuntimeStats {
	if p.stats == nil {
		p.stats = make(map[string]*grokRuntimeStats)
	}
	if s, ok := p.stats[id]; ok {
		return s
	}
	// seed from config
	s := &grokRuntimeStats{}
	if acc := config.GetGrokAccountByID(id); acc != nil {
		s.requestCount = acc.RequestCount
		s.errorCount = acc.ErrorCount
		s.totalTokens = acc.TotalTokens
		s.totalCredits = acc.TotalCredits
		s.lastUsed = acc.LastUsed
	}
	p.stats[id] = s
	return s
}
