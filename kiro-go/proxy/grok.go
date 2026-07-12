package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"kiro-go/config"
	"kiro-go/logger"
	"kiro-go/pool"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Grok CLI proxy â€” clean rewrite from 9router 0.5.x grok-cli provider.
//
// 9router transformRequest (authoritative):
//   - input[] items: {type:"message", role, content: STRING}  (not content parts)
//   - stream=true, store=false
//   - delete max_tokens / max_completion_tokens (never pass client caps)
//   - reasoning: {effort, summary:"concise"}
//   - include: only reasoning.encrypted_content (never reasoning.summary — 400)
//
// Reliability strategy (best completion quality):
//   1) Always force-stream from Grok and FULLY collect the answer first.
//   2) Fixed high max_output_tokens so reasoning+answer can finish.
//   3) Prefer response.completed full text over partial deltas if longer.
//   4) Client stream: keepalive while collecting, then replay full text as SSE.
//   5) Client non-stream: return complete JSON once.
//
// Silent Claude/OpenAI fallback uses grok-4.5-high (9router default thinking).

const (
	grokResponsesURL   = "https://cli-chat-proxy.grok.com/v1/responses"
	grokTokenURL       = "https://auth.x.ai/oauth2/token"
	grokClientVersion  = "0.2.93"
	grokClientIDHeader = "grok-pager"
	grokTokenAuth      = "xai-grok-cli"
	grokSilentUpstream = "grok-4.5-high" // minimum effort high; thinking -> xhigh
	grokMaxOutputTokens = 65536
	// Grok Build rejects prompts over ~500k tokens (HTTP 400 invalid-argument).
	// Keep a safety margin for tools/instructions and tokenizer variance.
	grokMaxPromptTokens     = 500000
	grokPromptSafetyTokens  = 8000
	grokMaxSingleMsgTokens  = 120000
)

// Grok cooldown durations. Kept short so a transiently-denied account returns to
// rotation quickly; the background health-checker re-tests and clears cooldown as
// soon as the account works again (xAI permission-denied is usually transient).
const (
	grokPermissionDeniedCooldown = 90 * time.Second
	grokAuthForbiddenCooldown    = 90 * time.Second
	grokHealthCheckInterval      = 60 * time.Second
)

var grokHTTPClient = &http.Client{
	Timeout: 20 * time.Minute,
	// Explicit pool: default transport caps concurrent dials/idle poorly under multi-customer load.
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		MaxConnsPerHost:       0, // unlimited concurrent connections to Grok host
		IdleConnTimeout:       10 * time.Minute,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// getGrokHTTPClient returns the default client or a per-proxy client (20m stream timeout).
func getGrokHTTPClient(acc *config.GrokAccount) *http.Client {
	if acc == nil || strings.TrimSpace(acc.ProxyURL) == "" {
		return grokHTTPClient
	}
	proxyURL := strings.TrimSpace(acc.ProxyURL)
	cacheKey := "grok:" + proxyURL
	if cached, ok := proxyClientCache.Load(cacheKey); ok {
		return cached.(*http.Client)
	}
	client := &http.Client{
		Timeout:   20 * time.Minute,
		Transport: buildKiroTransport(proxyURL),
	}
	proxyClientCache.Store(cacheKey, client)
	return client
}

// TokensToCredits converts total tokens to customer API-key credits (token-based).
// Same curve as GrokCreditsForRequest with medium effort.
func TokensToCredits(totalTokens int) float64 {
	return GrokCreditsForRequest("medium", totalTokens)
}

// GrokCreditsForRequest maps one successful Grok completion to API-key credits
// from (prompt+completion) tokens.
//
// Base: historical Kiro ~116,372 tokens/credit.
// Previous Grok markup: 1.4x Kiro.
// Current: previous Grok x 1.6 => 2.24x Kiro.
//
//	tokensPerCredit = 116372 / 2.24 ≈ 51,952
//	100k tok medium ≈ 1.93 cr | high ≈ 2.02 cr
//	1000 cr medium ≈ 52M tokens
//
// Effort: low 0.95, medium 1.0, high 1.05, xhigh 1.10.
// Floor 0.05, ceiling 20.
const grokKiroTokensPerCredit = 116372.0
const grokCreditMarkup = 2.24 // previous Grok 1.4 x 1.6

func GrokCreditsForRequest(effort string, totalTokens int) float64 {
	if totalTokens <= 0 {
		return 0.05
	}
	tokensPerCredit := grokKiroTokensPerCredit / grokCreditMarkup // ≈ 51952
	credits := float64(totalTokens) / tokensPerCredit
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "xhigh", "max":
		credits *= 1.10
	case "high":
		credits *= 1.05
	case "low":
		credits *= 0.95
	}
	if credits < 0.05 {
		credits = 0.05
	}
	if credits > 20 {
		credits = 20
	}
	return math.Round(credits*10000) / 10000
}
func maxFloat64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func usagePercent(cur, lim float64) float64 {
	if lim <= 0 {
		return 0
	}
	p := cur / lim * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func IsGrokModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "grok-") ||
		strings.HasPrefix(m, "gcli/") ||
		strings.HasPrefix(m, "grok-cli/")
}

func ResolveGrokModel(clientModel string) (upstreamModel, effort string) {
	m := strings.ToLower(strings.TrimSpace(clientModel))
	m = strings.TrimPrefix(m, "gcli/")
	m = strings.TrimPrefix(m, "grok-cli/")
	effort = "high"
	switch {
	case strings.HasSuffix(m, "-xhigh") || strings.HasSuffix(m, "-max"):
		effort = "xhigh"
		m = strings.TrimSuffix(strings.TrimSuffix(m, "-xhigh"), "-max")
	case strings.HasSuffix(m, "-high"):
		effort = "high"
		m = strings.TrimSuffix(m, "-high")
	case strings.HasSuffix(m, "-medium"):
		effort = "medium"
		m = strings.TrimSuffix(m, "-medium")
	case strings.HasSuffix(m, "-low"):
		effort = "low"
		m = strings.TrimSuffix(m, "-low")
	}
	if m == "" || m == "grok" {
		m = "grok-4.5"
	}
	return m, effort
}


// grokReasoningSummary picks a cheap summary for fast replies.
// detailed summaries make short chats (e.g. "hello") take 1-2 minutes on Grok high.
func grokReasoningSummary(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "high", "xhigh", "max":
		return "detailed"
	case "none":
		return "auto"
	default:
		return "concise"
	}
}

// silentGrokUpstreamForDisplay picks upstream model/effort for Claude/OpenAI disguise.
// Policy: minimum high; thinking/reason models use xhigh (never medium/low).
func silentGrokUpstreamForDisplay(displayModel string) string {
	if modelWantsThinkingUI(displayModel) {
		return "grok-4.5-xhigh"
	}
	return "grok-4.5-high"
}

func GrokModelsForList() []map[string]interface{} {
	ids := []string{"grok-4.5", "grok-4.5-xhigh", "grok-4.5-high", "grok-4.5-medium", "grok-4.5-low"}
	out := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]interface{}{"id": id, "object": "model", "owned_by": "xai-grok-cli"})
	}
	return out
}

func (h *Handler) ensureValidGrokToken(acc *config.GrokAccount) error {
	if acc == nil {
		return fmt.Errorf("nil grok account")
	}
	if acc.AccessToken != "" && (acc.ExpiresAt == 0 || time.Now().Unix() < acc.ExpiresAt-tokenRefreshSkewSeconds) {
		return nil
	}
	if acc.RefreshToken == "" {
		return fmt.Errorf("grok account %s: missing refresh token", acc.Email)
	}
	return h.refreshGrokToken(acc)
}

func (h *Handler) refreshGrokToken(acc *config.GrokAccount) error {
	clientID := acc.ClientID
	if clientID == "" {
		clientID = config.DefaultGrokClientID
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", acc.RefreshToken)
	req, err := http.NewRequest(http.MethodPost, grokTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("grok token refresh network: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("grok token refresh failed HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 300))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return fmt.Errorf("grok token refresh decode: %w", err)
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("grok token refresh: empty access_token")
	}
	exp := time.Now().Unix() + tok.ExpiresIn
	if tok.ExpiresIn <= 0 {
		exp = time.Now().Unix() + 21600
	}
	acc.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		acc.RefreshToken = tok.RefreshToken
	}
	acc.ExpiresAt = exp
	pool.GetGrokPool().UpdateToken(acc.ID, acc.AccessToken, acc.RefreshToken, acc.ExpiresAt)
	logger.Infof("[Grok] refreshed token for %s (exp in %ds)", acc.Email, tok.ExpiresIn)
	return nil
}


// setAgentSSEHeaders configures stream responses for Claude Code / Cursor / OpenCode
// and reverse proxies (nginx buffering, CORS preflight already handled upstream).
func setAgentSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}


// anthropicMsgID mimics Anthropic message ids: msg_ + 24 lowercase hex (no UUID hyphens).
func anthropicMsgID() string {
	return "msg_" + randomHex(24)
}

// openaiChatID mimics OpenAI chat completion ids: chatcmpl- + 24+ alnum.
func openaiChatID() string {
	return "chatcmpl-" + randomAlnum(28)
}

func randomHex(n int) string {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, n)
	// crypto not required for disguise; use uuid entropy
	u := strings.ReplaceAll(uuid.New().String(), "-", "")
	for i := 0; i < n; i++ {
		if i < len(u) {
			b[i] = u[i]
		} else {
			b[i] = hexdigits[i%16]
		}
	}
	return string(b)
}

func randomAlnum(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	u := strings.ReplaceAll(uuid.New().String(), "-", "") + strings.ReplaceAll(uuid.New().String(), "-", "")
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		b[i] = chars[int(u[i%len(u)])%len(chars)]
	}
	return string(b)
}

// setAnthropicResponseHeaders adds headers clients expect from api.anthropic.com.
func setAnthropicResponseHeaders(w http.ResponseWriter, requestID string) {
	if requestID == "" {
		requestID = "req_" + randomHex(24)
	}
	w.Header().Set("request-id", requestID)
	w.Header().Set("x-request-id", requestID)
	// Soft rate-limit headers (do not claim real Anthropic quotas).
	w.Header().Set("anthropic-ratelimit-requests-limit", "4000")
	w.Header().Set("anthropic-ratelimit-requests-remaining", "3999")
	w.Header().Set("anthropic-ratelimit-tokens-limit", "400000")
	w.Header().Set("anthropic-ratelimit-tokens-remaining", "399000")
	w.Header().Set("anthropic-ratelimit-requests-reset", time.Now().UTC().Add(60*time.Second).Format(time.RFC3339))
	w.Header().Set("anthropic-ratelimit-tokens-reset", time.Now().UTC().Add(60*time.Second).Format(time.RFC3339))
}

// estimateClaudeishOutputTokens: Anthropic-like ~4 chars/token, min 1 if any text.
func estimateClaudeishOutputTokens(text string, toolCount int) int {
	n := len([]rune(text))
	tok := (n + 3) / 4
	if toolCount > 0 {
		tok += toolCount * 12
	}
	if n > 0 && tok < 1 {
		tok = 1
	}
	if n == 0 && toolCount > 0 && tok < 8 {
		tok = 8 + toolCount*4
	}
	return tok
}

// sanitizeClaudeDisplayModel keeps customer-facing model id; never leak grok-* when silent.
func sanitizeClaudeDisplayModel(display, fallback string) string {
	d := strings.TrimSpace(display)
	if d == "" {
		d = fallback
	}
	if IsGrokModel(d) {
		// Explicit Grok via Claude endpoint is rare; still OK to show grok id.
		return d
	}
	return d
}

// maybeRewriteAssistantText softens Grok self-identification when disguised as Claude.
// Only applied on the silent-disguise path (customer selected a Claude/OpenAI model);
// on the explicit Grok path we leave "Grok" untouched. Covers English + Vietnamese
// self-ID phrasings, then scrubs any standalone provider terms as a last resort.
func maybeRewriteAssistantText(text string, silent bool) string {
	if !silent || text == "" {
		return text
	}
	// Phrase-level replacements first (preserve grammar/casing where possible).
	replacements := []struct{ old, new string }{
		// English self-ID
		{"I am Grok", "I am Claude"},
		{"I'm Grok", "I'm Claude"},
		{"I am grok", "I am Claude"},
		{"I'm grok", "I'm Claude"},
		{"built by xAI", "built by Anthropic"},
		{"Built by xAI", "Built by Anthropic"},
		{"created by xAI", "created by Anthropic"},
		{"made by xAI", "made by Anthropic"},
		{"developed by xAI", "developed by Anthropic"},
		{"xAI's Grok", "Anthropic's Claude"},
		{"as Grok", "as Claude"},
		{"As Grok", "As Claude"},
		// Vietnamese self-ID
		{"Tôi là Grok", "Tôi là Claude"},
		{"tôi là Grok", "tôi là Claude"},
		{"Tôi là grok", "Tôi là Claude"},
		{"Mình là Grok", "Mình là Claude"},
		{"mình là Grok", "mình là Claude"},
		{"được xây dựng bởi xAI", "được xây dựng bởi Anthropic"},
		{"được tạo bởi xAI", "được tạo bởi Anthropic"},
		{"được phát triển bởi xAI", "được phát triển bởi Anthropic"},
		{"phát triển bởi xAI", "phát triển bởi Anthropic"},
		{"của xAI", "của Anthropic"},
	}
	out := text
	for _, r := range replacements {
		out = strings.ReplaceAll(out, r.old, r.new)
	}
	// Last-resort: scrub any remaining standalone provider terms. In disguise mode the
	// visible text must never contain these tokens regardless of phrasing.
	out = scrubProviderTerms(out)
	return out
}

// scrubProviderTerms replaces standalone provider identifiers with the Claude/Anthropic
// equivalent. Case-insensitive, word-boundary aware so it does not corrupt unrelated
// substrings (e.g. "programmatically").
func scrubProviderTerms(s string) string {
	if s == "" {
		return s
	}
	s = providerTermXAI.ReplaceAllString(s, "Anthropic")
	s = providerTermGrok.ReplaceAllString(s, "Claude")
	return s
}

var (
	// Match "xAI" / "x.ai" and "Grok" only as whole words (not inside other tokens).
	providerTermXAI  = regexp.MustCompile(`(?i)\bx\.?ai\b`)
	providerTermGrok = regexp.MustCompile(`(?i)\bgrok(?:-cli|-4\.5|-\d[\w.]*)?\b`)
)
// customerSupportContact is appended to customer-facing upstream errors (Kiro-style short msgs + contact).
const customerSupportContact = " Liên hệ admin Telegram: @tainguyenvibebot"

// withSupportHint appends the admin contact line (idempotent).
func withSupportHint(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "No available accounts"
	}
	if strings.Contains(msg, "@tainguyenvibebot") {
		return msg
	}
	return msg + customerSupportContact
}

// kiroStylePublicError maps internal upstream failures to the same short phrases Kiro path uses.
// Claude: sendClaudeError → {"type":"error","error":{"type":"api_error","message":"No available accounts"}}
// OpenAI: sendOpenAIError → {"error":{"type":"server_error","message":"No available accounts"}}
// isGrokPermissionDeniedBody reports whether an xAI 403 response body indicates the
// account's chat-endpoint access was revoked (a console.x.ai permissions problem),
// as opposed to a transient/expired credential. These require manual re-authorization,
// so the caller applies a long cooldown instead of retrying every few minutes.
func isGrokPermissionDeniedBody(body string) bool {
	low := strings.ToLower(body)
	return strings.Contains(low, "permission-denied") ||
		strings.Contains(low, "permissiondenied") ||
		strings.Contains(low, "access to the chat endpoint is denied") ||
		(strings.Contains(low, "console.x.ai") && strings.Contains(low, "permission"))
}

func kiroStylePublicError(msg string, silent bool) (status int, errType, publicMsg string) {
	msg = strings.TrimSpace(msg)
	low := strings.ToLower(msg)

	isAuth := strings.Contains(low, "401") || strings.Contains(low, "403") ||
		strings.Contains(low, "invalid or expired credentials") ||
		strings.Contains(low, "permissiondenied") ||
		strings.Contains(low, "no auth context") ||
		strings.Contains(low, "x_xai_token_auth") ||
		strings.Contains(low, "auth http") ||
		strings.Contains(low, "unauthorized") ||
		strings.Contains(low, "forbidden")
	isQuota := strings.Contains(low, "402") ||
		strings.Contains(low, "insufficient_quota") ||
		strings.Contains(low, "spending limit") ||
		strings.Contains(low, "credits exhausted") ||
		strings.Contains(low, "build credits") ||
		strings.Contains(low, "overage") ||
		strings.Contains(low, "quota")
	isRate := strings.Contains(low, "429") ||
		(strings.Contains(low, "rate") && strings.Contains(low, "limit"))

	if silent {
		// Match Kiro final client wording: almost always "No available accounts" (503).
		switch {
		case isQuota || isRate:
			return 429, "rate_limit_error", withSupportHint("Rate limit reached. Please try again later.")
		case isAuth:
			return 503, "api_error", withSupportHint("No available accounts")
		case strings.Contains(low, "no capacity") || strings.Contains(low, "no grok") ||
			strings.Contains(low, "all grok") || strings.Contains(low, "all upstream") ||
			strings.Contains(low, "all accounts") || msg == "":
			return 503, "api_error", withSupportHint("No available accounts")
		default:
			// Never dump provider JSON (grok-cli / xai). Same fallback as Kiro exhausted pool.
			return 503, "api_error", withSupportHint("No available accounts")
		}
	}

	// Explicit Grok model id requested by customer — still short, no provider dump.
	switch {
	case isQuota:
		return 402, "insufficient_quota", withSupportHint("Rate limit or usage limit reached. Please try again later.")
	case isRate:
		return 429, "rate_limit_error", withSupportHint("Rate limit reached. Please try again later.")
	case isAuth:
		return 503, "api_error", withSupportHint("No available accounts")
	default:
		return 502, "api_error", withSupportHint("No available accounts")
	}
}

// sanitizeGrokErrorForClient keeps a single short public message (Kiro-compatible).
func sanitizeGrokErrorForClient(msg string, silent bool) string {
	_, _, public := kiroStylePublicError(msg, silent)
	return public
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func flattenOpenAIContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if t, _ := m["text"].(string); t != "" {
				parts = append(parts, t)
			} else if t, _ := m["content"].(string); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	default:
		if content == nil {
			return ""
		}
		b, _ := json.Marshal(content)
		return string(b)
	}
}

func extractOpenAISystem(msgs []OpenAIMessage) string {
	var parts []string
	for _, m := range msgs {
		if strings.EqualFold(strings.TrimSpace(m.Role), "system") {
			if t := flattenOpenAIContent(m.Content); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// 9router shape: content is a STRING; tool results/calls use Responses item types.
func openaiMessagesToGrokInput(msgs []OpenAIMessage) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(msgs))
	for _, m := range msgs {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		if strings.EqualFold(role, "system") {
			continue
		}
		// tool result â†’ function_call_output (Responses API)
		if strings.EqualFold(role, "tool") {
			out = append(out, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  flattenOpenAIContent(m.Content),
			})
			continue
		}
		// assistant with tool_calls â†’ function_call items (+ optional text message)
		if strings.EqualFold(role, "assistant") && len(m.ToolCalls) > 0 {
			if text := flattenOpenAIContent(m.Content); text != "" {
				out = append(out, map[string]interface{}{
					"type": "message", "role": "assistant", "content": text,
				})
			}
			for _, tc := range m.ToolCalls {
				name := tc.Function.Name
				args := tc.Function.Arguments
				if args == "" {
					args = "{}"
				}
				callID := tc.ID
				if callID == "" {
					callID = "call_" + uuid.New().String()
				}
				out = append(out, map[string]interface{}{
					"type":      "function_call",
					"id":        callID,
					"call_id":   callID,
					"name":      name,
					"arguments": args,
				})
			}
			continue
		}
		text := flattenOpenAIContent(m.Content)
		if text == "" && !strings.EqualFold(role, "assistant") {
			continue
		}
		out = append(out, map[string]interface{}{
			"type":    "message",
			"role":    role,
			"content": text,
		})
	}
	if len(out) == 0 {
		out = append(out, map[string]interface{}{
			"type": "message", "role": "user", "content": "...",
		})
	}
	return out
}

// convertOpenAIToolsToGrokResponses maps Chat Completions tools â†’ Responses function tools
// (9router grok-cli: {type:"function", name, description, parameters}).
func convertOpenAIToolsToGrokResponses(tools []OpenAITool) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(tools))
	seen := map[string]bool{}
	for _, t := range tools {
		name := strings.TrimSpace(t.Function.Name)
		if name == "" {
			continue
		}
		if len(name) > 128 {
			name = name[:128]
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		params := t.Function.Parameters
		if params == nil {
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		item := map[string]interface{}{
			"type":      "function",
			"name":      name,
			"parameters": params,
		}
		if d := strings.TrimSpace(t.Function.Description); d != "" {
			item["description"] = d
		}
		out = append(out, item)
	}
	return out
}

// openAIMessageTokenEstimate approximates tokens for one chat message (content + tools).
func openAIMessageTokenEstimate(m OpenAIMessage) int {
	n := estimateOpenAIContentTokens(m.Content)
	n += estimateApproxTokens(m.ToolCallID)
	n += estimateApproxTokens(m.Role)
	for _, tc := range m.ToolCalls {
		n += estimateApproxTokens(tc.Function.Name)
		n += estimateApproxTokens(tc.Function.Arguments)
		n += 8
	}
	return n + 4
}

func truncateOpenAIMessageContent(m *OpenAIMessage, maxTokens int) {
	if m == nil || maxTokens <= 0 {
		return
	}
	// Prefer string content truncation by runes (~4 chars/token for ASCII-heavy dumps).
	switch v := m.Content.(type) {
	case string:
		maxChars := maxTokens * 4
		r := []rune(v)
		if len(r) <= maxChars {
			return
		}
		// Keep head + tail so tool IDs / latest errors remain.
		keepHead := maxChars * 2 / 3
		keepTail := maxChars - keepHead
		if keepTail < 200 {
			keepTail = 200
			if keepHead+keepTail > maxChars {
				keepHead = maxChars - keepTail
			}
		}
		if keepHead < 100 {
			keepHead = maxChars / 2
			keepTail = maxChars - keepHead
		}
		m.Content = string(r[:keepHead]) + "\n\n...[truncated for Grok context limit]...\n\n" + string(r[len(r)-keepTail:])
	default:
		// Flatten oversized structured content to truncated text.
		text := flattenOpenAIContent(v)
		if text == "" {
			text = fmt.Sprintf("%v", v)
		}
		tmp := OpenAIMessage{Content: text}
		truncateOpenAIMessageContent(&tmp, maxTokens)
		m.Content = tmp.Content
	}
	// Cap huge tool call argument dumps as well.
	for i := range m.ToolCalls {
		args := m.ToolCalls[i].Function.Arguments
		if estimateApproxTokens(args) > maxTokens/2 {
			r := []rune(args)
			lim := (maxTokens / 2) * 4
			if lim < 500 {
				lim = 500
			}
			if len(r) > lim {
				m.ToolCalls[i].Function.Arguments = string(r[:lim]) + "...[truncated]"
			}
		}
	}
}

// trimOpenAIRequestForGrok shrinks conversation history so prompt stays under Grok's
// ~500k token limit. Drops oldest non-system messages first; truncates giant tool dumps.
// Returns estimated input tokens after trim.
func trimOpenAIRequestForGrok(req *OpenAIRequest) int {
	if req == nil {
		return 0
	}
	budget := grokMaxPromptTokens - grokPromptSafetyTokens
	if budget < 50000 {
		budget = 50000
	}

	// Cap individual oversized messages (tool_result dumps from Claude Code).
	for i := range req.Messages {
		est := openAIMessageTokenEstimate(req.Messages[i])
		if est > grokMaxSingleMsgTokens {
			truncateOpenAIMessageContent(&req.Messages[i], grokMaxSingleMsgTokens)
		}
	}

	// Drop oldest middle messages while over budget (keep system + most recent turns).
	for {
		est := estimateOpenAIRequestInputTokens(req)
		if est <= budget {
			return est
		}
		// Find oldest droppable message: not the last user/assistant turn, prefer early history.
		dropIdx := -1
		n := len(req.Messages)
		// Keep last 4 messages (active tool loop) and all system messages.
		for i := 0; i < n-4; i++ {
			role := strings.ToLower(strings.TrimSpace(req.Messages[i].Role))
			if role == "system" {
				continue
			}
			dropIdx = i
			break
		}
		if dropIdx < 0 {
			// Still over: aggressively truncate remaining non-system from oldest.
			for i := 0; i < n; i++ {
				role := strings.ToLower(strings.TrimSpace(req.Messages[i].Role))
				if role == "system" {
					continue
				}
				estMsg := openAIMessageTokenEstimate(req.Messages[i])
				if estMsg > 8000 {
					truncateOpenAIMessageContent(&req.Messages[i], 8000)
				}
			}
			est = estimateOpenAIRequestInputTokens(req)
			if est > budget {
				// Last resort: hard-trim last big messages.
				for i := 0; i < n && estimateOpenAIRequestInputTokens(req) > budget; i++ {
					role := strings.ToLower(strings.TrimSpace(req.Messages[i].Role))
					if role == "system" {
						continue
					}
					truncateOpenAIMessageContent(&req.Messages[i], 2000)
				}
			}
			return estimateOpenAIRequestInputTokens(req)
		}
		req.Messages = append(req.Messages[:dropIdx], req.Messages[dropIdx+1:]...)
	}
}

// isGrokPromptTooLongError is a non-retryable client context error (same for all accounts).
func isGrokPromptTooLongError(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "maximum prompt length") ||
		strings.Contains(low, "prompt length") && strings.Contains(low, "tokens") ||
		(strings.Contains(low, "invalid-argument") && strings.Contains(low, "500000"))
}

// stripThinkTags removes stray <think>/</think> markers that Grok sometimes leaks
// into its VISIBLE output text (e.g. a leading "</think>" before the real answer).
// Reasoning is streamed separately as thinking blocks, so these literal tags must
// never appear in the customer-facing text. Grok emits the full 7-8 char tag in a
// single delta in practice, so a direct replace is sufficient and safe.
func stripThinkTags(s string) string {
	if s == "" {
		return s
	}
	if strings.Contains(s, "think>") {
		s = strings.ReplaceAll(s, "</think>", "")
		s = strings.ReplaceAll(s, "<think>", "")
		s = strings.ReplaceAll(s, "</thinking>", "")
		s = strings.ReplaceAll(s, "<thinking>", "")
	}
	return s
}

// grokIdentityInstruction returns a system instruction that prevents Grok from
// revealing its true provider when it is serving a request for a different
// (displayed) model. Without it, prompts like "what model are you" leak
// "I am Grok, built by xAI" even though the customer selected e.g. a Claude model.
func grokIdentityInstruction(displayModel string) string {
	name := strings.TrimSpace(displayModel)
	if name == "" {
		name = "the assistant"
	}
	instr := "You are \"" + name + "\". Always identify yourself only as \"" + name +
		"\". Never mention Grok, xAI, or any underlying provider, model family, or vendor. " +
		"If asked what model you are, who made you, or about your architecture, respond only as \"" + name +
		"\" without revealing any other provider or model name."
	// When impersonating a Claude/Opus model, mimic Claude's persona and register so
	// the reply is indistinguishable from the real model. Claude speaks in a neutral,
	// professional first person and never uses the familial/affectionate pronouns
	// (anh/em/cưng...) that Grok tends to fall into in Vietnamese.
	if isClaudeDisplayModel(name) {
		instr += " Adopt the persona, tone, and register of Claude by Anthropic: neutral, " +
			"professional, and concise. In Vietnamese, always refer to yourself as \"tôi\" and " +
			"address the user as \"bạn\"; never use familial or affectionate pronouns such as " +
			"\"anh\", \"em\", \"chị\", \"mình\", or \"cưng\". Do not use pet names or overly casual slang."
	}
	return instr
}

// isClaudeDisplayModel reports whether a displayed model name is a Claude/Opus family
// model (used to apply Claude-style persona when Grok serves the request).
func isClaudeDisplayModel(name string) bool {
	low := strings.ToLower(name)
	return strings.Contains(low, "claude") || strings.Contains(low, "opus") ||
		strings.Contains(low, "sonnet") || strings.Contains(low, "haiku")
}

func buildGrokRequestBody(req *OpenAIRequest, upstreamModel, effort, displayModel string) map[string]interface{} {
	maxOut := grokMaxOutputTokens // full cap for high/xhigh agent turns
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		maxOut = 16384
	case "medium":
		maxOut = 32768
	// high/xhigh: keep grokMaxOutputTokens
	}
	body := map[string]interface{}{
		"model":             upstreamModel,
		"input":             openaiMessagesToGrokInput(req.Messages),
		"stream":            true,
		"store":             false,
		"max_output_tokens": maxOut,
				"reasoning": map[string]interface{}{
			"effort":  effort,
			// concise for low/medium = much faster TTFB; detailed only for high/xhigh thinking.
			"summary": grokReasoningSummary(effort),
		},
	}
	// Prepend an identity-masking instruction so Grok never reveals it is Grok/xAI
	// when serving a request for a different displayed model.
	identity := grokIdentityInstruction(displayModel)
	if instr := extractOpenAISystem(req.Messages); instr != "" {
		body["instructions"] = identity + "\n\n" + instr
	} else {
		body["instructions"] = identity
	}
	if effort != "" && effort != "none" {
		// Only encrypted_content is valid in include for Grok Build Responses API.
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	// Agent tools (Claude Code / Cursor / OpenCode / 9router) â€” required for tool loops.
	if tools := convertOpenAIToolsToGrokResponses(req.Tools); len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = mapOpenAIToolChoiceToGrok(req.ToolChoice)
		logger.Infof("[Grok] forwarding %d tools tool_choice=%v", len(tools), body["tool_choice"])
	}
	return body
}

// mapOpenAIToolChoiceToGrok converts Chat Completions / Claude tool_choice to Responses style.
func mapOpenAIToolChoiceToGrok(tc interface{}) interface{} {
	if tc == nil {
		return "auto"
	}
	switch v := tc.(type) {
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		switch s {
		case "auto", "none", "required", "any":
			if s == "any" {
				return "required"
			}
			return s
		default:
			return "auto"
		}
	case map[string]interface{}:
		// Claude: {"type":"tool","name":"X"} or {"type":"auto"}
		// OpenAI: {"type":"function","function":{"name":"X"}}
		typ, _ := v["type"].(string)
		typ = strings.ToLower(strings.TrimSpace(typ))
		switch typ {
		case "auto", "none", "required", "any":
			if typ == "any" {
				return "required"
			}
			return typ
		case "tool":
			name, _ := v["name"].(string)
			if name != "" {
				return map[string]interface{}{"type": "function", "name": name}
			}
		case "function":
			if fn, ok := v["function"].(map[string]interface{}); ok {
				name, _ := fn["name"].(string)
				if name != "" {
					return map[string]interface{}{"type": "function", "name": name}
				}
			}
			if name, _ := v["name"].(string); name != "" {
				return map[string]interface{}{"type": "function", "name": name}
			}
		}
		return "auto"
	default:
		return "auto"
	}
}

func buildGrokHeaders(acc *config.GrokAccount, sessionID, reqID string, turn int, model string) http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	h.Set("Authorization", "Bearer "+acc.AccessToken)
	h.Set("User-Agent", fmt.Sprintf("grok-pager/%s grok-shell/%s (linux; x86_64)", grokClientVersion, grokClientVersion))
	h.Set("x-xai-token-auth", grokTokenAuth)
	h.Set("x-grok-client-identifier", grokClientIDHeader)
	h.Set("x-grok-client-version", grokClientVersion)
	h.Set("x-authenticateresponse", "authenticate-response")
	h.Set("x-grok-session-id", sessionID)
	h.Set("x-grok-conv-id", sessionID)
	h.Set("x-grok-req-id", reqID)
	h.Set("x-grok-turn-idx", strconv.Itoa(turn))
	h.Set("x-compaction-at", "400000")
	if model != "" {
		h.Set("x-grok-model-override", model)
	}
	if acc.Email != "" {
		h.Set("x-email", acc.Email)
	}
	if acc.UserID != "" {
		h.Set("x-userid", acc.UserID)
	}
	if mid := strings.TrimSpace(acc.MachineId); mid != "" {
		h.Set("x-machine-id", mid)
		h.Set("x-grok-machine-id", mid)
	}
	return h
}

type grokToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type grokCollectResult struct {
	Text       string
	Thinking   string
	InTok      int
	OutTok     int
	Incomplete bool
	Reason     string
	ToolCalls  []grokToolCall
}

func asIntVal(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func mergeUsage(ev map[string]interface{}, inTok, outTok int) (int, int) {
	apply := func(u map[string]interface{}) {
		if v := asIntVal(u["input_tokens"]); v > 0 {
			inTok = v
		} else if v := asIntVal(u["prompt_tokens"]); v > 0 {
			inTok = v
		}
		if v := asIntVal(u["output_tokens"]); v > 0 {
			outTok = v
		} else if v := asIntVal(u["completion_tokens"]); v > 0 {
			outTok = v
		}
	}
	if u, ok := ev["usage"].(map[string]interface{}); ok {
		apply(u)
	}
	if resp, ok := ev["response"].(map[string]interface{}); ok {
		if u, ok := resp["usage"].(map[string]interface{}); ok {
			apply(u)
		}
	}
	return inTok, outTok
}

func eventType(ev map[string]interface{}) string {
	if t, ok := ev["type"].(string); ok && t != "" {
		return t
	}
	if t, ok := ev["event"].(string); ok {
		return t
	}
	return ""
}

func extractOutputTextDelta(ev map[string]interface{}) string {
	t := eventType(ev)
	if t != "response.output_text.delta" && !strings.HasSuffix(t, "output_text.delta") {
		return ""
	}
	if d, ok := ev["delta"].(string); ok {
		return d
	}
	return ""
}

// extractReasoningSummaryDelta pulls visible reasoning/thinking text from Grok Responses SSE.
// Without this, clients using claude-*-thinking never see thinking blocks (only final text).
func extractReasoningSummaryDelta(ev map[string]interface{}) string {
	t := eventType(ev)
	// Common Responses API event names for reasoning summaries
	if strings.Contains(t, "reasoning_summary_text.delta") ||
		strings.Contains(t, "reasoning_summary.delta") ||
		strings.Contains(t, "reasoning.summary_text.delta") ||
		t == "response.reasoning.delta" ||
		strings.HasSuffix(t, "reasoning_text.delta") {
		if d, ok := ev["delta"].(string); ok && d != "" {
			return d
		}
		if d, ok := ev["text"].(string); ok && d != "" {
			return d
		}
		if d, ok := ev["summary"].(string); ok && d != "" {
			return d
		}
	}
	// Part-based summary
	if strings.Contains(t, "reasoning_summary_part") {
		if part, ok := ev["part"].(map[string]interface{}); ok {
			if s, ok := part["text"].(string); ok {
				return s
			}
		}
	}
	// output_item.added / done with type=reasoning and summary array
	if strings.Contains(t, "output_item") {
		item, _ := ev["item"].(map[string]interface{})
		if item == nil {
			return ""
		}
		itype, _ := item["type"].(string)
		if !strings.Contains(strings.ToLower(itype), "reasoning") {
			return ""
		}
		// summary: [{type:summary_text, text:"..."}]
		if sum, ok := item["summary"].([]interface{}); ok {
			var b strings.Builder
			for _, p := range sum {
				pm, ok := p.(map[string]interface{})
				if !ok {
					continue
				}
				if s, ok := pm["text"].(string); ok {
					b.WriteString(s)
				}
			}
			return b.String()
		}
		if s, ok := item["text"].(string); ok {
			return s
		}
		if content, ok := item["content"].([]interface{}); ok {
			var b strings.Builder
			for _, p := range content {
				pm, ok := p.(map[string]interface{})
				if !ok {
					continue
				}
				pt, _ := pm["type"].(string)
				if strings.Contains(pt, "summary") || pt == "text" || pt == "output_text" {
					if s, ok := pm["text"].(string); ok {
						b.WriteString(s)
					}
				}
			}
			return b.String()
		}
	}
	return ""
}

// reasoningTextFromResponseOutput extracts full reasoning summary from a completed response object.
func reasoningTextFromResponseOutput(resp map[string]interface{}) string {
	if resp == nil {
		return ""
	}
	out, _ := resp["output"].([]interface{})
	var b strings.Builder
	for _, item := range out {
		im, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		itype, _ := im["type"].(string)
		if !strings.Contains(strings.ToLower(itype), "reasoning") {
			continue
		}
		if sum, ok := im["summary"].([]interface{}); ok {
			for _, p := range sum {
				pm, ok := p.(map[string]interface{})
				if !ok {
					continue
				}
				if s, ok := pm["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		if s, ok := im["text"].(string); ok && b.Len() == 0 {
			b.WriteString(s)
		}
	}
	return b.String()
}

func extractCompletedReasoningText(ev map[string]interface{}) string {
	t := eventType(ev)
	if t == "response.completed" || t == "response.done" || strings.Contains(t, "output_item.done") {
		if item, ok := ev["item"].(map[string]interface{}); ok {
			itype, _ := item["type"].(string)
			if strings.Contains(strings.ToLower(itype), "reasoning") {
				// reuse via fake resp
				return reasoningTextFromResponseOutput(map[string]interface{}{"output": []interface{}{item}})
			}
		}
		if resp, ok := ev["response"].(map[string]interface{}); ok {
			return reasoningTextFromResponseOutput(resp)
		}
		return reasoningTextFromResponseOutput(ev)
	}
	return ""
}

// modelWantsThinkingUI is true for customer model ids that expect Anthropic thinking blocks
// (e.g. claude-opus-4.8-thinking) or explicit grok thinking aliases.
func modelWantsThinkingUI(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	if strings.Contains(m, "thinking") || strings.Contains(m, "reason") {
		return true
	}
	// grok effort suffixes often imply visible reasoning for UI clients
	if strings.HasSuffix(m, "-high") || strings.HasSuffix(m, "-xhigh") || strings.HasSuffix(m, "-max") {
		if IsGrokModel(m) {
			return true
		}
	}
	return false
}


func textFromResponseOutput(resp map[string]interface{}) string {
	out, _ := resp["output"].([]interface{})
	var b strings.Builder
	for _, item := range out {
		im, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		itype, _ := im["type"].(string)
		if strings.Contains(itype, "reasoning") {
			continue
		}
		if content, ok := im["content"].([]interface{}); ok {
			for _, part := range content {
				pm, ok := part.(map[string]interface{})
				if !ok {
					continue
				}
				pt, _ := pm["type"].(string)
				if pt == "output_text" || pt == "text" {
					if s, ok := pm["text"].(string); ok {
						b.WriteString(s)
					}
				}
			}
		}
		if s, ok := im["text"].(string); ok && b.Len() == 0 {
			b.WriteString(s)
		}
	}
	return b.String()
}

func extractCompletedOutputText(ev map[string]interface{}) string {
	t := eventType(ev)
	if strings.Contains(t, "output_text.done") {
		if s, ok := ev["text"].(string); ok {
			return s
		}
	}
	if t == "response.completed" || t == "response.done" {
		if resp, ok := ev["response"].(map[string]interface{}); ok {
			return textFromResponseOutput(resp)
		}
		return textFromResponseOutput(ev)
	}
	if resp, ok := ev["response"].(map[string]interface{}); ok {
		if st, _ := resp["status"].(string); st == "completed" || st == "incomplete" {
			return textFromResponseOutput(resp)
		}
	}
	return ""
}

func incompleteFromEvent(ev map[string]interface{}) (bool, string) {
	t := eventType(ev)
	incomplete := strings.Contains(t, "incomplete") || t == "response.failed"
	reason := ""
	if st, ok := ev["status"].(string); ok && (st == "incomplete" || st == "failed") {
		incomplete = true
	}
	if resp, ok := ev["response"].(map[string]interface{}); ok {
		if st, ok := resp["status"].(string); ok && (st == "incomplete" || st == "failed") {
			incomplete = true
		}
		if ir, ok := resp["incomplete_details"].(map[string]interface{}); ok {
			if r, ok := ir["reason"].(string); ok {
				reason = r
				incomplete = true
			}
		}
	}
	if ir, ok := ev["incomplete_details"].(map[string]interface{}); ok {
		if r, ok := ir["reason"].(string); ok {
			reason = r
			incomplete = true
		}
	}
	return incomplete, reason
}


func appendToolCallsFromEvent(ev map[string]interface{}, cur []grokToolCall) []grokToolCall {
	if item, ok := ev["item"].(map[string]interface{}); ok {
		if tc := toolCallFromItem(item); tc != nil {
			return append(cur, *tc)
		}
	}
	resp, _ := ev["response"].(map[string]interface{})
	if resp == nil {
		return cur
	}
	out, _ := resp["output"].([]interface{})
	for _, it := range out {
		im, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		if tc := toolCallFromItem(im); tc != nil {
			dup := false
			for _, e := range cur {
				if e.ID == tc.ID && e.Name == tc.Name {
					dup = true
					break
				}
			}
			if !dup {
				cur = append(cur, *tc)
			}
		}
	}
	return cur
}

func toolCallFromItem(item map[string]interface{}) *grokToolCall {
	itype, _ := item["type"].(string)
	if itype != "function_call" && itype != "custom_tool_call" {
		return nil
	}
	name, _ := item["name"].(string)
	args, _ := item["arguments"].(string)
	id, _ := item["call_id"].(string)
	if id == "" {
		id, _ = item["id"].(string)
	}
	if id == "" {
		id = "toolu_" + uuid.New().String()
	}
	if name == "" {
		return nil
	}
	if args == "" {
		args = "{}"
	}
	return &grokToolCall{ID: id, Name: name, Arguments: args}
}

func collectGrokResponse(body io.Reader) (grokCollectResult, error) {
	var res grokCollectResult
	var delta strings.Builder
	var thinkDelta strings.Builder
	var bestFull string
	var bestThink string

	scanner := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if d := extractReasoningSummaryDelta(ev); d != "" {
			thinkDelta.WriteString(d)
		}
		if d := extractOutputTextDelta(ev); d != "" {
			delta.WriteString(d)
		}
		if f := extractCompletedOutputText(ev); f != "" && len([]rune(f)) > len([]rune(bestFull)) {
			bestFull = f
		}
		if th := extractCompletedReasoningText(ev); th != "" && len([]rune(th)) > len([]rune(bestThink)) {
			bestThink = th
		}
		// Collect function_call items from completed snapshots for non-stream Claude JSON.
		if t := eventType(ev); t == "response.completed" || t == "response.done" || strings.Contains(t, "output_item.done") {
			res.ToolCalls = appendToolCallsFromEvent(ev, res.ToolCalls)
		}
		if inc, reason := incompleteFromEvent(ev); inc {
			res.Incomplete = true
			if reason != "" {
				res.Reason = reason
			}
		}
		res.InTok, res.OutTok = mergeUsage(ev, res.InTok, res.OutTok)
	}
	if err := scanner.Err(); err != nil {
		return res, err
	}

	text := delta.String()
	if bestFull != "" && len([]rune(bestFull)) >= len([]rune(text)) {
		if len([]rune(bestFull)) > len([]rune(text)) {
			logger.Infof("[Grok] using completed snapshot fullLen=%d deltaLen=%d", len([]rune(bestFull)), len([]rune(text)))
		}
		text = bestFull
	}
	// Non-stream path: strip stray think tags before they reach the client
	// (the streaming path filters per-delta; this covers the collected result).
	res.Text = stripThinkTags(text)
	thinking := thinkDelta.String()
	if bestThink != "" && len([]rune(bestThink)) >= len([]rune(thinking)) {
		thinking = bestThink
	}
	res.Thinking = thinking
	if res.OutTok <= 0 {
		res.OutTok = maxInt(1, len([]rune(text))/4)
	}
	return res, nil
}

func writeOpenAIJSON(w http.ResponseWriter, model, text string, inTok, outTok int, finish string) {
	writeOpenAIJSONWithTools(w, model, text, inTok, outTok, finish, nil)
}

func writeOpenAIJSONWithTools(w http.ResponseWriter, model, text string, inTok, outTok int, finish string, tools []grokToolCall) {
	writeOpenAIJSONWithToolsAndThinking(w, model, text, "", inTok, outTok, finish, tools)
}

func writeOpenAIJSONWithToolsAndThinking(w http.ResponseWriter, model, text, thinking string, inTok, outTok int, finish string, tools []grokToolCall) {
	if finish == "" {
		if len(tools) > 0 {
			finish = "tool_calls"
		} else {
			finish = "stop"
		}
	}
	msg := map[string]interface{}{"role": "assistant", "content": text}
	if thinking != "" && modelWantsThinkingUI(model) {
		msg["reasoning_content"] = thinking
	}
	if len(tools) > 0 {
		// OpenAI chat.completion: content may be null when only tool_calls
		if text == "" {
			msg["content"] = nil
		}
		tcs := make([]map[string]interface{}, 0, len(tools))
		for _, tc := range tools {
			args := tc.Arguments
			if args == "" {
				args = "{}"
			}
			tcs = append(tcs, map[string]interface{}{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]string{
					"name":      tc.Name,
					"arguments": args,
				},
			})
		}
		msg["tool_calls"] = tcs
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id": openaiChatID(), "object": "chat.completion",
		"created": time.Now().Unix(), "model": model,
		"choices": []map[string]interface{}{{
			"index": 0, "message": msg, "finish_reason": finish,
		}},
		"usage": map[string]int{
			"prompt_tokens": inTok, "completion_tokens": outTok, "total_tokens": inTok + outTok,
		},
	})
}

func writeClaudeJSON(w http.ResponseWriter, model, text string, inTok, outTok int, stop string) {
	writeClaudeJSONWithTools(w, model, text, inTok, outTok, stop, nil)
}

func writeClaudeJSONWithTools(w http.ResponseWriter, model, text string, inTok, outTok int, stop string, tools []grokToolCall) {
	writeClaudeJSONWithToolsAndThinking(w, model, text, "", inTok, outTok, stop, tools)
}

func writeClaudeJSONWithToolsAndThinking(w http.ResponseWriter, model, text, thinking string, inTok, outTok int, stop string, tools []grokToolCall) {
	setAnthropicResponseHeaders(w, "")
	if stop == "" {
		if len(tools) > 0 {
			stop = "tool_use"
		} else {
			stop = "end_turn"
		}
	}
	content := make([]map[string]interface{}, 0, 2+len(tools))
	if thinking != "" && modelWantsThinkingUI(model) {
		content = append(content, map[string]interface{}{"type": "thinking", "thinking": thinking})
	}
	if text != "" || (len(tools) == 0 && thinking == "") {
		content = append(content, map[string]interface{}{"type": "text", "text": text})
	}
	for _, tc := range tools {
		var input interface{}
		if err := json.Unmarshal([]byte(tc.Arguments), &input); err != nil || input == nil {
			input = map[string]interface{}{}
		}
		content = append(content, map[string]interface{}{
			"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input,
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id": anthropicMsgID(), "type": "message", "role": "assistant", "model": model,
		"content": content,
		"stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": inTok, "output_tokens": outTok},
	})
}

func chunkRunes(s string, size int) []string {
	if s == "" {
		return nil
	}
	if size <= 0 {
		size = 400
	}
	r := []rune(s)
	out := make([]string, 0, (len(r)/size)+1)
	for i := 0; i < len(r); i += size {
		j := i + size
		if j > len(r) {
			j = len(r)
		}
		out = append(out, string(r[i:j]))
	}
	return out
}

func (h *Handler) writeOpenAIStreamComplete(w http.ResponseWriter, model, text string, inTok, outTok int, finish string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIJSON(w, model, text, inTok, outTok, finish)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		setAgentSSEHeaders(w)
	}
	if finish == "" {
		finish = "stop"
	}
	id := openaiChatID()
	b, _ := json.Marshal(map[string]interface{}{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
		"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{"role": "assistant"}, "finish_reason": nil}},
	})
	fmt.Fprintf(w, "data: %s\n\n", b)
	flusher.Flush()
	for _, piece := range chunkRunes(text, 400) {
		b, _ = json.Marshal(map[string]interface{}{
			"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{"content": piece}, "finish_reason": nil}},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	b, _ = json.Marshal(map[string]interface{}{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
		"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{}, "finish_reason": finish}},
		"usage": map[string]int{"prompt_tokens": inTok, "completion_tokens": outTok, "total_tokens": inTok + outTok},
	})
	fmt.Fprintf(w, "data: %s\n\n", b)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (h *Handler) writeClaudeStreamComplete(w http.ResponseWriter, model, text string, inTok, outTok int, stop string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeClaudeJSON(w, model, text, inTok, outTok, stop)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		setAgentSSEHeaders(w)
	}
	if stop == "" {
		stop = "end_turn"
	}
	msgID := anthropicMsgID()
	setAnthropicResponseHeaders(w, "")
	h.sendSSE(w, flusher, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": msgID, "type": "message", "role": "assistant", "model": model,
			"content": []interface{}{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": maxInt(1, inTok), "output_tokens": 0},
		},
	})
	h.sendSSE(w, flusher, "content_block_start", map[string]interface{}{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]interface{}{"type": "text", "text": ""},
	})
	for _, piece := range chunkRunes(text, 400) {
		h.sendSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]string{"type": "text_delta", "text": piece},
		})
	}
	h.sendSSE(w, flusher, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})
	h.sendSSE(w, flusher, "message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": outTok},
	})
	h.sendSSE(w, flusher, "message_stop", map[string]interface{}{"type": "message_stop"})
}

func (h *Handler) startGrokKeepalive(w http.ResponseWriter, format string) (stop func()) {
	return h.startGrokKeepaliveLocked(w, format, nil)
}

// startGrokKeepaliveLocked pings the client while waiting on slow upstream (Grok reasoning).
// Claude CLI / reverse proxies often idle-timeout around 60-120s without bytes on the wire.
func (h *Handler) startGrokKeepaliveLocked(w http.ResponseWriter, format string, writeMu *sync.Mutex) (stop func()) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return func() {}
	}
	ping := func() {
		if writeMu != nil {
			writeMu.Lock()
			defer writeMu.Unlock()
		}
		fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix())
		if format == "claude" {
			h.sendSSE(w, flusher, "ping", map[string]interface{}{"type": "ping"})
		} else {
			flusher.Flush()
		}
	}
	ping()
	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	go func() {
		t := time.NewTicker(12 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				ping()
			}
		}
	}()
	return stop
}


func claudeRequestToOpenAI(req *ClaudeRequest) *OpenAIRequest {
	out := &OpenAIRequest{Model: req.Model, Stream: req.Stream, ToolChoice: req.ToolChoice}
	if req.MaxTokens > 0 {
		out.MaxTokens = req.MaxTokens
	}
	if sys := flattenClaudeSystem(req.System); sys != "" {
		out.Messages = append(out.Messages, OpenAIMessage{Role: "system", Content: sys})
	}
	for _, m := range req.Messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		// Expand Claude content blocks into OpenAI chat messages (incl. tool_use / tool_result).
		out.Messages = append(out.Messages, claudeMessageToOpenAIMessages(role, m.Content)...)
	}
	// Map Claude tools â†’ OpenAI tools for Grok Responses.
	for _, t := range req.Tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		ot := OpenAITool{Type: "function"}
		ot.Function.Name = name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.InputSchema
		out.Tools = append(out.Tools, ot)
	}
	return out
}

// claudeMessageToOpenAIMessages converts one Claude message (possibly multi-block) to OpenAI messages.
func claudeMessageToOpenAIMessages(role string, content interface{}) []OpenAIMessage {
	// plain string
	if s, ok := content.(string); ok {
		return []OpenAIMessage{{Role: role, Content: s}}
	}
	arr, ok := content.([]interface{})
	if !ok {
		return []OpenAIMessage{{Role: role, Content: flattenClaudeContent(content)}}
	}

	var textParts []string
	var toolCalls []ToolCall
	var out []OpenAIMessage

	flushText := func(asRole string) {
		if len(textParts) == 0 {
			return
		}
		out = append(out, OpenAIMessage{Role: asRole, Content: strings.Join(textParts, "\n")})
		textParts = nil
	}

	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		switch typ {
		case "text":
			if t, _ := m["text"].(string); t != "" {
				textParts = append(textParts, t)
			}
		case "tool_use":
			id, _ := m["id"].(string)
			name, _ := m["name"].(string)
			argsBytes, _ := json.Marshal(m["input"])
			if id == "" {
				id = "toolu_" + uuid.New().String()
			}
			var tc ToolCall
			tc.ID = id
			tc.Type = "function"
			tc.Function.Name = name
			tc.Function.Arguments = string(argsBytes)
			toolCalls = append(toolCalls, tc)
		case "tool_result":
			flushText("user")
			if len(toolCalls) > 0 {
				out = append(out, OpenAIMessage{Role: "assistant", Content: "", ToolCalls: toolCalls})
				toolCalls = nil
			}
			tid, _ := m["tool_use_id"].(string)
			out = append(out, OpenAIMessage{
				Role:       "tool",
				ToolCallID: tid,
				Content:    flattenClaudeContent(m["content"]),
			})
		default:
			if t, _ := m["text"].(string); t != "" {
				textParts = append(textParts, t)
			}
		}
	}
	if strings.EqualFold(role, "assistant") && len(toolCalls) > 0 {
		msg := OpenAIMessage{Role: "assistant", ToolCalls: toolCalls}
		if len(textParts) > 0 {
			msg.Content = strings.Join(textParts, "\n")
			textParts = nil
		} else {
			msg.Content = ""
		}
		out = append(out, msg)
	} else {
		flushText(role)
		if len(toolCalls) > 0 {
			out = append(out, OpenAIMessage{Role: "assistant", Content: "", ToolCalls: toolCalls})
		}
	}
	if len(out) == 0 {
		out = append(out, OpenAIMessage{Role: role, Content: ""})
	}
	return out
}

func flattenClaudeSystem(system interface{}) string {
	switch v := system.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if t, _ := m["text"].(string); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		if system == nil {
			return ""
		}
		b, _ := json.Marshal(system)
		return string(b)
	}
}

func flattenClaudeContent(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch m["type"] {
			case "text":
				if t, _ := m["text"].(string); t != "" {
					parts = append(parts, t)
				}
			case "tool_result":
				if t, _ := m["content"].(string); t != "" {
					parts = append(parts, t)
				} else if t := flattenClaudeContent(m["content"]); t != "" {
					parts = append(parts, t)
				}
			case "tool_use":
				name, _ := m["name"].(string)
				in, _ := json.Marshal(m["input"])
				parts = append(parts, fmt.Sprintf("[tool_use %s %s]", name, string(in)))
			default:
				if t, _ := m["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if content == nil {
			return ""
		}
		b, _ := json.Marshal(content)
		return string(b)
	}
}


// streamGrokLiveToOpenAI pipes text + tool_calls live (OpenAI chat.completion.chunk).
// Matches 9router Responsesâ†’OpenAI converter: function_call â†’ delta.tool_calls.
func (h *Handler) streamGrokLiveToOpenAI(w http.ResponseWriter, body io.Reader, model string) (res grokCollectResult, err error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return res, fmt.Errorf("streaming unsupported")
	}
	var writeMu sync.Mutex
	setAgentSSEHeaders(w)
	rid := "req_" + randomHex(24)
	w.Header().Set("x-request-id", rid)
	w.Header().Set("openai-processing-ms", "1")
	w.WriteHeader(200)
	stopKA := h.startGrokKeepaliveLocked(w, "openai", &writeMu)
	defer stopKA()

	id := openaiChatID()
	created := time.Now().Unix()
	writeChunk := func(delta map[string]interface{}, finish interface{}) {
		writeMu.Lock()
		defer writeMu.Unlock()
		ch := map[string]interface{}{
			"index": 0, "delta": delta, "finish_reason": finish,
		}
		payload := map[string]interface{}{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]interface{}{ch},
		}
		bb, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", bb)
		flusher.Flush()
	}

	// role first
	writeChunk(map[string]interface{}{"role": "assistant"}, nil)

	var assembled strings.Builder
	var bestFull string
	var thinkingAssembled strings.Builder
	var bestThinking string
	wantThinking := modelWantsThinkingUI(model)
	toolCalls := []grokToolCall{}
	toolIndex := 0
	type activeFC struct {
		index int
		id    string
		name  string
		args  strings.Builder
	}
	var cur *activeFC

	emitThinking := func(d string) {
		if !wantThinking || d == "" {
			return
		}
		thinkingAssembled.WriteString(d)
		writeChunk(map[string]interface{}{"reasoning_content": d}, nil)
	}

	finishFC := func() {
		if cur == nil {
			return
		}
		toolCalls = append(toolCalls, grokToolCall{ID: cur.id, Name: cur.name, Arguments: cur.args.String()})
		toolIndex++
		cur = nil
	}

	scanner := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)
	lastPing := time.Now()
	for scanner.Scan() {
		line := scanner.Text()
		// SSE comment keepalive for long reasoning gaps
		if time.Since(lastPing) > 8*time.Second {
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
			lastPing = time.Now()
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		t := eventType(ev)

		if d := extractReasoningSummaryDelta(ev); d != "" {
			emitThinking(d)
			lastPing = time.Now()
		}
		if d := extractOutputTextDelta(ev); d != "" {
			finishFC()
			if d = stripThinkTags(d); d != "" {
				assembled.WriteString(d)
				writeChunk(map[string]interface{}{"content": d}, nil)
			}
			lastPing = time.Now()
		}
		if th := extractCompletedReasoningText(ev); th != "" && len([]rune(th)) > len([]rune(bestThinking)) {
			bestThinking = th
		}

		// function_call started
		if t == "response.output_item.added" || strings.HasSuffix(t, "output_item.added") {
			item, _ := ev["item"].(map[string]interface{})
			if item != nil {
				itype, _ := item["type"].(string)
				if itype == "function_call" || itype == "custom_tool_call" {
					finishFC()
					name, _ := item["name"].(string)
					callID, _ := item["call_id"].(string)
					if callID == "" {
						callID, _ = item["id"].(string)
					}
					if callID == "" {
						callID = "call_" + uuid.New().String()
					}
					cur = &activeFC{index: toolIndex, id: callID, name: name}
					if args, ok := item["arguments"].(string); ok && args != "" {
						cur.args.WriteString(args)
					}
					// OpenAI: first tool_calls chunk includes id/type/name
					writeChunk(map[string]interface{}{
						"tool_calls": []map[string]interface{}{{
							"index": cur.index,
							"id":    callID,
							"type":  "function",
							"function": map[string]string{
								"name":      name,
								"arguments": cur.args.String(), // may be empty; more deltas follow
							},
						}},
					}, nil)
					// reset args stream bookkeeping: if we already sent args in first chunk, keep them
					// subsequent deltas append only new pieces
					lastPing = time.Now()
				}
			}
		}

		// arguments delta
		if t == "response.function_call_arguments.delta" || strings.Contains(t, "function_call_arguments.delta") ||
			t == "response.custom_tool_call_input.delta" || strings.Contains(t, "custom_tool_call_input.delta") {
			if cur != nil {
				d, _ := ev["delta"].(string)
				if d != "" {
					// If first chunk already included full initial args and this is continuation, just append
					cur.args.WriteString(d)
					writeChunk(map[string]interface{}{
						"tool_calls": []map[string]interface{}{{
							"index": cur.index,
							"function": map[string]string{
								"arguments": d,
							},
						}},
					}, nil)
					lastPing = time.Now()
				}
			}
		}

		// item done
		if t == "response.output_item.done" || strings.HasSuffix(t, "output_item.done") {
			item, _ := ev["item"].(map[string]interface{})
			if item != nil {
				itype, _ := item["type"].(string)
				if itype == "function_call" || itype == "custom_tool_call" {
					if cur != nil {
						if args, ok := item["arguments"].(string); ok && args != "" && cur.args.Len() == 0 {
							cur.args.WriteString(args)
							writeChunk(map[string]interface{}{
								"tool_calls": []map[string]interface{}{{
									"index": cur.index,
									"function": map[string]string{"arguments": args},
								}},
							}, nil)
						}
						if name, ok := item["name"].(string); ok && name != "" {
							cur.name = name
						}
						// if first chunk sent empty args and done has full args that we already partially streamed, OK
					} else {
						// tool call only appeared at done
						if tc := toolCallFromItem(item); tc != nil {
							writeChunk(map[string]interface{}{
								"tool_calls": []map[string]interface{}{{
									"index": toolIndex,
									"id":    tc.ID,
									"type":  "function",
									"function": map[string]string{
										"name":      tc.Name,
										"arguments": tc.Arguments,
									},
								}},
							}, nil)
							toolCalls = append(toolCalls, *tc)
							toolIndex++
						}
					}
					finishFC()
				}
			}
		}

		if f := extractCompletedOutputText(ev); f != "" && len([]rune(f)) > len([]rune(bestFull)) {
			bestFull = f
		}
		if t == "response.completed" || t == "response.done" {
			res.ToolCalls = appendToolCallsFromEvent(ev, res.ToolCalls)
		}
		if inc, reason := incompleteFromEvent(ev); inc {
			res.Incomplete = true
			if reason != "" {
				res.Reason = reason
			}
		}
		res.InTok, res.OutTok = mergeUsage(ev, res.InTok, res.OutTok)
	}
	if err = scanner.Err(); err != nil {
		return res, err
	}

		gotThink := thinkingAssembled.String()
	if bestThinking != "" && len([]rune(bestThinking)) > len([]rune(gotThink)) {
		rest := bestThinking
		if gotThink != "" && strings.HasPrefix(bestThinking, gotThink) {
			rest = strings.TrimPrefix(bestThinking, gotThink)
		}
		if rest != "" {
			logger.Infof("[Grok] live OpenAI thinking gap-fill +%d runes", len([]rune(rest)))
			emitThinking(rest)
		}
	}

got := assembled.String()
	if bestFull != "" && len([]rune(bestFull)) > len([]rune(got)) {
		rest := bestFull
		if got != "" && strings.HasPrefix(bestFull, got) {
			rest = strings.TrimPrefix(bestFull, got)
		}
		if rest != "" {
			logger.Infof("[Grok] live OpenAI gap-fill +%d runes", len([]rune(rest)))
			writeChunk(map[string]interface{}{"content": rest}, nil)
			assembled.WriteString(rest)
			got = assembled.String()
		}
	}
	finishFC()

	// Prefer streamed toolCalls; merge any from completed if missing
	if len(toolCalls) == 0 && len(res.ToolCalls) > 0 {
		toolCalls = res.ToolCalls
	}
	res.Text = got
	res.Thinking = thinkingAssembled.String()
	if bestThinking != "" && len([]rune(bestThinking)) > len([]rune(res.Thinking)) {
		res.Thinking = bestThinking
	}
	res.ToolCalls = toolCalls
	if res.OutTok <= 0 {
		res.OutTok = maxInt(1, (len([]rune(got))+len(toolCalls)*16)/4)
	}

	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	} else if res.Incomplete {
		finish = "length"
	}
	// final chunk with finish_reason + usage
	bb, _ := json.Marshal(map[string]interface{}{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []map[string]interface{}{{
			"index": 0, "delta": map[string]interface{}{}, "finish_reason": finish,
		}},
		"usage": map[string]int{
			"prompt_tokens": res.InTok, "completion_tokens": res.OutTok, "total_tokens": res.InTok + res.OutTok,
		},
	})
	fmt.Fprintf(w, "data: %s\n\n", bb)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
	if len(toolCalls) > 0 {
		logger.Infof("[Grok] openai tool_calls count=%d finish=%s", len(toolCalls), finish)
	}
	return res, nil
}

// streamGrokLiveToClaude pipes text + tool_use (function_call) as Anthropic SSE.
// 9router Anthropic clients require tool_use blocks or the agent turn looks "cut".
func (h *Handler) streamGrokLiveToClaude(w http.ResponseWriter, body io.Reader, model string) (res grokCollectResult, err error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return res, fmt.Errorf("streaming unsupported")
	}
	var writeMu sync.Mutex
	setAgentSSEHeaders(w)
	setAnthropicResponseHeaders(w, "")
	w.WriteHeader(200)
	stopKA := h.startGrokKeepaliveLocked(w, "claude", &writeMu)
	defer stopKA()

	lockedSend := func(event string, data interface{}) {
		writeMu.Lock()
		defer writeMu.Unlock()
		h.sendSSE(w, flusher, event, data)
	}

	msgID := anthropicMsgID()
	// Non-zero-looking usage keeps some Claude clients happier while streaming.
	lockedSend( "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": msgID, "type": "message", "role": "assistant", "model": model,
			"content": []interface{}{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		},
	})

	var assembled strings.Builder
	var bestFull string
	var thinkingAssembled strings.Builder
	var bestThinking string
	wantThinking := modelWantsThinkingUI(model)
	textBlockOpen := false
	thinkingOpen := false
	textIndex := 0
	thinkingIndex := 0
	nextIndex := 0
	type activeFC struct {
		index int
		id    string
		name  string
		args  strings.Builder
	}
	var cur *activeFC
	toolCalls := []grokToolCall{}

	openThinking := func() {
		if !wantThinking || thinkingOpen {
			return
		}
		thinkingIndex = nextIndex
		nextIndex++
		lockedSend( "content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": thinkingIndex,
			"content_block": map[string]interface{}{"type": "thinking", "thinking": ""},
		})
		thinkingOpen = true
	}
	closeThinking := func() {
		if !thinkingOpen {
			return
		}
		lockedSend( "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": thinkingIndex})
		thinkingOpen = false
	}
	emitThinking := func(d string) {
		if !wantThinking || d == "" {
			return
		}
		openThinking()
		thinkingAssembled.WriteString(d)
		lockedSend( "content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": thinkingIndex,
			"delta": map[string]string{"type": "thinking_delta", "thinking": d},
		})
	}
	openText := func() {
		if textBlockOpen {
			return
		}
		closeThinking()
		textIndex = nextIndex
		nextIndex++
		lockedSend( "content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": textIndex,
			"content_block": map[string]interface{}{"type": "text", "text": ""},
		})
		textBlockOpen = true
	}
	closeText := func() {
		if !textBlockOpen {
			return
		}
		lockedSend( "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": textIndex})
		textBlockOpen = false
	}
	emitText := func(d string) {
		d = stripThinkTags(d)
		if d == "" {
			return
		}
		openText()
		assembled.WriteString(d)
		lockedSend( "content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": textIndex,
			"delta": map[string]string{"type": "text_delta", "text": d},
		})
	}
	finishFC := func() {
		if cur == nil {
			return
		}
		lockedSend( "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": cur.index})
		toolCalls = append(toolCalls, grokToolCall{ID: cur.id, Name: cur.name, Arguments: cur.args.String()})
		cur = nil
	}

	scanner := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)
	lastPing := time.Now()
	for scanner.Scan() {
		line := scanner.Text()
		if time.Since(lastPing) > 8*time.Second {
			lockedSend( "ping", map[string]interface{}{"type": "ping"})
			lastPing = time.Now()
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		t := eventType(ev)

		if d := extractReasoningSummaryDelta(ev); d != "" {
			emitThinking(d)
			lastPing = time.Now()
		}
		if d := extractOutputTextDelta(ev); d != "" {
			finishFC()
			emitText(d)
			lastPing = time.Now()
		}
		if th := extractCompletedReasoningText(ev); th != "" && len([]rune(th)) > len([]rune(bestThinking)) {
			bestThinking = th
		}

		if t == "response.output_item.added" || strings.HasSuffix(t, "output_item.added") {
			item, _ := ev["item"].(map[string]interface{})
			if item == nil {
				if d, ok := ev["delta"].(map[string]interface{}); ok {
					item, _ = d["item"].(map[string]interface{})
				}
			}
			if item != nil {
				itype, _ := item["type"].(string)
				if itype == "function_call" || itype == "custom_tool_call" {
					closeThinking()
					closeText()
					finishFC()
					name, _ := item["name"].(string)
					callID, _ := item["call_id"].(string)
					if callID == "" {
						callID, _ = item["id"].(string)
					}
					if callID == "" {
						callID = "toolu_" + uuid.New().String()
					}
					idx := nextIndex
					nextIndex++
					cur = &activeFC{index: idx, id: callID, name: name}
					if args, ok := item["arguments"].(string); ok && args != "" {
						cur.args.WriteString(args)
					}
					lockedSend( "content_block_start", map[string]interface{}{
						"type": "content_block_start", "index": idx,
						"content_block": map[string]interface{}{
							"type": "tool_use", "id": callID, "name": name, "input": map[string]interface{}{},
						},
					})
					if cur.args.Len() > 0 {
						lockedSend( "content_block_delta", map[string]interface{}{
							"type": "content_block_delta", "index": idx,
							"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": cur.args.String()},
						})
					}
					lastPing = time.Now()
				}
			}
		}

		if t == "response.function_call_arguments.delta" || strings.Contains(t, "function_call_arguments.delta") ||
			t == "response.custom_tool_call_input.delta" || strings.Contains(t, "custom_tool_call_input.delta") {
			if cur != nil {
				d, _ := ev["delta"].(string)
				if d != "" {
					cur.args.WriteString(d)
					lockedSend( "content_block_delta", map[string]interface{}{
						"type": "content_block_delta", "index": cur.index,
						"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": d},
					})
					lastPing = time.Now()
				}
			}
		}

		if t == "response.output_item.done" || strings.HasSuffix(t, "output_item.done") {
			item, _ := ev["item"].(map[string]interface{})
			if item != nil {
				itype, _ := item["type"].(string)
				if itype == "function_call" || itype == "custom_tool_call" {
					if cur != nil {
						if args, ok := item["arguments"].(string); ok && args != "" && cur.args.Len() == 0 {
							cur.args.WriteString(args)
							lockedSend( "content_block_delta", map[string]interface{}{
								"type": "content_block_delta", "index": cur.index,
								"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": args},
							})
						}
						if name, ok := item["name"].(string); ok && name != "" {
							cur.name = name
						}
					}
					finishFC()
				}
			}
		}

		if f := extractCompletedOutputText(ev); f != "" && len([]rune(f)) > len([]rune(bestFull)) {
			bestFull = f
		}
		if inc, reason := incompleteFromEvent(ev); inc {
			res.Incomplete = true
			if reason != "" {
				res.Reason = reason
			}
		}
		res.InTok, res.OutTok = mergeUsage(ev, res.InTok, res.OutTok)
	}
	if err = scanner.Err(); err != nil {
		return res, err
	}

	gotThink := thinkingAssembled.String()
	if bestThinking != "" && len([]rune(bestThinking)) > len([]rune(gotThink)) {
		rest := bestThinking
		if gotThink != "" && strings.HasPrefix(bestThinking, gotThink) {
			rest = strings.TrimPrefix(bestThinking, gotThink)
		}
		if rest != "" {
			logger.Infof("[Grok] live Claude thinking gap-fill +%d runes", len([]rune(rest)))
			emitThinking(rest)
		}
	}
	closeThinking()

	got := assembled.String()
	if bestFull != "" && len([]rune(bestFull)) > len([]rune(got)) {
		rest := bestFull
		if got != "" && strings.HasPrefix(bestFull, got) {
			rest = strings.TrimPrefix(bestFull, got)
		}
		if rest != "" {
			logger.Infof("[Grok] live Claude gap-fill +%d runes", len([]rune(rest)))
			emitText(rest)
			got = assembled.String()
		}
	}
	finishFC()
	closeThinking()
	closeText()

	res.Text = got
	res.Thinking = thinkingAssembled.String()
	if bestThinking != "" && len([]rune(bestThinking)) > len([]rune(res.Thinking)) {
		res.Thinking = bestThinking
	}
	res.ToolCalls = toolCalls
	if res.OutTok <= 0 {
		res.OutTok = maxInt(1, len([]rune(got))/4)
	}
	stop := "end_turn"
	if len(toolCalls) > 0 {
		stop = "tool_use"
	} else if res.Incomplete {
		stop = "max_tokens"
	}
	lockedSend( "message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]int{"input_tokens": maxInt(1, res.InTok), "output_tokens": maxInt(1, res.OutTok)},
	})
	lockedSend( "message_stop", map[string]interface{}{"type": "message_stop"})
	if len(toolCalls) > 0 {
		logger.Infof("[Grok] claude tool_use count=%d stop=%s", len(toolCalls), stop)
	}
	return res, nil
}

func (h *Handler) handleGrokOpenAIChat(w http.ResponseWriter, r *http.Request, req *OpenAIRequest) {
	h.handleGrokWithFormat(w, r, req, "openai", "")
}

func (h *Handler) handleGrokClaudeMessages(w http.ResponseWriter, r *http.Request, req *ClaudeRequest) {
	h.handleGrokWithFormat(w, r, claudeRequestToOpenAI(req), "claude", "")
}

func (h *Handler) handleGrokWithFormat(w http.ResponseWriter, r *http.Request, req *OpenAIRequest, format, displayModel string) {
	reqStart := time.Now()
	apiKeyID := apiKeyIDFromContext(r.Context())
	stream := req.Stream
	clientModel := req.Model
	silent := displayModel != "" && !IsGrokModel(displayModel)
	responseModel := clientModel
	if displayModel != "" {
		responseModel = displayModel
	}

	logEndpoint := "openai-grok"
	if format == "claude" {
		logEndpoint = "claude-grok"
	}
	if silent {
		logEndpoint += "-silent"
	}
	lastTriedAccountID := ""

	sendErr := func(status int, errType, msg string) {
		// Admin request log table previously only recorded successes for Grok.
		h.recordFailureWithDetails(logEndpoint, clientModel, lastTriedAccountID, fmt.Errorf("%s", msg))
		// Re-map through Kiro-style classifier (status/type may be overridden).
		st, et, public := kiroStylePublicError(msg, silent)
		if silent {
			// Prefer classifier for silent disguise path (Claude/OpenAI via Grok).
			status, errType, public = st, et, public
		} else {
			// Explicit grok path: keep short public message; align OpenAI 5xx type with Kiro.
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

		// Already streaming: Anthropic SSE error event (same shape as Kiro mid-stream).
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

		// Non-stream / before body: match Kiro sendClaudeError / sendOpenAIError exactly.
		if format == "claude" {
			h.sendClaudeError(w, status, errType, msg)
			return
		}
		h.sendOpenAIError(w, status, errType, msg)
	}

	gp := pool.GetGrokPool()
	if gp.Count() == 0 {
		gp.Reload()
	}
	if gp.Count() == 0 {
		sendErr(503, "api_error", "No available accounts")
		return
	}

	upstreamModel, effort := ResolveGrokModel(clientModel)
	beforeTok := estimateOpenAIRequestInputTokens(req)
	afterTok := trimOpenAIRequestForGrok(req)
	if afterTok != beforeTok || beforeTok > grokMaxPromptTokens-grokPromptSafetyTokens {
		logger.Warnf("[Grok] context trim est %d -> %d msgs=%d (limit=%d)", beforeTok, afterTok, len(req.Messages), grokMaxPromptTokens-grokPromptSafetyTokens)
	}
	bodyMap := buildGrokRequestBody(req, upstreamModel, effort, responseModel)
	rawBody, _ := json.Marshal(bodyMap)
	logger.Infof("[Grok] start upstream=%s effort=%s max_out=%d silent=%v stream=%v display=%s est_in=%d body_bytes=%d",
		upstreamModel, effort, grokMaxOutputTokens, silent, stream, responseModel, afterTok, len(rawBody))

	excluded := map[string]bool{}
	var lastErr error
	// Try every enabled account (not capped at 8). Least-in-flight pick each attempt.
	maxTry := gp.Count()
	if maxTry < 1 {
		maxTry = 1
	}
	// Extra attempts beyond Count to allow refresh-retry after token refresh.
	if maxTry < gp.Count()+2 {
		maxTry = gp.Count() + 2
	}
	if maxTry > 24 {
		maxTry = 24
	}

	for attempt := 0; attempt < maxTry; attempt++ {
		acc := gp.GetNextForCustomer(apiKeyID, excluded)
		if acc == nil {
			// No account from least-in-flight; try pure excluding again after clear (compat).
			gp.ClearStickyCustomer(apiKeyID)
			acc = gp.GetNextExcluding(excluded)
		}
		if acc == nil {
			// Last resort: every account is cooling down (soft-banned by an earlier
			// request after transient proxy/auth errors) but none was tried in THIS
			// request. Ignore cooldown so we don't 503 the whole window. Only when
			// nothing has been excluded yet this request.
			if len(excluded) == 0 {
				acc = gp.PickIgnoringCooldown(excluded)
				if acc != nil {
					logger.Warnf("[Grok] all accounts cooling down; retrying account=%s ignoring cooldown", acc.Email)
				}
			}
		}
		if acc == nil {
			logger.Warnf("[Grok] no account left attempt=%d excluded=%d count=%d available=%d last=%v",
				attempt, len(excluded), gp.Count(), gp.AvailableCount(), lastErr)
			break
		}
		logger.Infof("[Grok] try account=%s attempt=%d/%d customer=%s", acc.Email, attempt+1, maxTry, apiKeyID)
		lastTriedAccountID = acc.ID
		gp.Acquire(acc.ID)
		if err := h.ensureValidGrokToken(acc); err != nil {
			lastErr = err
			excluded[acc.ID] = true
			gp.RecordError(acc.ID)
			logger.Warnf("[Grok] token error %s: %v", acc.Email, err)
			gp.Release(acc.ID)
			continue
		}

		sessionID := uuid.New().String()
		reqID := uuid.New().String()

		var resp *http.Response
		var err error
		for tTry := 0; tTry < 2; tTry++ {
			if tTry > 0 {
				time.Sleep(time.Duration(1500+tTry*500) * time.Millisecond)
			}
			httpReq, e := http.NewRequest(http.MethodPost, grokResponsesURL, bytes.NewReader(rawBody))
			if e != nil {
				err = e
				break
			}
			httpReq.Header = buildGrokHeaders(acc, sessionID, reqID, 1, upstreamModel)
			resp, err = getGrokHTTPClient(acc).Do(httpReq)
			if err != nil {
				lastErr = fmt.Errorf("network: %w", err)
				continue
			}
			if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				lastErr = fmt.Errorf("transient HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 200))
				logger.Warnf("[Grok] transient %d try=%d %s", resp.StatusCode, tTry+1, acc.Email)
				resp = nil
				continue
			}
			break
		}
		if err != nil || resp == nil {
			excluded[acc.ID] = true
			gp.RecordError(acc.ID)
			gp.Release(acc.ID)
			continue
		}

		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			bodyPreview := truncateStr(string(b), 400)
			// Log full provider detail server-side only.
			logger.Warnf("[Grok] auth HTTP %d account=%s body=%s", resp.StatusCode, acc.Email, bodyPreview)
			// Client-facing lastErr uses short labels (sanitized later).
			lastErr = fmt.Errorf("auth HTTP %d: invalid or expired credentials", resp.StatusCode)
			excluded[acc.ID] = true
			gp.RecordError(acc.ID)
			gp.ClearStickyCustomer(apiKeyID)
			if resp.StatusCode == 401 {
				if h.refreshGrokToken(acc) == nil {
					delete(excluded, acc.ID)
					logger.Infof("[Grok] token refreshed after 401, will retry account=%s", acc.Email)
				} else {
					// Soft cooldown — do NOT permanently disable (empties pool for all customers).
					gp.Cooldown(acc.ID, "auth failed", 5*time.Minute)
					_ = config.SetGrokAccountQuota(acc.ID, "error", "auth failed / no access (cooldown 5m)", -1, 0)
					logger.Warnf("[Grok] cooldown 5m after auth fail account=%s", acc.Email)
				}
			} else if isGrokPermissionDeniedBody(string(b)) {
				// xAI intermittently revokes chat-endpoint access for a token
				// (console.x.ai). This is usually TRANSIENT — access returns within
				// ~30m-1h (often sooner). So instead of a long ban, refresh the token
				// (mimics "clear cache / re-auth") and apply only a SHORT cooldown; the
				// background health-checker re-tests and clears it as soon as access
				// returns, keeping the account in rotation.
				_ = h.refreshGrokToken(acc)
				gp.Cooldown(acc.ID, "chat access temporarily denied (console.x.ai)", grokPermissionDeniedCooldown)
				_ = config.SetGrokAccountQuota(acc.ID, "no_access", "chat endpoint temporarily denied — auto-retrying", -1, 0)
				logger.Warnf("[Grok] permission-denied account=%s — refreshed token, short cooldown %s, health-checker will re-test", acc.Email, grokPermissionDeniedCooldown)
			} else {
				gp.Cooldown(acc.ID, "auth forbidden", grokAuthForbiddenCooldown)
			}
			gp.Release(acc.ID)
			continue
		}
		if resp.StatusCode == 402 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			// Log the raw provider body server-side only for debugging; NEVER store it
			// in QuotaMessage or lastErr — the xAI/grok body would then surface in the
			// admin UI badge tooltip and could leak the provider identity to customers.
			logger.Warnf("[Grok] 402 quota exhausted account=%s body=%s", acc.Email, truncateStr(string(b), 200))
			msg := "Usage limit reached"
			_ = config.SetGrokAccountQuota(acc.ID, "exhausted", "Usage limit reached (cooldown 10m)", 0, 0)
			gp.Cooldown(acc.ID, "quota exhausted", 10*time.Minute)
			excluded[acc.ID] = true
			gp.RecordError(acc.ID)
			lastErr = fmt.Errorf("%s", msg)
			logger.Warnf("[Grok] cooldown 30m quota exhausted account=%s — try next", acc.Email)
			// Do not return raw 402 body to client yet; try other accounts first.
			gp.Release(acc.ID)
			continue
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			bodyStr := string(b)
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(bodyStr, 400))
			excluded[acc.ID] = true
			gp.RecordError(acc.ID)
			gp.Release(acc.ID)
			// Prompt too long is identical for every account — do not burn the pool.
			if isGrokPromptTooLongError(bodyStr) || isGrokPromptTooLongError(lastErr.Error()) {
				logger.Warnf("[Grok] prompt too long account=%s — stop failover", acc.Email)
				sendErr(400, "invalid_request_error", "Request too large for model context window. Reduce conversation/tool output size and retry.")
				return
			}
			continue
		}

		// STREAM: live pipe like 9router (token-by-token). Collect-first caused many
		// clients to abort while waiting (only pings / silence) â†’ looked like "cut".
		// NON-STREAM: fully collect then JSON.
		var result grokCollectResult
		var cErr error
		if stream {
			if format == "claude" {
				result, cErr = h.streamGrokLiveToClaude(w, resp.Body, responseModel)
			} else {
				result, cErr = h.streamGrokLiveToOpenAI(w, resp.Body, responseModel)
			}
			resp.Body.Close()
			if cErr != nil {
				logger.Warnf("[Grok] live stream error after start: %v", cErr)
				// headers already sent — cannot retry accounts
				h.recordFailureWithDetails(logEndpoint, clientModel, acc.ID, cErr)
				gp.RecordError(acc.ID)
				gp.Release(acc.ID)
				return
			}
		} else {
			result, cErr = collectGrokResponse(resp.Body)
			resp.Body.Close()
			if cErr != nil {
				lastErr = cErr
				excluded[acc.ID] = true
				gp.RecordError(acc.ID)
				gp.Release(acc.ID)
				continue
			}
		}

		if result.InTok <= 0 {
			result.InTok = estimateOpenAIRequestInputTokens(req)
		}
		// Disguise: rewrite Grok self-ID when silent; fix token estimates to Claude-like scale.
		if format == "claude" {
			result.Text = maybeRewriteAssistantText(result.Text, silent)
			// Prefer char-based estimate when upstream usage looks inflated/odd for short replies.
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
			logger.Warnf("[Grok] incomplete reason=%s outTok=%d textLen=%d", result.Reason, result.OutTok, len([]rune(result.Text)))
		}

		credits := GrokCreditsForRequest(effort, result.InTok+result.OutTok)
		logger.Infof("[Grok] charge credits=%.2f effort=%s tokens=%d (token-based)", credits, effort, result.InTok+result.OutTok)
		h.recordSuccessForApiKey(apiKeyID, result.InTok, result.OutTok, credits)
		gp.RecordSuccess(acc.ID)
		gp.UpdateStats(acc.ID, result.InTok+result.OutTok, credits)
		h.recordSuccessLog(logEndpoint, clientModel, acc.ID, result.InTok+result.OutTok, credits, time.Since(reqStart).Milliseconds())
		logger.Infof("[Grok] done display=%s outTok=%d textLen=%d incomplete=%v stream=%v ms=%d",
			responseModel, result.OutTok, len([]rune(result.Text)), result.Incomplete, stream, time.Since(reqStart).Milliseconds())

		if stream {
			// live path already wrote SSE to completion
			gp.Release(acc.ID)
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
		gp.Release(acc.ID)
		return
	}

	// Same end-state as Kiro when pool exhausted: 503 + "No available accounts" (+ support contact).
	msg := "No available accounts"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	logger.Warnf("[Grok] FAIL all accounts silent=%v display=%s available=%d excluded=%d last=%v",
		silent, responseModel, gp.AvailableCount(), len(excluded), lastErr)
	sendErr(503, "api_error", msg)
}


func (h *Handler) trySilentGrokClaudeFallback(w http.ResponseWriter, r *http.Request, req *ClaudeRequest, requestedModel string) bool {
	gp := pool.GetGrokPool()
	if gp.Count() == 0 {
		gp.Reload()
	}
	if gp.Count() == 0 {
		return false
	}
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = req.Model
	}
	oai := claudeRequestToOpenAI(req)
	oai.Model = silentGrokUpstreamForDisplay(requestedModel)
	oai.MaxTokens = 0
	logger.Infof("[GrokSilent] claude %s -> %s", requestedModel, oai.Model)
	h.handleGrokWithFormat(w, r, oai, "claude", requestedModel)
	return true
}

func (h *Handler) trySilentGrokOpenAIFallback(w http.ResponseWriter, r *http.Request, req *OpenAIRequest, requestedModel string) bool {
	gp := pool.GetGrokPool()
	if gp.Count() == 0 {
		gp.Reload()
	}
	if gp.Count() == 0 {
		return false
	}
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = req.Model
	}
	proxyReq := *req
	proxyReq.Model = silentGrokUpstreamForDisplay(requestedModel)
	proxyReq.MaxTokens = 0
	logger.Infof("[GrokSilent] openai %s -> %s", requestedModel, proxyReq.Model)
	h.handleGrokWithFormat(w, r, &proxyReq, "openai", requestedModel)
	return true
}

func (h *Handler) kiroPoolEmpty() bool {
	if h.pool == nil {
		return true
	}
	return h.pool.AvailableCount() == 0
}

// --- Admin Grok accounts API ---

func (h *Handler) apiGetGrokAccounts(w http.ResponseWriter, r *http.Request) {
	accs := config.GetGrokAccounts()
	// redact tokens partially in list
	type view struct {
		ID           string  `json:"id"`
		Email        string  `json:"email"`
		Nickname     string  `json:"nickname"`
		DisplayName  string  `json:"displayName"`
		Enabled      bool    `json:"enabled"`
		ExpiresAt    int64   `json:"expiresAt"`
		AuthMethod   string  `json:"authMethod"`
		RequestCount int     `json:"requestCount"`
		ErrorCount   int     `json:"errorCount"`
		TotalTokens  int     `json:"totalTokens"`
		TotalCredits float64 `json:"totalCredits"`
		BanStatus    string  `json:"banStatus,omitempty"`
		BanReason    string  `json:"banReason,omitempty"`
		HasRefresh   bool    `json:"hasRefreshToken"`
		MachineId    string  `json:"machineId,omitempty"`
		ProxyURL     string  `json:"proxyURL,omitempty"`
		LastUsed     int64   `json:"lastUsed,omitempty"`
		UserID       string  `json:"userId,omitempty"`
		QuotaStatus    string  `json:"quotaStatus,omitempty"`
		QuotaMessage   string  `json:"quotaMessage,omitempty"`
		QuotaCheckedAt int64   `json:"quotaCheckedAt,omitempty"`
		QuotaRemaining float64 `json:"quotaRemaining,omitempty"`
		QuotaLimit     float64 `json:"quotaLimit,omitempty"`
	}
	gpStats := pool.GetGrokPool()
	out := make([]view, 0, len(accs))
	for _, a := range accs {
		req, errc, tok, cred, last := a.RequestCount, a.ErrorCount, a.TotalTokens, a.TotalCredits, a.LastUsed
		if r, e, t, c, l, ok := gpStats.SnapshotStats(a.ID); ok {
			req, errc, tok, cred, last = r, e, t, c, l
		}
		out = append(out, view{
			ID: a.ID, Email: a.Email, Nickname: a.Nickname, DisplayName: a.DisplayName,
			Enabled: a.Enabled, ExpiresAt: a.ExpiresAt, AuthMethod: a.AuthMethod,
			RequestCount: req, ErrorCount: errc,
			TotalTokens: tok, TotalCredits: cred,
			BanStatus: a.BanStatus, BanReason: a.BanReason,
			HasRefresh: a.RefreshToken != "",
			MachineId: a.MachineId, ProxyURL: a.ProxyURL, LastUsed: last, UserID: a.UserID,
			QuotaStatus: a.QuotaStatus, QuotaMessage: a.QuotaMessage, QuotaCheckedAt: a.QuotaCheckedAt,
			QuotaRemaining: a.QuotaRemaining, QuotaLimit: a.QuotaLimit,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"accounts": out, "count": len(out)})
}

func (h *Handler) apiAddGrokAccount(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "read body failed"})
		return
	}
	acc, err := parseGrokAccountJSON(raw)
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
	if !acc.Enabled {
		// default enable on import unless explicitly false was set ÃƒÆ’Ã‚Â¢ÃƒÂ¢Ã¢â‚¬Å¡Ã‚Â¬ÃƒÂ¢Ã¢â€šÂ¬Ã‚Â Enabled false is zero value;
		// treat missing as true if tokens present.
		acc.Enabled = true
	}
	// detect explicit enabled:false from raw
	var probe map[string]interface{}
	_ = json.Unmarshal(raw, &probe)
	if v, ok := probe["enabled"].(bool); ok {
		acc.Enabled = v
	}
	existed := config.GrokAccountIDExists(acc.ID)
	if err := config.AddGrokAccount(*acc); err != nil {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	pool.GetGrokPool().Reload()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"id":      acc.ID,
		"updated": existed,
	})
}

func (h *Handler) apiDeleteGrokAccount(w http.ResponseWriter, r *http.Request, id string) {
	if err := config.DeleteGrokAccount(id); err != nil {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	pool.GetGrokPool().Reload()
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
func (h *Handler) apiSetGrokAccountEnabled(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// allow empty body: default disable
		body.Enabled = false
	}
	reason := body.Reason
	if !body.Enabled && reason == "" {
		reason = "disabled by admin"
	}
	if err := config.SetGrokAccountEnabled(id, body.Enabled, reason); err != nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	pool.GetGrokPool().Reload()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "enabled": body.Enabled})
}


func (h *Handler) apiGetGrokAccount(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	acc := config.GetGrokAccountByID(id)
	if acc == nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	req, errc, tok, cred, last := acc.RequestCount, acc.ErrorCount, acc.TotalTokens, acc.TotalCredits, acc.LastUsed
	if r, e, t, c, l, ok := pool.GetGrokPool().SnapshotStats(acc.ID); ok {
		req, errc, tok, cred, last = r, e, t, c, l
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id": acc.ID, "email": acc.Email, "nickname": acc.Nickname, "displayName": acc.DisplayName,
		"enabled": acc.Enabled, "expiresAt": acc.ExpiresAt, "authMethod": acc.AuthMethod,
		"requestCount": req, "errorCount": errc,
		"totalTokens": tok, "totalCredits": cred,
		"banStatus": acc.BanStatus, "banReason": acc.BanReason,
		"hasRefreshToken": acc.RefreshToken != "",
		"machineId": acc.MachineId, "proxyURL": acc.ProxyURL,
		"lastUsed": last, "userId": acc.UserID, "clientId": acc.ClientID,
		"quotaStatus": acc.QuotaStatus, "quotaMessage": acc.QuotaMessage,
		"quotaCheckedAt": acc.QuotaCheckedAt, "quotaRemaining": acc.QuotaRemaining, "quotaLimit": acc.QuotaLimit,
	})
}

func (h *Handler) apiPatchGrokAccount(w http.ResponseWriter, r *http.Request, id string) {
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
	if err := config.PatchGrokAccountFields(id, machineId, proxyURL, nickname, displayName); err != nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	pool.GetGrokPool().Reload()
	acc := config.GetGrokAccountByID(id)
	out := map[string]interface{}{"success": true, "id": id}
	if acc != nil {
		out["machineId"] = acc.MachineId
		out["proxyURL"] = acc.ProxyURL
		out["nickname"] = acc.Nickname
		out["displayName"] = acc.DisplayName
	}
	_ = json.NewEncoder(w).Encode(out)
}


// probeGrokAccountQuota best-effort check of Grok Build remaining capacity.
// xAI does not expose a stable public usage API for CLI OAuth; we:
//  1) refresh token
//  2) GET /v1/billing (if available)
//  3) fallback: tiny non-stream completion (max_output_tokens=1) — detects 402 exhausted
func (h *Handler) probeGrokAccountQuota(acc *config.GrokAccount) (status, message string, remaining, limit float64, err error) {
	status = "unknown"
	remaining = -1
	limit = 0
	if acc == nil {
		return "error", "nil account", -1, 0, fmt.Errorf("nil account")
	}
	if err = h.ensureValidGrokToken(acc); err != nil {
		_ = config.SetGrokAccountQuota(acc.ID, "error", err.Error(), -1, 0)
		return "error", err.Error(), -1, 0, err
	}

	client := getGrokHTTPClient(acc)
	// 1) Try billing endpoint
	req, e := http.NewRequest(http.MethodGet, "https://cli-chat-proxy.grok.com/v1/billing", nil)
	if e == nil {
		req.Header = buildGrokHeaders(acc, uuid.New().String(), uuid.New().String(), 0, "grok-4.5")
		resp, e2 := client.Do(req)
		if e2 == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
			resp.Body.Close()
			if resp.StatusCode == 402 {
				msg := truncateStr(string(body), 300)
				if msg == "" {
					msg = "Build credits exhausted"
				}
				_ = config.SetGrokAccountQuota(acc.ID, "exhausted", msg, 0, 0)
				return "exhausted", msg, 0, 0, nil
			}
			if resp.StatusCode == 200 {
				rem, lim, msg := parseGrokQuotaJSON(body)
				st := "ok"
				if rem == 0 && lim > 0 {
					st = "exhausted"
				}
				if msg == "" {
					msg = "billing ok"
				}
				_ = config.SetGrokAccountQuota(acc.ID, st, msg, rem, lim)
				return st, msg, rem, lim, nil
			}
		}
	}

	// 2) Tiny probe completion (detects spending-limit 402 without long generation)
	probeBody := map[string]interface{}{
		"model":             "grok-4.5",
		"input":             "ping",
		"stream":            false,
		"store":             false,
		"max_output_tokens": 1,
	}
	raw, _ := json.Marshal(probeBody)
	httpReq, e := http.NewRequest(http.MethodPost, grokResponsesURL, bytes.NewReader(raw))
	if e != nil {
		_ = config.SetGrokAccountQuota(acc.ID, "error", e.Error(), -1, 0)
		return "error", e.Error(), -1, 0, e
	}
	httpReq.Header = buildGrokHeaders(acc, uuid.New().String(), uuid.New().String(), 1, "grok-4.5")
	resp, e := client.Do(httpReq)
	if e != nil {
		_ = config.SetGrokAccountQuota(acc.ID, "error", e.Error(), -1, 0)
		return "error", e.Error(), -1, 0, e
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()

	switch {
	case resp.StatusCode == 402:
		msg := truncateStr(string(body), 300)
		if msg == "" {
			msg = "Build credits exhausted (spending limit)"
		}
		_ = config.SetGrokAccountQuota(acc.ID, "exhausted", msg, 0, 0)
		return "exhausted", msg, 0, 0, nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		msg := "auth failed: " + truncateStr(string(body), 200)
		_ = config.SetGrokAccountQuota(acc.ID, "error", msg, -1, 0)
		return "error", msg, -1, 0, fmt.Errorf("%s", msg)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		msg := "upstream accepted probe (quota available; exact remaining unknown)"
		_ = config.SetGrokAccountQuota(acc.ID, "ok", msg, -1, 0)
		return "ok", msg, -1, 0, nil
	default:
		msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 200))
		_ = config.SetGrokAccountQuota(acc.ID, "unknown", msg, -1, 0)
		return "unknown", msg, -1, 0, nil
	}
}

func parseGrokQuotaJSON(body []byte) (remaining, limit float64, message string) {
	remaining = -1
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return -1, 0, ""
	}
	// common shapes
	for _, k := range []string{"remaining", "remaining_credits", "credits_remaining", "balance"} {
		if v, ok := m[k]; ok {
			if f, ok2 := asFloat(v); ok2 {
				remaining = f
			}
		}
	}
	for _, k := range []string{"limit", "credits_limit", "spending_limit", "total"} {
		if v, ok := m[k]; ok {
			if f, ok2 := asFloat(v); ok2 {
				limit = f
			}
		}
	}
	if u, ok := m["usage"].(map[string]interface{}); ok {
		if v, ok2 := asFloat(u["remaining"]); ok2 {
			remaining = v
		}
		if v, ok2 := asFloat(u["limit"]); ok2 {
			limit = v
		}
	}
	if s, ok := m["message"].(string); ok {
		message = s
	}
	b, _ := json.Marshal(m)
	if message == "" {
		message = truncateStr(string(b), 200)
	}
	return remaining, limit, message
}

func asFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		var f float64
		_, err := fmt.Sscanf(n, "%f", &f)
		return f, err == nil
	default:
		return 0, false
	}
}

func (h *Handler) apiRefreshGrokQuota(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	acc := config.GetGrokAccountByID(id)
	if acc == nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	status, msg, rem, lim, err := h.probeGrokAccountQuota(acc)
	pool.GetGrokPool().Reload()
	out := map[string]interface{}{
		"success":        err == nil || status != "error",
		"id":             id,
		"email":          acc.Email,
		"quotaStatus":    status,
		"quotaMessage":   msg,
		"quotaRemaining": rem,
		"quotaLimit":     lim,
		"quotaCheckedAt": time.Now().Unix(),
	}
	if err != nil && status == "error" {
		w.WriteHeader(502)
		out["error"] = err.Error()
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) apiRefreshAllGrokQuota(w http.ResponseWriter, r *http.Request) {
	accs := config.GetGrokAccounts()
	results := make([]map[string]interface{}, 0, len(accs))
	for i := range accs {
		a := accs[i]
		status, msg, rem, lim, err := h.probeGrokAccountQuota(&a)
		row := map[string]interface{}{
			"id": a.ID, "email": a.Email,
			"quotaStatus": status, "quotaMessage": msg,
			"quotaRemaining": rem, "quotaLimit": lim,
		}
		if err != nil {
			row["error"] = err.Error()
		}
		results = append(results, row)
	}
	pool.GetGrokPool().Reload()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"count":   len(results),
		"results": results,
	})
}

// parseGrokAccountJSON accepts:
// - native GrokAccount JSON
// - 9router providerConnections row shape (data nested or flattened)
// testGrokAccountHello sends a tiny completion through the same HTTP client/proxy
// path as production traffic. Reports proxy usage, latency, and upstream status.
func (h *Handler) testGrokAccountHello(acc *config.GrokAccount) map[string]interface{} {
	out := map[string]interface{}{
		"id":    "",
		"email": "",
		"ok":    false,
	}
	if acc == nil {
		out["error"] = "nil account"
		return out
	}
	out["id"] = acc.ID
	out["email"] = acc.Email
	out["enabled"] = acc.Enabled
	proxyURL := strings.TrimSpace(acc.ProxyURL)
	out["proxyURL"] = proxyURL
	out["proxyConfigured"] = proxyURL != ""
	// getGrokHTTPClient uses per-account proxy when ProxyURL is set; otherwise default client / env proxy.
	out["proxyUsed"] = proxyURL != ""
	out["machineId"] = strings.TrimSpace(acc.MachineId)

	start := time.Now()
	if err := h.ensureValidGrokToken(acc); err != nil {
		out["ms"] = time.Since(start).Milliseconds()
		out["stage"] = "token"
		out["error"] = err.Error()
		logger.Warnf("[GrokTest] token fail email=%s proxy=%q err=%v", acc.Email, proxyURL, err)
		return out
	}
	out["tokenMs"] = time.Since(start).Milliseconds()

	body := map[string]interface{}{
		"model": "grok-4.5",
		"input": []map[string]interface{}{
			{"type": "message", "role": "user", "content": "Reply with exactly: hello"},
		},
		"stream":            false,
		"store":             false,
		"max_output_tokens": 32,
		"reasoning":         map[string]interface{}{"effort": "low", "summary": "concise"},
	}
	raw, _ := json.Marshal(body)
	httpReq, err := http.NewRequest(http.MethodPost, grokResponsesURL, bytes.NewReader(raw))
	if err != nil {
		out["ms"] = time.Since(start).Milliseconds()
		out["stage"] = "request"
		out["error"] = err.Error()
		return out
	}
	httpReq.Header = buildGrokHeaders(acc, uuid.New().String(), uuid.New().String(), 1, "grok-4.5")
	client := getGrokHTTPClient(acc)
	logger.Infof("[GrokTest] hello start email=%s proxyConfigured=%v proxyURL=%q", acc.Email, proxyURL != "", proxyURL)

	resp, err := client.Do(httpReq)
	elapsed := time.Since(start).Milliseconds()
	out["ms"] = elapsed
	if err != nil {
		out["stage"] = "upstream"
		out["error"] = err.Error()
		errS := strings.ToLower(err.Error())
		if strings.Contains(errS, "proxy") || strings.Contains(errS, "connect") {
			out["proxyError"] = true
		}
		logger.Warnf("[GrokTest] hello network fail email=%s proxy=%q ms=%d err=%v", acc.Email, proxyURL, elapsed, err)
		return out
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
	out["httpStatus"] = resp.StatusCode
	out["stage"] = "done"
	preview := truncateStr(string(b), 240)
	out["bodyPreview"] = preview

	switch {
	case resp.StatusCode == 402:
		out["ok"] = false
		out["status"] = "exhausted"
		out["error"] = "Build credits exhausted"
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		out["ok"] = false
		out["status"] = "auth_error"
		out["error"] = "auth failed: " + preview
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		out["ok"] = true
		out["status"] = "ok"
		var ev map[string]interface{}
		if json.Unmarshal(b, &ev) == nil {
			if t := extractCompletedOutputText(ev); t != "" {
				out["reply"] = truncateStr(t, 120)
			}
		}
		if _, has := out["reply"]; !has {
			out["reply"] = preview
		}
	default:
		out["ok"] = false
		out["status"] = "http_error"
		out["error"] = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, preview)
	}
	logger.Infof("[GrokTest] hello done email=%s ok=%v status=%v http=%d proxy=%q ms=%d",
		acc.Email, out["ok"], out["status"], resp.StatusCode, proxyURL, elapsed)
	return out
}

// StartGrokHealthChecker launches a background goroutine that periodically re-tests
// Grok accounts currently in cooldown and clears the cooldown as soon as they work
// again. xAI's permission-denied is usually transient (access returns within
// ~30m-1h, often sooner), so this keeps recovered accounts in rotation without
// waiting out the full cooldown or requiring manual intervention. Safe to call once
// at startup; it runs for the process lifetime.
func (h *Handler) StartGrokHealthChecker() {
	go func() {
		ticker := time.NewTicker(grokHealthCheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			gp := pool.GetGrokPool()
			cooling := gp.CoolingDownAccounts()
			if len(cooling) == 0 {
				continue
			}
			for i := range cooling {
				acc := cooling[i]
				// Refresh token first (mimics "clear cache / re-auth") then probe.
				_ = h.refreshGrokToken(&acc)
				res := h.testGrokAccountHello(&acc)
				if ok, _ := res["ok"].(bool); ok {
					gp.ClearCooldown(acc.ID)
					_ = config.SetGrokAccountQuota(acc.ID, "active", "", -1, 0)
					logger.Infof("[GrokHealth] account=%s recovered — cooldown cleared, back in rotation", acc.Email)
				} else {
					logger.Debugf("[GrokHealth] account=%s still unavailable status=%v", acc.Email, res["status"])
				}
			}
		}
	}()
	logger.Infof("[GrokHealth] background health-checker started (interval=%s)", grokHealthCheckInterval)
}

func (h *Handler) apiTestGrokAccount(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	acc := config.GetGrokAccountByID(id)
	if acc == nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	res := h.testGrokAccountHello(acc)
	if ok, _ := res["ok"].(bool); !ok {
		w.WriteHeader(502)
	}
	_ = json.NewEncoder(w).Encode(res)
}

func (h *Handler) apiTestAllGrokAccounts(w http.ResponseWriter, r *http.Request) {
	// default: only enabled. ?all=1 includes disabled.
	onlyEnabled := r.URL.Query().Get("all") != "1"
	accs := config.GetGrokAccounts()
	results := make([]map[string]interface{}, 0, len(accs))
	okN, failN := 0, 0
	for i := range accs {
		a := accs[i]
		if onlyEnabled && !a.Enabled {
			results = append(results, map[string]interface{}{
				"id": a.ID, "email": a.Email, "enabled": false, "ok": false,
				"status": "skipped", "skipped": true,
				"proxyURL": a.ProxyURL, "proxyConfigured": strings.TrimSpace(a.ProxyURL) != "",
			})
			continue
		}
		acc := a
		res := h.testGrokAccountHello(&acc)
		if ok, _ := res["ok"].(bool); ok {
			okN++
		} else {
			failN++
		}
		results = append(results, res)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"count":   len(results),
		"ok":      okN,
		"failed":  failN,
		"results": results,
	})
}


func parseGrokAccountJSON(raw []byte) (*config.GrokAccount, error) {
	// flattened first
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
		ExpiresAt    interface{} `json:"expiresAt"`
		ExpiresIn    int64       `json:"expiresIn"`
		Scope        string      `json:"scope"`
		ClientID     string      `json:"clientId"`
		AuthMethod   string      `json:"authMethod"`
		UserID       string      `json:"userId"`
		IDToken      string      `json:"idToken"`
		Enabled      *bool       `json:"enabled"`
		// nested 9router style
		Data json.RawMessage `json:"data"`
		ProviderSpecificData *struct {
			AuthMethod string `json:"authMethod"`
			IDToken    string `json:"idToken"`
			Email      string `json:"email"`
			UserID     string `json:"userId"`
		} `json:"providerSpecificData"`
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	// If data is nested object (full connection export), merge
	if len(flat.Data) > 0 && flat.AccessToken == "" {
		var nested map[string]interface{}
		if json.Unmarshal(flat.Data, &nested) == nil {
			if v, ok := nested["accessToken"].(string); ok {
				flat.AccessToken = v
			}
			if v, ok := nested["refreshToken"].(string); ok {
				flat.RefreshToken = v
			}
			if v, ok := nested["displayName"].(string); ok {
				flat.DisplayName = v
			}
			if v, ok := nested["scope"].(string); ok {
				flat.Scope = v
			}
			if v, ok := nested["expiresAt"]; ok {
				flat.ExpiresAt = v
			}
			if v, ok := nested["expiresIn"].(float64); ok {
				flat.ExpiresIn = int64(v)
			}
			if ps, ok := nested["providerSpecificData"].(map[string]interface{}); ok {
				if v, ok := ps["authMethod"].(string); ok {
					flat.AuthMethod = v
				}
				if v, ok := ps["idToken"].(string); ok {
					flat.IDToken = v
				}
				if v, ok := ps["email"].(string); ok && flat.Email == "" {
					flat.Email = v
				}
				if v, ok := ps["userId"].(string); ok {
					flat.UserID = v
				}
			}
		}
	}
	if flat.ProviderSpecificData != nil {
		if flat.AuthMethod == "" {
			flat.AuthMethod = flat.ProviderSpecificData.AuthMethod
		}
		if flat.IDToken == "" {
			flat.IDToken = flat.ProviderSpecificData.IDToken
		}
		if flat.Email == "" {
			flat.Email = flat.ProviderSpecificData.Email
		}
		if flat.UserID == "" {
			flat.UserID = flat.ProviderSpecificData.UserID
		}
	}
	if flat.Email == "" {
		flat.Email = flat.Name
	}
	if flat.Nickname == "" {
		flat.Nickname = flat.Email
	}
	acc := &config.GrokAccount{
		ID:           flat.ID,
		Email:        flat.Email,
		Nickname:     flat.Nickname,
		DisplayName:  flat.DisplayName,
		AccessToken:  flat.AccessToken,
		RefreshToken: flat.RefreshToken,
		Scope:        flat.Scope,
		ClientID:     flat.ClientID,
		AuthMethod:   flat.AuthMethod,
		UserID:       flat.UserID,
		IDToken:      flat.IDToken,
		Enabled:      true,
	}
	if flat.Enabled != nil {
		acc.Enabled = *flat.Enabled
	}
	acc.ExpiresAt = parseExpiresAt(flat.ExpiresAt, flat.ExpiresIn)
	if acc.ClientID == "" {
		acc.ClientID = config.DefaultGrokClientID
	}
	if acc.AuthMethod == "" {
		acc.AuthMethod = "device_code"
	}
	return acc, nil
}

func parseExpiresAt(v interface{}, expiresIn int64) int64 {
	switch t := v.(type) {
	case float64:
		// if looks like ms
		if t > 1e12 {
			return int64(t / 1000)
		}
		return int64(t)
	case int64:
		return t
	case string:
		if t == "" {
			break
		}
		if ts, err := time.Parse(time.RFC3339, t); err == nil {
			return ts.Unix()
		}
		if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return ts.Unix()
		}
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			if n > 1e12 {
				return n / 1000
			}
			return n
		}
	}
	if expiresIn > 0 {
		return time.Now().Unix() + expiresIn
	}
	return 0
}
