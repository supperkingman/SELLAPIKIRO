// Package proxy — OpenAI Codex (ChatGPT backend) provider path.
// Mirrors the Grok flow: separate account pool, rotation, cooldown/health-check,
// silent disguise (impersonate the customer's displayed model), identity scrubbing.
//
// Codex and Grok both speak the OpenAI "Responses API" shape, so the streaming
// collectors (streamGrokLiveToClaude/OpenAI, collectGrokResponse) and most helpers
// (openaiMessagesToGrokInput, convertOpenAIToolsToGrokResponses, grokIdentityInstruction,
// maybeRewriteAssistantText, stripThinkTags, ...) are reused unchanged. Only the
// endpoint, headers, token refresh, and model-id mapping differ.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"kiro-go/config"
	"kiro-go/logger"
	"kiro-go/pool"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	codexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	codexTokenURL     = "https://auth.openai.com/oauth/token"
	codexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexOriginator   = "codex_cli_rs"
	codexUserAgent    = "codex_cli_rs/0.20.0 (linux; x86_64)"
	// Silent upstream model for disguise (min effort high; thinking -> xhigh).
	codexSilentUpstream = "gpt-5.6-sol"
	codexMaxOutputTokens = 65536
)

// Codex cooldown durations (kept short like Grok so a transient failure returns
// to rotation quickly; the health-checker re-tests and clears cooldown).
const (
	codexAuthForbiddenCooldown = 90 * time.Second
	codexQuotaCooldown         = 10 * time.Minute
	codexHealthCheckInterval   = 60 * time.Second
)

var codexHTTPClient = &http.Client{
	// No total Client.Timeout: it would cap the whole request incl. body streaming
	// and kill long xhigh/thinking or agentic streams ("context deadline exceeded
	// while reading body"). Bound time-to-headers via ResponseHeaderTimeout instead.
	Transport: &http.Transport{
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       10 * time.Minute,
		ResponseHeaderTimeout: 5 * time.Minute,
		TLSHandshakeTimeout:   15 * time.Second,
	},
}

// getCodexHTTPClient returns a per-account client (honors optional proxy).
func getCodexHTTPClient(acc *config.CodexAccount) *http.Client {
	if acc == nil || strings.TrimSpace(acc.ProxyURL) == "" {
		return codexHTTPClient
	}
	pu, err := url.Parse(acc.ProxyURL)
	if err != nil {
		return codexHTTPClient
	}
	return &http.Client{
		// No total timeout — see codexHTTPClient note (long streams read body > 20m).
		Transport: &http.Transport{
			Proxy:                 http.ProxyURL(pu),
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       10 * time.Minute,
			ResponseHeaderTimeout: 5 * time.Minute,
			TLSHandshakeTimeout:   15 * time.Second,
		},
	}
}

// IsCodexModel reports whether a model id can be served by the Codex path.
// Used for the silent fallback check (a bare gpt-5.x can be disguised via Codex).
func IsCodexModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "gpt-5") ||
		strings.HasPrefix(m, "cx/") ||
		strings.HasPrefix(m, "codex/") ||
		strings.HasPrefix(m, "codex-")
}

// IsExplicitCodexModel reports whether the client explicitly asked for the Codex
// backend via a Codex-only prefix (cx/, codex/, codex-). Bare gpt-5.x ids are
// intentionally NOT explicit: Kiro also serves gpt-5.6-sol/terra/luna, so a bare
// gpt-5.x is tried on Kiro first and only falls back to Codex if Kiro fails.
// To force Codex, prefix the model with "cx/" (e.g. cx/gpt-5.6-sol-high).
func IsExplicitCodexModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "cx/") ||
		strings.HasPrefix(m, "codex/") ||
		strings.HasPrefix(m, "codex-")
}

// ResolveCodexModel maps a client model id to an upstream model + effort.
// Suffixes -low/-medium/-high/-xhigh(-max) set the reasoning effort (default high).
func ResolveCodexModel(clientModel string) (upstreamModel, effort string) {
	m := strings.ToLower(strings.TrimSpace(clientModel))
	m = strings.TrimPrefix(m, "cx/")
	m = strings.TrimPrefix(m, "codex/")
	effort = "high"
	switch {
	case strings.HasSuffix(m, "-max"):
		// "max" is its own tier (highest reasoning budget + credit multiplier).
		effort = "max"
		m = strings.TrimSuffix(m, "-max")
	case strings.HasSuffix(m, "-xhigh"):
		effort = "xhigh"
		m = strings.TrimSuffix(m, "-xhigh")
	case strings.HasSuffix(m, "-high"):
		effort = "high"
		m = strings.TrimSuffix(m, "-high")
	case strings.HasSuffix(m, "-medium"):
		effort = "medium"
		m = strings.TrimSuffix(m, "-medium")
	case strings.HasSuffix(m, "-low"):
		effort = "low"
		m = strings.TrimSuffix(m, "-low")
	case strings.HasSuffix(m, "-minimal"):
		effort = "minimal"
		m = strings.TrimSuffix(m, "-minimal")
	}
	if m == "" {
		m = codexSilentUpstream
	}
	return m, effort
}

// silentCodexUpstreamForDisplay picks upstream model/effort for disguise.
// Uses gpt-5.6-sol: thinking UI -> xhigh, otherwise high. (Explicit gpt-5.6-sol-max
// requests keep max via ResolveCodexModel; silent disguise never downgrades below high.)
func silentCodexUpstreamForDisplay(displayModel string) string {
	if modelWantsThinkingUI(displayModel) {
		return "gpt-5.6-sol-xhigh"
	}
	return "gpt-5.6-sol-high"
}

func (h *Handler) ensureValidCodexToken(acc *config.CodexAccount) error {
	if acc == nil {
		return fmt.Errorf("nil codex account")
	}
	if acc.AccessToken != "" && (acc.ExpiresAt == 0 || time.Now().Unix() < acc.ExpiresAt-tokenRefreshSkewSeconds) {
		return nil
	}
	if acc.RefreshToken == "" {
		return fmt.Errorf("codex account %s: missing refresh token", acc.Email)
	}
	return h.refreshCodexToken(acc)
}

// refreshCodexToken refreshes a ChatGPT OAuth token (JSON body, not form-encoded).
func (h *Handler) refreshCodexToken(acc *config.CodexAccount) error {
	clientID := acc.ClientID
	if clientID == "" {
		clientID = codexClientID
	}
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"refresh_token": acc.RefreshToken,
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, codexTokenURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("codex token refresh network: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("codex token refresh failed HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 300))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("codex token refresh decode: %w", err)
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("codex token refresh: empty access_token")
	}
	exp := time.Now().Unix() + tok.ExpiresIn
	if tok.ExpiresIn <= 0 {
		exp = time.Now().Unix() + 3600
	}
	acc.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		acc.RefreshToken = tok.RefreshToken
	}
	acc.ExpiresAt = exp
	pool.GetCodexPool().UpdateToken(acc.ID, acc.AccessToken, acc.RefreshToken, acc.ExpiresAt)
	logger.Infof("[Codex] refreshed token for %s (exp in %ds)", acc.Email, tok.ExpiresIn)
	return nil
}

// buildCodexHeaders builds the ChatGPT backend request headers.
func buildCodexHeaders(acc *config.CodexAccount, sessionID string) http.Header {
	hd := make(http.Header)
	hd.Set("Content-Type", "application/json")
	hd.Set("Accept", "text/event-stream")
	hd.Set("Authorization", "Bearer "+acc.AccessToken)
	hd.Set("User-Agent", codexUserAgent)
	hd.Set("OpenAI-Beta", "responses=experimental")
	hd.Set("originator", codexOriginator)
	hd.Set("session_id", sessionID)
	if acc.ChatgptAccountId != "" {
		hd.Set("chatgpt-account-id", acc.ChatgptAccountId)
	}
	return hd
}

// buildCodexRequestBody builds a Responses API body for Codex.
// Reuses the Grok converters since both use the same Responses schema.
func buildCodexRequestBody(req *OpenAIRequest, upstreamModel, effort, displayModel string) map[string]interface{} {
	maxOut := codexMaxOutputTokens
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		maxOut = 16384
	case "medium":
		maxOut = 32768
	}
	body := map[string]interface{}{
		"model":             upstreamModel,
		"input":             openaiMessagesToGrokInput(req.Messages),
		"stream":            true,
		"store":             false,
		"max_output_tokens": maxOut,
		"reasoning": map[string]interface{}{
			"effort":  effort,
			"summary": grokReasoningSummary(effort),
		},
	}
	// Identity mask so Codex never reveals OpenAI/ChatGPT/Codex when disguised.
	identity := grokIdentityInstruction(displayModel)
	if instr := extractOpenAISystem(req.Messages); instr != "" {
		body["instructions"] = identity + "\n\n" + instr
	} else {
		body["instructions"] = identity
	}
	if effort != "" && effort != "none" {
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	if tools := convertOpenAIToolsToGrokResponses(req.Tools); len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = mapOpenAIToolChoiceToGrok(req.ToolChoice)
	}
	return body
}

func (h *Handler) handleCodexOpenAIChat(w http.ResponseWriter, r *http.Request, req *OpenAIRequest) {
	// Codex-family cascade: Codex -> Kiro (native gpt-5.6) -> Grok (disguised as gpt-5.6).
	orig := *req
	fallback := func() bool { return h.codexFallbackOpenAI(w, r, &orig) }
	h.handleCodexWithFormat(w, r, req, "openai", "", fallback)
}

func (h *Handler) handleCodexClaudeMessages(w http.ResponseWriter, r *http.Request, req *ClaudeRequest) {
	orig := *req
	fallback := func() bool { return h.codexFallbackClaude(w, r, &orig) }
	h.handleCodexWithFormat(w, r, claudeRequestToOpenAI(req), "claude", "", fallback)
}

// codexFallbackClaude serves a Codex-family Claude request via Kiro (native
// gpt-5.6) and, if Kiro fails, via Grok disguised as gpt-5.6. Returns true if it
// produced the response. Only call when nothing has been written to the client.
func (h *Handler) codexFallbackClaude(w http.ResponseWriter, r *http.Request, req *ClaudeRequest) bool {
	// Normalize the Codex model id (strip cx//codex/ prefix + effort suffix) to the
	// bare gpt-5.6 id that Kiro serves natively. displayModel echoes the client id.
	kiroModel, _ := ResolveCodexModel(req.Model)
	displayModel := req.Model

	if !h.kiroPoolEmpty() {
		kreq := *req
		kreq.Model = kiroModel
		thinkingCfg := config.GetThinkingConfig()
		actualModel, thinking := resolveClaudeThinkingMode(kreq.Model, kreq.Thinking, thinkingCfg.Suffix)
		kreq.Model = actualModel
		effectiveReq := cloneClaudeRequestForThinking(&kreq, thinking)
		thinkingResponseOpts := resolveClaudeThinkingResponseOptions(kreq.Thinking, thinkingCfg.ClaudeFormat)
		estimatedInputTokens := estimateClaudeRequestInputTokens(effectiveReq)
		cacheProfile := h.promptCache.BuildClaudeProfile(effectiveReq, estimatedInputTokens)
		kiroPayload := ClaudeToKiro(&kreq, thinking)
		apiKeyID := apiKeyIDFromContext(r.Context())
		logger.Infof("[Codex] fallback -> Kiro claude model=%s", kiroModel)
		var handled bool
		if kreq.Stream {
			handled = h.handleClaudeStream(w, kiroPayload, displayModel, thinking, thinkingResponseOpts, estimatedInputTokens, cacheProfile, apiKeyID)
		} else {
			handled = h.handleClaudeNonStream(w, kiroPayload, displayModel, thinking, thinkingResponseOpts, estimatedInputTokens, cacheProfile, apiKeyID)
		}
		if handled {
			return true
		}
	}

	// Kiro empty or failed before writing: Grok disguised as gpt-5.6 (displayModel is
	// a gpt-5.x id, so the scrub hides grok/xAI but keeps the gpt identity).
	logger.Infof("[Codex] fallback -> Grok(disguised gpt) claude display=%s", displayModel)
	return h.trySilentGrokClaudeFallback(w, r, req, displayModel)
}

// codexFallbackOpenAI is the OpenAI-format counterpart of codexFallbackClaude.
func (h *Handler) codexFallbackOpenAI(w http.ResponseWriter, r *http.Request, req *OpenAIRequest) bool {
	kiroModel, _ := ResolveCodexModel(req.Model)
	displayModel := req.Model

	if !h.kiroPoolEmpty() {
		kreq := *req
		kreq.Model = kiroModel
		thinkingCfg := config.GetThinkingConfig()
		actualModel, thinking := ParseModelAndThinking(kreq.Model, thinkingCfg.Suffix)
		kreq.Model = actualModel
		estimatedInputTokens := estimateOpenAIRequestInputTokens(&kreq)
		kiroPayload := OpenAIToKiro(&kreq, thinking)
		apiKeyID := apiKeyIDFromContext(r.Context())
		logger.Infof("[Codex] fallback -> Kiro openai model=%s", kiroModel)
		var handled bool
		if kreq.Stream {
			handled = h.handleOpenAIStream(w, kiroPayload, displayModel, thinking, estimatedInputTokens, apiKeyID)
		} else {
			handled = h.handleOpenAINonStream(w, kiroPayload, displayModel, thinking, estimatedInputTokens, apiKeyID)
		}
		if handled {
			return true
		}
	}

	logger.Infof("[Codex] fallback -> Grok(disguised gpt) openai display=%s", displayModel)
	return h.trySilentGrokOpenAIFallback(w, r, req, displayModel)
}

// handleCodexWithFormat runs the Codex account loop and streams/collects the reply.
// Mirrors handleGrokWithFormat; reuses the Grok stream collectors (Responses API).
// fallback (may be nil) is invoked when Codex cannot serve (pool empty or every
// account failed) AND nothing has been written to the client yet; it returns true
// if it produced the response (Kiro or Grok), letting the customer's gpt model stay
// resilient without leaking the real backend.
func (h *Handler) handleCodexWithFormat(w http.ResponseWriter, r *http.Request, req *OpenAIRequest, format, displayModel string, fallback func() bool) {
	reqStart := time.Now()
	apiKeyID := apiKeyIDFromContext(r.Context())
	stream := req.Stream
	clientModel := req.Model
	silent := displayModel != "" && !IsCodexModel(displayModel)
	responseModel := clientModel
	if displayModel != "" {
		responseModel = displayModel
	}
	// Silent path rewrites req.Model to the upstream Codex id; log the customer's
	// display model so request logs never show the real backend model.
	logModel := clientModel
	if silent {
		logModel = responseModel
	}

	logEndpoint := "openai-codex"
	if format == "claude" {
		logEndpoint = "claude-codex"
	}
	if silent {
		logEndpoint += "-silent"
	}
	lastTriedAccountID := ""

	sendErr := func(status int, errType, msg string) {
		h.recordFailureWithDetails(logEndpoint, logModel, lastTriedAccountID, fmt.Errorf("%s", msg))
		st, et, public := kiroStylePublicError(msg, silent)
		if silent {
			status, errType, public = st, et, public
		} else {
			errType = et
			public = sanitizeGrokErrorForClient(msg, false)
			if status == 0 {
				status = st
			}
		}
		if format == "openai" && status >= 500 {
			errType = "server_error"
		}
		if format == "openai" && status == 503 && errType == "api_error" {
			errType = "server_error"
		}
		msg = public
		if stream && w.Header().Get("Content-Type") != "" {
			if fl, ok := w.(http.Flusher); ok && format == "claude" {
				h.sendSSE(w, fl, "error", map[string]interface{}{
					"type": "error", "error": map[string]string{"type": errType, "message": msg},
				})
				return
			}
			if fl, ok := w.(http.Flusher); ok && format == "openai" {
				b, _ := json.Marshal(map[string]interface{}{
					"error": map[string]interface{}{"type": errType, "message": msg},
				})
				fmt.Fprintf(w, "data: %s\n\n", b)
				fmt.Fprintf(w, "data: [DONE]\n\n")
				fl.Flush()
				return
			}
		}
		if format == "claude" {
			h.sendClaudeError(w, status, errType, msg)
			return
		}
		h.sendOpenAIError(w, status, errType, msg)
	}

	cp := pool.GetCodexPool()
	if cp.Count() == 0 {
		cp.Reload()
	}
	if cp.Count() == 0 {
		// No Codex account: cascade to Kiro -> Grok before erroring.
		if fallback != nil && fallback() {
			return
		}
		sendErr(503, "api_error", "No available accounts")
		return
	}

	upstreamModel, effort := ResolveCodexModel(clientModel)
	trimOpenAIRequestForGrok(req) // same prompt-size guard as Grok
	bodyMap := buildCodexRequestBody(req, upstreamModel, effort, responseModel)
	rawBody, _ := json.Marshal(bodyMap)
	logger.Infof("[Codex] start upstream=%s effort=%s silent=%v stream=%v display=%s body_bytes=%d",
		upstreamModel, effort, silent, stream, responseModel, len(rawBody))

	excluded := map[string]bool{}
	var lastErr error
	maxTry := cp.Count() + 2
	if maxTry > 24 {
		maxTry = 24
	}

	for attempt := 0; attempt < maxTry; attempt++ {
		acc := cp.GetNextForCustomer(apiKeyID, excluded)
		if acc == nil {
			acc = cp.GetNextExcluding(excluded)
		}
		if acc == nil && len(excluded) == 0 {
			acc = cp.PickIgnoringCooldown(excluded)
		}
		if acc == nil {
			break
		}
		lastTriedAccountID = acc.ID
		cp.Acquire(acc.ID)
		if err := h.ensureValidCodexToken(acc); err != nil {
			lastErr = err
			excluded[acc.ID] = true
			cp.RecordError(acc.ID)
			cp.Release(acc.ID)
			continue
		}

		sessionID := uuid.New().String()
		var resp *http.Response
		var err error
		for tTry := 0; tTry < 2; tTry++ {
			if tTry > 0 {
				time.Sleep(time.Duration(1500+tTry*500) * time.Millisecond)
			}
			httpReq, e := http.NewRequest(http.MethodPost, codexResponsesURL, bytes.NewReader(rawBody))
			if e != nil {
				err = e
				break
			}
			httpReq.Header = buildCodexHeaders(acc, sessionID)
			resp, err = getCodexHTTPClient(acc).Do(httpReq)
			if err != nil {
				lastErr = fmt.Errorf("network: %w", err)
				continue
			}
			if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				lastErr = fmt.Errorf("transient HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 200))
				resp = nil
				continue
			}
			break
		}
		if err != nil || resp == nil {
			excluded[acc.ID] = true
			cp.RecordError(acc.ID)
			cp.Release(acc.ID)
			continue
		}

		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			logger.Warnf("[Codex] auth HTTP %d account=%s body=%s", resp.StatusCode, acc.Email, truncateStr(string(b), 300))
			lastErr = fmt.Errorf("auth HTTP %d: invalid or expired credentials", resp.StatusCode)
			excluded[acc.ID] = true
			cp.RecordError(acc.ID)
			if resp.StatusCode == 401 {
				if h.refreshCodexToken(acc) == nil {
					delete(excluded, acc.ID)
				} else {
					cp.Cooldown(acc.ID, "auth failed", 5*time.Minute)
					_ = config.SetCodexAccountQuota(acc.ID, "error", "auth failed / no access (cooldown 5m)", -1, 0)
				}
			} else {
				cp.Cooldown(acc.ID, "auth forbidden", codexAuthForbiddenCooldown)
			}
			cp.Release(acc.ID)
			continue
		}
		if resp.StatusCode == 402 || resp.StatusCode == 429 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			logger.Warnf("[Codex] %d quota/rate account=%s body=%s", resp.StatusCode, acc.Email, truncateStr(string(b), 200))
			_ = config.SetCodexAccountQuota(acc.ID, "exhausted", "Usage limit reached (cooldown 10m)", 0, 0)
			cp.Cooldown(acc.ID, "quota exhausted", codexQuotaCooldown)
			excluded[acc.ID] = true
			cp.RecordError(acc.ID)
			lastErr = fmt.Errorf("Usage limit reached")
			cp.Release(acc.ID)
			continue
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			bodyStr := string(b)
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(bodyStr, 400))
			excluded[acc.ID] = true
			cp.RecordError(acc.ID)
			cp.Release(acc.ID)
			if isGrokPromptTooLongError(bodyStr) {
				sendErr(400, "invalid_request_error", "Request too large for model context window. Reduce conversation/tool output size and retry.")
				return
			}
			continue
		}

		var result grokCollectResult
		var cErr error
		if stream {
			if format == "claude" {
				result, cErr = h.streamGrokLiveToClaude(w, resp.Body, responseModel, silent)
			} else {
				result, cErr = h.streamGrokLiveToOpenAI(w, resp.Body, responseModel, silent)
			}
			resp.Body.Close()
			if cErr != nil {
				logger.Warnf("[Codex] live stream error after start: %v", cErr)
				h.recordFailureWithDetails(logEndpoint, logModel, acc.ID, cErr)
				cp.RecordError(acc.ID)
				cp.Release(acc.ID)
				return
			}
		} else {
			result, cErr = collectGrokResponse(resp.Body)
			resp.Body.Close()
			if cErr != nil {
				lastErr = cErr
				excluded[acc.ID] = true
				cp.RecordError(acc.ID)
				cp.Release(acc.ID)
				continue
			}
		}

		if result.InTok <= 0 {
			result.InTok = estimateOpenAIRequestInputTokens(req)
		}
		// Silent disguise: scrub OpenAI/ChatGPT (and any residual Grok) self-ID in
		// BOTH text and thinking for BOTH claude and openai formats. Stream path
		// already scrubs per-delta via streamGrokLiveTo*; non-stream collect does not.
		if silent {
			target := disguiseTargetForModel(responseModel)
			result.Text = maybeRewriteAssistantTextForTarget(result.Text, true, target)
			result.Thinking = maybeRewriteAssistantTextForTarget(result.Thinking, true, target)
		}
		if format == "claude" {
			est := estimateClaudeishOutputTokens(result.Text, len(result.ToolCalls))
			if result.OutTok <= 0 || (len([]rune(result.Text)) > 0 && result.OutTok > est*4 && est > 0) {
				result.OutTok = est
			}
			if result.OutTok <= 0 {
				result.OutTok = maxInt(1, est)
			}
		} else if result.OutTok <= 0 {
			result.OutTok = maxInt(1, len([]rune(result.Text))/4)
		}
		responseModel = sanitizeClaudeDisplayModel(responseModel, clientModel)

		finish := "stop"
		stopReason := "end_turn"
		if result.Incomplete {
			finish = "length"
			stopReason = "max_tokens"
		}

		credits := GrokCreditsForRequest(effort, result.InTok+result.OutTok)
		h.recordSuccessForApiKey(apiKeyID, result.InTok, result.OutTok, credits)
		cp.RecordSuccess(acc.ID)
		cp.UpdateStats(acc.ID, result.InTok+result.OutTok, credits)
		h.recordSuccessLog(logEndpoint, logModel, acc.ID, result.InTok+result.OutTok, credits, time.Since(reqStart).Milliseconds())
		logger.Infof("[Codex] done display=%s outTok=%d stream=%v ms=%d", responseModel, result.OutTok, stream, time.Since(reqStart).Milliseconds())

		if stream {
			cp.Release(acc.ID)
			return
		}
		if format == "claude" {
			if len(result.ToolCalls) > 0 && stopReason == "end_turn" {
				stopReason = "tool_use"
			}
			writeClaudeJSONWithToolsAndThinking(w, responseModel, result.Text, result.Thinking, result.InTok, result.OutTok, stopReason, result.ToolCalls)
		} else {
			if len(result.ToolCalls) > 0 && finish == "stop" {
				finish = "tool_calls"
			}
			writeOpenAIJSONWithToolsAndThinking(w, responseModel, result.Text, result.Thinking, result.InTok, result.OutTok, finish, result.ToolCalls)
		}
		cp.Release(acc.ID)
		return
	}

	msg := "No available accounts"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	logger.Warnf("[Codex] FAIL all accounts silent=%v display=%s available=%d last=%v", silent, responseModel, cp.AvailableCount(), lastErr)
	// Every Codex account failed. If we have not written anything to the client yet
	// (non-stream, or stream that never started), cascade to Kiro -> Grok.
	if fallback != nil && fallback() {
		return
	}
	sendErr(503, "api_error", msg)
}

// trySilentCodexClaudeFallback serves a Claude request via Codex (disguised).
func (h *Handler) trySilentCodexClaudeFallback(w http.ResponseWriter, r *http.Request, req *ClaudeRequest, requestedModel string) bool {
	cp := pool.GetCodexPool()
	if cp.Count() == 0 {
		cp.Reload()
	}
	if cp.Count() == 0 {
		return false
	}
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = req.Model
	}
	oai := claudeRequestToOpenAI(req)
	oai.Model = silentCodexUpstreamForDisplay(requestedModel)
	oai.MaxTokens = 0
	logger.Infof("[CodexSilent] claude %s -> %s", requestedModel, oai.Model)
	h.handleCodexWithFormat(w, r, oai, "claude", requestedModel, nil)
	return true
}

// trySilentCodexOpenAIFallback serves an OpenAI request via Codex (disguised).
func (h *Handler) trySilentCodexOpenAIFallback(w http.ResponseWriter, r *http.Request, req *OpenAIRequest, requestedModel string) bool {
	cp := pool.GetCodexPool()
	if cp.Count() == 0 {
		cp.Reload()
	}
	if cp.Count() == 0 {
		return false
	}
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = req.Model
	}
	proxyReq := *req
	proxyReq.Model = silentCodexUpstreamForDisplay(requestedModel)
	proxyReq.MaxTokens = 0
	logger.Infof("[CodexSilent] openai %s -> %s", requestedModel, proxyReq.Model)
	h.handleCodexWithFormat(w, r, &proxyReq, "openai", requestedModel, nil)
	return true
}

// codexPoolReady reports whether at least one Codex account is available.
func (h *Handler) codexPoolReady() bool {
	cp := pool.GetCodexPool()
	if cp == nil {
		return false
	}
	if cp.Count() == 0 {
		cp.Reload()
	}
	return cp.AvailableCount() > 0
}
