package pool

import (
	"kiro-go/config"
	"sync"
	"sync/atomic"
	"time"
)

// CodexPool is a least-in-flight pool for OpenAI Codex (ChatGPT) accounts.
// Mirrors GrokPool so both providers behave identically.
type CodexPool struct {
	mu       sync.RWMutex
	accounts []config.CodexAccount
	index    uint64
	stats    map[string]*codexRuntimeStats
	// temporary soft-ban: account id -> unix expiry (not persisted).
	cooldownUntil map[string]int64
	// inFlight counts concurrent live requests per account.
	inFlight map[string]int
}

type codexRuntimeStats struct {
	requestCount int
	errorCount   int
	totalTokens  int
	totalCredits float64
	lastUsed     int64
}

var (
	codexPool     *CodexPool
	codexPoolOnce sync.Once
)

// GetCodexPool returns the singleton Codex pool.
func GetCodexPool() *CodexPool {
	codexPoolOnce.Do(func() {
		codexPool = &CodexPool{stats: make(map[string]*codexRuntimeStats), cooldownUntil: make(map[string]int64), inFlight: make(map[string]int)}
		codexPool.Reload()
	})
	return codexPool
}

// Reload reloads enabled accounts from config.
func (p *CodexPool) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = config.GetEnabledCodexAccounts()
}

// Count returns total loaded (enabled) accounts.
func (p *CodexPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.accounts)
}

// AvailableCount counts enabled accounts not in temporary cooldown.
func (p *CodexPool) AvailableCount() int {
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

// GetNext returns the next enabled account (least-in-flight), or nil.
func (p *CodexPool) GetNext() *config.CodexAccount {
	return p.GetNextExcluding(nil)
}

// GetNextExcluding skips ids present in excluded.
func (p *CodexPool) GetNextExcluding(excluded map[string]bool) *config.CodexAccount {
	return p.pickRoundRobin(excluded)
}

// PickIgnoringCooldown is a LAST-RESORT pick that ignores temporary cooldowns.
func (p *CodexPool) PickIgnoringCooldown(excluded map[string]bool) *config.CodexAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.accounts)
	if n == 0 {
		return nil
	}
	var best *config.CodexAccount
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

// ClearCooldown removes a temporary cooldown.
func (p *CodexPool) ClearCooldown(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	if p.cooldownUntil != nil {
		delete(p.cooldownUntil, id)
	}
	p.mu.Unlock()
}

// GetNextForCustomer selects the next Codex account (least in-flight, no sticky).
func (p *CodexPool) GetNextForCustomer(customerKey string, excluded map[string]bool) *config.CodexAccount {
	_ = customerKey
	return p.pickRoundRobin(excluded)
}

// ClearStickyForAccount is a no-op (least-in-flight only).
func (p *CodexPool) ClearStickyForAccount(accountID string) { _ = accountID }

// ClearStickyCustomer is a no-op (least-in-flight only).
func (p *CodexPool) ClearStickyCustomer(customerKey string) { _ = customerKey }

func (p *CodexPool) pickRoundRobin(excluded map[string]bool) *config.CodexAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickRoundRobinLocked(excluded)
}

// pickRoundRobinLocked requires p.mu held. Prefers accounts with fewer in-flight requests.
func (p *CodexPool) pickRoundRobinLocked(excluded map[string]bool) *config.CodexAccount {
	n := len(p.accounts)
	if n == 0 {
		return nil
	}
	var best *config.CodexAccount
	bestLoad := int(^uint(0) >> 1)
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
				break
			}
		}
	}
	return best
}

// GetByID returns a copy of the account from the pool (falls back to config).
func (p *CodexPool) GetByID(id string) *config.CodexAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, a := range p.accounts {
		if a.ID == id {
			cp := a
			return &cp
		}
	}
	return config.GetCodexAccountByID(id)
}

// UpdateToken updates tokens in memory + config.
func (p *CodexPool) UpdateToken(id, access, refresh string, expiresAt int64) {
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
	_ = config.UpdateCodexAccountToken(id, access, refresh, expiresAt)
}

// Acquire marks an account as in-use for concurrent load balancing.
func (p *CodexPool) Acquire(id string) {
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

// Release decrements in-flight after a request finishes.
func (p *CodexPool) Release(id string) {
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

func (p *CodexPool) inFlightLocked(id string) int {
	if p.inFlight == nil {
		return 0
	}
	return p.inFlight[id]
}

// Disable marks account disabled in config and reloads.
func (p *CodexPool) Disable(id, reason string) {
	_ = config.SetCodexAccountEnabled(id, false, reason)
	p.Reload()
}

// Cooldown soft-bans an account in-memory for duration (default 10m).
func (p *CodexPool) Cooldown(id, reason string, d time.Duration) {
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
	_ = reason
}

func (p *CodexPool) isCoolingDownLocked(id string) bool {
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
func (p *CodexPool) CoolingDownAccounts() []config.CodexAccount {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []config.CodexAccount
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
func (p *CodexPool) RecordSuccess(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	s := p.ensureStats(id)
	s.requestCount++
	s.lastUsed = time.Now().Unix()
	req, errc, tok, cred, last := s.requestCount, s.errorCount, s.totalTokens, s.totalCredits, s.lastUsed
	p.mu.Unlock()
	_ = config.UpdateCodexAccountStats(id, req, errc, tok, cred, last)
}

// RecordError increments error stats in memory and persists counters.
func (p *CodexPool) RecordError(id string) {
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
	_ = config.UpdateCodexAccountStats(id, req, errc, tok, cred, last)
}

// SnapshotStats returns runtime counters for an account if present.
func (p *CodexPool) SnapshotStats(id string) (requestCount, errorCount, totalTokens int, totalCredits float64, lastUsed int64, ok bool) {
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
func (p *CodexPool) UpdateStats(id string, tokens int, credits float64) {
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
	_ = config.UpdateCodexAccountStats(id, req, errc, tok, cred, last)
}

func (p *CodexPool) ensureStats(id string) *codexRuntimeStats {
	if p.stats == nil {
		p.stats = make(map[string]*codexRuntimeStats)
	}
	if s, ok := p.stats[id]; ok {
		return s
	}
	s := &codexRuntimeStats{}
	if acc := config.GetCodexAccountByID(id); acc != nil {
		s.requestCount = acc.RequestCount
		s.errorCount = acc.ErrorCount
		s.totalTokens = acc.TotalTokens
		s.totalCredits = acc.TotalCredits
		s.lastUsed = acc.LastUsed
	}
	p.stats[id] = s
	return s
}
