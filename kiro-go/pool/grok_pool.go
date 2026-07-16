package pool

import (
	"kiro-go/config"
	"sync"
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
	// inFlight counts concurrent live requests per account (spread load across pool).
	inFlight map[string]int
	// quota402Streak counts CONSECUTIVE upstream 402 (quota exhausted) responses per
	// account. Reset to 0 on any success. When it reaches the disable threshold the
	// account is disabled (Enabled=false) so a genuinely exhausted account is pulled
	// from rotation immediately, while a one-off transient 402 does not.
	quota402Streak map[string]int
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
		grokPool = &GrokPool{stats: make(map[string]*grokRuntimeStats), cooldownUntil: make(map[string]int64), inFlight: make(map[string]int)}
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

// AvailableCount counts enabled accounts not in temporary cooldown.
func (p *GrokPool) AvailableCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for i := range p.accounts {
		if !p.accounts[i].Enabled {
			continue
		}
		if p.isCoolingDownLocked(p.accounts[i].ID) {
			continue
		}
		n++
	}
	return n
}

// GetNext returns the next enabled account (round-robin), or nil.
func (p *GrokPool) GetNext() *config.GrokAccount {
	return p.GetNextExcluding(nil)
}

// GetNextExcluding skips ids present in excluded (pure round-robin).
func (p *GrokPool) GetNextExcluding(excluded map[string]bool) *config.GrokAccount {
	return p.pickRoundRobin(excluded)
}

// PickIgnoringCooldown is a LAST-RESORT pick that ignores temporary cooldowns.
// Used when a fresh request finds every account cooling down (e.g. a previous
// request soft-banned the whole pool after transient proxy/auth errors). Without
// this, the proxy returns 503 for the entire cooldown window even though the
// accounts may work again. Still honors Enabled and the caller's excluded set.
func (p *GrokPool) PickIgnoringCooldown(excluded map[string]bool) *config.GrokAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.accounts)
	if n == 0 {
		return nil
	}
	var best *config.GrokAccount
	bestLoad := int(^uint(0) >> 1)
	start := int(atomic.AddUint64(&p.index, 1)-1) % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		acc := p.accounts[idx]
		if excluded != nil && excluded[acc.ID] {
			continue
		}
		if !acc.Enabled {
			continue
		}
		load := p.inFlightLocked(acc.ID)
		if load < bestLoad {
			bestLoad = load
			cp := acc
			best = &cp
			if load == 0 {
				break
			}
		}
	}
	return best
}

// ClearCooldown removes a temporary cooldown (used when an account succeeds again
// or when we want to force a retry after the cooldown window looks stale).
func (p *GrokPool) ClearCooldown(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	if p.cooldownUntil != nil {
		delete(p.cooldownUntil, id)
	}
	p.mu.Unlock()
}

// GetNextForCustomer selects the next Grok account for a request.
//
// Policy (no sticky): every request picks the enabled account with the least
// in-flight load (scan starts at a rotating index for fairness). customerKey is
// accepted for API compatibility but is not used for pinning.
// Failover: caller excludes failed ids and calls again.
func (p *GrokPool) GetNextForCustomer(customerKey string, excluded map[string]bool) *config.GrokAccount {
	_ = customerKey
	return p.pickRoundRobin(excluded)
}


// ClearStickyForAccount is a no-op (sticky pinning removed; least-in-flight only).
func (p *GrokPool) ClearStickyForAccount(accountID string) {
	_ = accountID
}

// ClearStickyCustomer is a no-op (sticky pinning removed; least-in-flight only).
func (p *GrokPool) ClearStickyCustomer(customerKey string) {
	_ = customerKey
}


func (p *GrokPool) pickRoundRobin(excluded map[string]bool) *config.GrokAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickRoundRobinLocked(excluded)
}

// pickRoundRobinLocked requires p.mu held (R or W).
// Prefers accounts with fewer in-flight requests so concurrent customers
// fan out across the pool (no per-customer sticky pin).
func (p *GrokPool) pickRoundRobinLocked(excluded map[string]bool) *config.GrokAccount {
	n := len(p.accounts)
	if n == 0 {
		return nil
	}
	var best *config.GrokAccount
	bestLoad := int(^uint(0) >> 1)
	// Scan all accounts starting from RR index for fairness.
	start := int(atomic.AddUint64(&p.index, 1)-1) % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
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
		load := p.inFlightLocked(acc.ID)
		if load < bestLoad {
			bestLoad = load
			cp := acc
			best = &cp
			if load == 0 {
				break // perfect free account
			}
		}
	}
	return best
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

// Acquire marks an account as in-use for concurrent load balancing.
func (p *GrokPool) Acquire(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	if p.inFlight == nil {
		p.inFlight = make(map[string]int)
	}
	p.inFlight[id]++
	p.mu.Unlock()
}

// Release decrements in-flight after a request finishes (success or fail).
func (p *GrokPool) Release(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	if p.inFlight != nil {
		if n := p.inFlight[id]; n <= 1 {
			delete(p.inFlight, id)
		} else {
			p.inFlight[id] = n - 1
		}
	}
	p.mu.Unlock()
}

func (p *GrokPool) inFlightLocked(id string) int {
	if p.inFlight == nil {
		return 0
	}
	return p.inFlight[id]
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

// CoolingDownAccounts returns copies of enabled accounts currently in cooldown.
// Used by the background health-checker to re-test them and clear the cooldown
// early once the account works again (xAI permission-denied is transient).
func (p *GrokPool) CoolingDownAccounts() []config.GrokAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []config.GrokAccount
	for i := range p.accounts {
		if !p.accounts[i].Enabled {
			continue
		}
		if p.isCoolingDownLocked(p.accounts[i].ID) {
			out = append(out, p.accounts[i])
		}
	}
	return out
}

// RecordSuccess increments success stats in memory and persists counters.
func (p *GrokPool) RecordSuccess(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	s := p.ensureStats(id)
	s.requestCount++
	s.lastUsed = time.Now().Unix()
	if p.quota402Streak != nil {
		delete(p.quota402Streak, id) // a success clears any consecutive-402 streak
	}
	req, errc, tok, cred, last := s.requestCount, s.errorCount, s.totalTokens, s.totalCredits, s.lastUsed
	p.mu.Unlock()
	_ = config.UpdateGrokAccountStats(id, req, errc, tok, cred, last)
}

// RecordQuota402 increments the consecutive-402 streak for an account and returns
// the new streak count. Callers compare it against the disable threshold to decide
// whether to pull the account from rotation. The streak is reset by RecordSuccess.
func (p *GrokPool) RecordQuota402(id string) int {
	if id == "" {
		return 0
	}
	p.mu.Lock()
	if p.quota402Streak == nil {
		p.quota402Streak = make(map[string]int)
	}
	p.quota402Streak[id]++
	n := p.quota402Streak[id]
	p.mu.Unlock()
	return n
}

// ResetQuota402 clears the consecutive-402 streak for an account (e.g. after the
// account recovers, or is disabled and no longer needs tracking).
func (p *GrokPool) ResetQuota402(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	if p.quota402Streak != nil {
		delete(p.quota402Streak, id)
	}
	p.mu.Unlock()
}

// RecordError increments error stats in memory and persists counters.
func (p *GrokPool) RecordError(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	s := p.ensureStats(id)
	s.errorCount++
	s.requestCount++
	s.lastUsed = time.Now().Unix()
	req, errc, tok, cred, last := s.requestCount, s.errorCount, s.totalTokens, s.totalCredits, s.lastUsed
	p.mu.Unlock()
	_ = config.UpdateGrokAccountStats(id, req, errc, tok, cred, last)
}

// SnapshotStats returns runtime counters for an account if present.
func (p *GrokPool) SnapshotStats(id string) (requestCount, errorCount, totalTokens int, totalCredits float64, lastUsed int64, ok bool) {
	if id == "" {
		return 0, 0, 0, 0, 0, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.stats == nil {
		return 0, 0, 0, 0, 0, false
	}
	s, exists := p.stats[id]
	if !exists || s == nil {
		return 0, 0, 0, 0, 0, false
	}
	return s.requestCount, s.errorCount, s.totalTokens, s.totalCredits, s.lastUsed, true
}

// UpdateStats adds tokens/credits and persists snapshot.
func (p *GrokPool) UpdateStats(id string, tokens int, credits float64) {
	if id == "" {
		return
	}
	p.mu.Lock()
	s := p.ensureStats(id)
	s.totalTokens += tokens
	s.totalCredits += credits
	s.lastUsed = time.Now().Unix()
	req, errc, tok, cred := s.requestCount, s.errorCount, s.totalTokens, s.totalCredits
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
