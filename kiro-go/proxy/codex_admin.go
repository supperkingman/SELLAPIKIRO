// Package proxy — admin HTTP handlers for the Codex account pool + split control.
// Mirrors the Grok admin API (apiGetGrokAccounts, apiAddGrokAccount, ...).
package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"kiro-go/config"
	"kiro-go/logger"
	"kiro-go/pool"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// --- Admin Codex split-percentage API ---

func (h *Handler) apiGetCodexSplit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]int{"percent": config.GetCodexSplitPercent()})
}

func (h *Handler) apiSetCodexSplit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Percent int `json:"percent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Percent < 0 || body.Percent > 100 {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "percent must be between 0 and 100"})
		return
	}
	if err := config.UpdateCodexSplitPercent(body.Percent); err != nil {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "percent": config.GetCodexSplitPercent()})
}

// --- Admin Codex accounts API ---

func (h *Handler) apiGetCodexAccounts(w http.ResponseWriter, r *http.Request) {
	accs := config.GetCodexAccounts()
	type view struct {
		ID             string  `json:"id"`
		Email          string  `json:"email"`
		Nickname       string  `json:"nickname"`
		DisplayName    string  `json:"displayName"`
		Enabled        bool    `json:"enabled"`
		ExpiresAt      int64   `json:"expiresAt"`
		AuthMethod     string  `json:"authMethod"`
		PlanType       string  `json:"planType,omitempty"`
		RequestCount   int     `json:"requestCount"`
		ErrorCount     int     `json:"errorCount"`
		TotalTokens    int     `json:"totalTokens"`
		TotalCredits   float64 `json:"totalCredits"`
		BanStatus      string  `json:"banStatus,omitempty"`
		BanReason      string  `json:"banReason,omitempty"`
		HasRefresh     bool    `json:"hasRefreshToken"`
		MachineId      string  `json:"machineId,omitempty"`
		ProxyURL       string  `json:"proxyURL,omitempty"`
		LastUsed       int64   `json:"lastUsed,omitempty"`
		QuotaStatus    string  `json:"quotaStatus,omitempty"`
		QuotaMessage   string  `json:"quotaMessage,omitempty"`
		QuotaCheckedAt int64   `json:"quotaCheckedAt,omitempty"`
		AddedAt        int64   `json:"addedAt,omitempty"`
		Warming        bool    `json:"warming,omitempty"`
		WarmupMaxConc  int     `json:"warmupMaxConcurrent,omitempty"`
		WarmupSpacing  int     `json:"warmupMinSpacingSec,omitempty"`
	}
	cpStats := pool.GetCodexPool()
	out := make([]view, 0, len(accs))
	for _, a := range accs {
		req, errc, tok, cred, last := a.RequestCount, a.ErrorCount, a.TotalTokens, a.TotalCredits, a.LastUsed
		if r, e, t, c, l, ok := cpStats.SnapshotStats(a.ID); ok {
			req, errc, tok, cred, last = r, e, t, c, l
		}
		wi := cpStats.WarmupInfo(a.ID)
		out = append(out, view{
			ID: a.ID, Email: a.Email, Nickname: a.Nickname, DisplayName: a.DisplayName,
			Enabled: a.Enabled, ExpiresAt: a.ExpiresAt, AuthMethod: a.AuthMethod,
			PlanType:     a.ChatgptPlanType,
			RequestCount: req, ErrorCount: errc,
			TotalTokens: tok, TotalCredits: cred,
			BanStatus: a.BanStatus, BanReason: a.BanReason,
			HasRefresh: a.RefreshToken != "",
			MachineId:  a.MachineId, ProxyURL: a.ProxyURL, LastUsed: last,
			QuotaStatus: a.QuotaStatus, QuotaMessage: a.QuotaMessage, QuotaCheckedAt: a.QuotaCheckedAt,
			AddedAt: a.AddedAt, Warming: wi.Warming,
			WarmupMaxConc: wi.MaxConcurrent, WarmupSpacing: wi.MinSpacingSec,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"accounts": out, "count": len(out)})
}

func (h *Handler) apiAddCodexAccount(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "read body failed"})
		return
	}
	acc, err := parseCodexAccountJSON(raw)
	if err != nil {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if acc.RefreshToken == "" && acc.AccessToken == "" {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing accessToken/refreshToken"})
		return
	}
	if acc.ID == "" {
		acc.ID = uuid.New().String()
	}
	acc.Enabled = true
	var probe map[string]interface{}
	_ = json.Unmarshal(raw, &probe)
	if v, ok := probe["enabled"].(bool); ok {
		acc.Enabled = v
	}
	existed := config.CodexAccountIDExists(acc.ID)
	if err := config.AddCodexAccount(*acc); err != nil {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	pool.GetCodexPool().Reload()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "id": acc.ID, "updated": existed})
}

func (h *Handler) apiDeleteCodexAccount(w http.ResponseWriter, r *http.Request, id string) {
	if err := config.DeleteCodexAccount(id); err != nil {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	pool.GetCodexPool().Reload()
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (h *Handler) apiSetCodexAccountEnabled(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Enabled = false
	}
	reason := body.Reason
	if !body.Enabled && reason == "" {
		reason = "disabled by admin"
	}
	if err := config.SetCodexAccountEnabled(id, body.Enabled, reason); err != nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	pool.GetCodexPool().Reload()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "enabled": body.Enabled})
}

func (h *Handler) apiGetCodexAccount(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	acc := config.GetCodexAccountByID(id)
	if acc == nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	req, errc, tok, cred, last := acc.RequestCount, acc.ErrorCount, acc.TotalTokens, acc.TotalCredits, acc.LastUsed
	if r, e, t, c, l, ok := pool.GetCodexPool().SnapshotStats(acc.ID); ok {
		req, errc, tok, cred, last = r, e, t, c, l
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id": acc.ID, "email": acc.Email, "nickname": acc.Nickname, "displayName": acc.DisplayName,
		"enabled": acc.Enabled, "expiresAt": acc.ExpiresAt, "authMethod": acc.AuthMethod,
		"planType": acc.ChatgptPlanType, "chatgptAccountId": acc.ChatgptAccountId,
		"requestCount": req, "errorCount": errc,
		"totalTokens": tok, "totalCredits": cred,
		"banStatus": acc.BanStatus, "banReason": acc.BanReason,
		"hasRefreshToken": acc.RefreshToken != "",
		"machineId":       acc.MachineId, "proxyURL": acc.ProxyURL,
		"lastUsed": last, "clientId": acc.ClientID,
		"quotaStatus": acc.QuotaStatus, "quotaMessage": acc.QuotaMessage, "quotaCheckedAt": acc.QuotaCheckedAt,
	})
}

func (h *Handler) apiPatchCodexAccount(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	if id == "" {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing id"})
		return
	}
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}
	var machineId, proxyURL, nickname, displayName *string
	if v, ok := body["machineId"].(string); ok {
		machineId = &v
	}
	if v, ok := body["proxyURL"].(string); ok {
		v = strings.TrimSpace(v)
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") &&
			!strings.HasPrefix(v, "socks5://") && !strings.HasPrefix(v, "socks5h://") {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "proxyURL must start with http://, https://, socks5://, or socks5h://"})
			return
		}
		proxyURL = &v
	}
	if v, ok := body["nickname"].(string); ok {
		nickname = &v
	}
	if v, ok := body["displayName"].(string); ok {
		displayName = &v
	}
	if machineId == nil && proxyURL == nil && nickname == nil && displayName == nil {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no fields to update (machineId, proxyURL, nickname, displayName)"})
		return
	}
	if err := config.PatchCodexAccountFields(id, machineId, proxyURL, nickname, displayName); err != nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	pool.GetCodexPool().Reload()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "id": id})
}

func (h *Handler) apiTestCodexAccount(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	acc := config.GetCodexAccountByID(id)
	if acc == nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	res := h.testCodexAccountHello(acc)
	if ok, _ := res["ok"].(bool); !ok {
		w.WriteHeader(502)
	}
	_ = json.NewEncoder(w).Encode(res)
}

func (h *Handler) apiTestAllCodexAccounts(w http.ResponseWriter, r *http.Request) {
	onlyEnabled := r.URL.Query().Get("all") != "1"
	accs := config.GetCodexAccounts()
	results := make([]map[string]interface{}, 0, len(accs))
	okN, failN := 0, 0
	for i := range accs {
		a := accs[i]
		if onlyEnabled && !a.Enabled {
			results = append(results, map[string]interface{}{
				"id": a.ID, "email": a.Email, "enabled": false, "ok": false, "status": "skipped", "skipped": true,
			})
			continue
		}
		acc := a
		res := h.testCodexAccountHello(&acc)
		if ok, _ := res["ok"].(bool); ok {
			okN++
		} else {
			failN++
		}
		results = append(results, res)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true, "count": len(results), "ok": okN, "failed": failN, "results": results,
	})
}

// StartCodexHealthChecker re-tests cooling-down accounts and clears cooldown on recovery.
func (h *Handler) StartCodexHealthChecker() {
	go func() {
		ticker := time.NewTicker(codexHealthCheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			cp := pool.GetCodexPool()
			cooling := cp.CoolingDownAccounts()
			if len(cooling) == 0 {
				continue
			}
			for i := range cooling {
				acc := cooling[i]
				_ = h.refreshCodexToken(&acc)
				res := h.testCodexAccountHello(&acc)
				if ok, _ := res["ok"].(bool); ok {
					cp.ClearCooldown(acc.ID)
					_ = config.SetCodexAccountQuota(acc.ID, "active", "", -1, 0)
					logger.Infof("[CodexHealth] account=%s recovered — cooldown cleared", acc.Email)
				}
			}
		}
	}()
	logger.Infof("[CodexHealth] background health-checker started (interval=%s)", codexHealthCheckInterval)
}

// parseCodexAccountJSON accepts either a flat kiro-go object or a nested 9router
// providerConnections export (provider=codex). Normalizes expiresAt to Unix seconds.
func parseCodexAccountJSON(raw []byte) (*config.CodexAccount, error) {
	var flat struct {
		ID           string      `json:"id"`
		Provider     string      `json:"provider"`
		AuthType     string      `json:"authType"`
		Name         string      `json:"name"`
		Email        string      `json:"email"`
		Nickname     string      `json:"nickname"`
		DisplayName  string      `json:"displayName"`
		AccessToken  string      `json:"accessToken"`
		RefreshToken string      `json:"refreshToken"`
		IDToken      string      `json:"idToken"`
		ExpiresAt    interface{} `json:"expiresAt"`
		ExpiresIn    int64       `json:"expiresIn"`
		ClientID     string      `json:"clientId"`
		AuthMethod   string      `json:"authMethod"`
		Enabled      *bool       `json:"enabled"`
		// flat codex-specific
		ChatgptAccountId string `json:"chatgptAccountId"`
		ChatgptPlanType  string `json:"chatgptPlanType"`
		// nested 9router style
		Data                 json.RawMessage `json:"data"`
		ProviderSpecificData *struct {
			ChatgptAccountId string `json:"chatgptAccountId"`
			ChatgptPlanType  string `json:"chatgptPlanType"`
		} `json:"providerSpecificData"`
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	// If data is a nested object (full connection export), merge it.
	if len(flat.Data) > 0 && flat.AccessToken == "" {
		var nested map[string]interface{}
		if json.Unmarshal(flat.Data, &nested) == nil {
			if v, ok := nested["accessToken"].(string); ok {
				flat.AccessToken = v
			}
			if v, ok := nested["refreshToken"].(string); ok {
				flat.RefreshToken = v
			}
			if v, ok := nested["idToken"].(string); ok {
				flat.IDToken = v
			}
			if v, ok := nested["displayName"].(string); ok {
				flat.DisplayName = v
			}
			if v, ok := nested["expiresAt"]; ok {
				flat.ExpiresAt = v
			}
			if v, ok := nested["expiresIn"].(float64); ok {
				flat.ExpiresIn = int64(v)
			}
			if ps, ok := nested["providerSpecificData"].(map[string]interface{}); ok {
				if v, ok := ps["chatgptAccountId"].(string); ok {
					flat.ChatgptAccountId = v
				}
				if v, ok := ps["chatgptPlanType"].(string); ok {
					flat.ChatgptPlanType = v
				}
			}
		}
	}
	if flat.ProviderSpecificData != nil {
		if flat.ChatgptAccountId == "" {
			flat.ChatgptAccountId = flat.ProviderSpecificData.ChatgptAccountId
		}
		if flat.ChatgptPlanType == "" {
			flat.ChatgptPlanType = flat.ProviderSpecificData.ChatgptPlanType
		}
	}
	if flat.Email == "" {
		flat.Email = flat.Name
	}
	if flat.Nickname == "" {
		flat.Nickname = flat.Email
	}
	acc := &config.CodexAccount{
		ID:               flat.ID,
		Email:            flat.Email,
		Nickname:         flat.Nickname,
		DisplayName:      flat.DisplayName,
		AccessToken:      flat.AccessToken,
		RefreshToken:     flat.RefreshToken,
		IDToken:          flat.IDToken,
		ClientID:         flat.ClientID,
		AuthMethod:       flat.AuthMethod,
		ChatgptAccountId: flat.ChatgptAccountId,
		ChatgptPlanType:  flat.ChatgptPlanType,
		Enabled:          true,
	}
	if flat.Enabled != nil {
		acc.Enabled = *flat.Enabled
	}
	acc.ExpiresAt = parseExpiresAt(flat.ExpiresAt, flat.ExpiresIn)
	if acc.ClientID == "" {
		acc.ClientID = config.DefaultCodexClientID
	}
	if acc.AuthMethod == "" {
		acc.AuthMethod = "oauth"
	}
	return acc, nil
}

// testCodexAccountHello sends a tiny stream request to verify the account works.
// ChatGPT Codex rejects max_output_tokens — do not set it here.
func (h *Handler) testCodexAccountHello(acc *config.CodexAccount) map[string]interface{} {
	out := map[string]interface{}{
		"id": acc.ID, "email": acc.Email, "enabled": acc.Enabled, "ok": false,
		"proxyURL": acc.ProxyURL, "proxyConfigured": strings.TrimSpace(acc.ProxyURL) != "",
	}
	if err := h.ensureValidCodexToken(acc); err != nil {
		out["status"] = "token_error"
		out["error"] = truncateStr(err.Error(), 160)
		return out
	}
	probe := &OpenAIRequest{
		Model:    "gpt-5.6-sol",
		Messages: []OpenAIMessage{{Role: "user", Content: "hi"}},
		Stream:   true,
	}
	body := buildCodexRequestBody(probe, "gpt-5.6-sol", "low", "gpt-5.6-sol")
	raw, _ := json.Marshal(body)
	httpReq, err := http.NewRequest(http.MethodPost, codexResponsesURL, strings.NewReader(string(raw)))
	if err != nil {
		out["status"] = "build_error"
		out["error"] = err.Error()
		return out
	}
	httpReq.Header = buildCodexHeaders(acc, uuid.New().String())
	resp, err := getCodexHTTPClient(acc).Do(httpReq)
	if err != nil {
		out["status"] = "network_error"
		out["error"] = truncateStr(err.Error(), 160)
		return out
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out["status"] = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		out["ok"] = true
	} else {
		// Never leak provider identity to the client; short label only.
		out["error"] = fmt.Sprintf("HTTP %d", resp.StatusCode)
		logger.Warnf("[Codex] test account=%s HTTP %d body=%s", acc.Email, resp.StatusCode, truncateStr(string(b), 200))
	}
	return out
}

var _ = strconv.Atoi
