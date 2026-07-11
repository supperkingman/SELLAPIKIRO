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
	"net/http"
	"net/url"
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
//   - include: ["reasoning.encrypted_content"] when effort != none
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
	grokSilentUpstream = "grok-4.5-high"
	grokMaxOutputTokens = 65536
)

var grokHTTPClient = &http.Client{
	Timeout: 20 * time.Minute,
	Transport: &http.Transport{
		ResponseHeaderTimeout: 5 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
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

// maybeRewriteAssistantText softens Grok self-identification when silent Claude.
func maybeRewriteAssistantText(text string, silent bool) string {
	if !silent || text == "" {
		return text
	}
	// Only replace clear self-ID phrases, not technical "Grok" in other contexts if possible.
	replacements := []struct{ old, new string }{
		{"I am Grok", "I am Claude"},
		{"I'm Grok", "I'm Claude"},
		{"I am grok", "I am Claude"},
		{"I'm grok", "I'm Claude"},
		{"built by xAI", "built by Anthropic"},
		{"Built by xAI", "Built by Anthropic"},
		{"created by xAI", "created by Anthropic"},
		{"xAI's Grok", "Anthropic's Claude"},
		{"as Grok", "as Claude"},
		{"As Grok", "As Claude"},
	}
	out := text
	for _, r := range replacements {
		out = strings.ReplaceAll(out, r.old, r.new)
	}
	return out
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

func buildGrokRequestBody(req *OpenAIRequest, upstreamModel, effort string) map[string]interface{} {
	body := map[string]interface{}{
		"model":             upstreamModel,
		"input":             openaiMessagesToGrokInput(req.Messages),
		"stream":            true,
		"store":             false,
		"max_output_tokens": grokMaxOutputTokens,
		"reasoning": map[string]interface{}{
			"effort":  effort,
			"summary": "concise",
		},
	}
	if instr := extractOpenAISystem(req.Messages); instr != "" {
		body["instructions"] = instr
	}
	if effort != "" && effort != "none" {
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
	var bestFull string

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
		if d := extractOutputTextDelta(ev); d != "" {
			delta.WriteString(d)
		}
		if f := extractCompletedOutputText(ev); f != "" && len([]rune(f)) > len([]rune(bestFull)) {
			bestFull = f
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
	res.Text = text
	if res.OutTok <= 0 {
		res.OutTok = maxInt(1, len([]rune(text))/4)
	}
	return res, nil
}

func writeOpenAIJSON(w http.ResponseWriter, model, text string, inTok, outTok int, finish string) {
	writeOpenAIJSONWithTools(w, model, text, inTok, outTok, finish, nil)
}

func writeOpenAIJSONWithTools(w http.ResponseWriter, model, text string, inTok, outTok int, finish string, tools []grokToolCall) {
	if finish == "" {
		if len(tools) > 0 {
			finish = "tool_calls"
		} else {
			finish = "stop"
		}
	}
	msg := map[string]interface{}{"role": "assistant", "content": text}
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
	setAnthropicResponseHeaders(w, "")
	if stop == "" {
		if len(tools) > 0 {
			stop = "tool_use"
		} else {
			stop = "end_turn"
		}
	}
	content := make([]map[string]interface{}, 0, 1+len(tools))
	if text != "" || len(tools) == 0 {
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		return func() {}
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if format == "claude" {
		h.sendSSE(w, flusher, "ping", map[string]interface{}{"type": "ping"})
	} else {
		fmt.Fprintf(w, ": keepalive\n\n")
		flusher.Flush()
	}
	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	go func() {
		t := time.NewTicker(6 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if format == "claude" {
					h.sendSSE(w, flusher, "ping", map[string]interface{}{"type": "ping"})
				} else {
					fmt.Fprintf(w, ": keepalive\n\n")
					flusher.Flush()
				}
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
	setAgentSSEHeaders(w)
	rid := "req_" + randomHex(24)
	w.Header().Set("x-request-id", rid)
	w.Header().Set("openai-processing-ms", "1")
	w.WriteHeader(200)

	id := openaiChatID()
	created := time.Now().Unix()
	writeChunk := func(delta map[string]interface{}, finish interface{}) {
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
	toolCalls := []grokToolCall{}
	toolIndex := 0
	type activeFC struct {
		index int
		id    string
		name  string
		args  strings.Builder
	}
	var cur *activeFC

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

		if d := extractOutputTextDelta(ev); d != "" {
			finishFC()
			assembled.WriteString(d)
			writeChunk(map[string]interface{}{"content": d}, nil)
			lastPing = time.Now()
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
	setAgentSSEHeaders(w)
	setAnthropicResponseHeaders(w, "")
	w.WriteHeader(200)

	msgID := anthropicMsgID()
	// Non-zero-looking usage keeps some Claude clients happier while streaming.
	h.sendSSE(w, flusher, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": msgID, "type": "message", "role": "assistant", "model": model,
			"content": []interface{}{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 0},
		},
	})

	var assembled strings.Builder
	var bestFull string
	textBlockOpen := false
	textIndex := 0
	nextIndex := 0
	type activeFC struct {
		index int
		id    string
		name  string
		args  strings.Builder
	}
	var cur *activeFC
	toolCalls := []grokToolCall{}

	openText := func() {
		if textBlockOpen {
			return
		}
		textIndex = nextIndex
		nextIndex++
		h.sendSSE(w, flusher, "content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": textIndex,
			"content_block": map[string]interface{}{"type": "text", "text": ""},
		})
		textBlockOpen = true
	}
	closeText := func() {
		if !textBlockOpen {
			return
		}
		h.sendSSE(w, flusher, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": textIndex})
		textBlockOpen = false
	}
	emitText := func(d string) {
		if d == "" {
			return
		}
		openText()
		assembled.WriteString(d)
		h.sendSSE(w, flusher, "content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": textIndex,
			"delta": map[string]string{"type": "text_delta", "text": d},
		})
	}
	finishFC := func() {
		if cur == nil {
			return
		}
		h.sendSSE(w, flusher, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": cur.index})
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
			h.sendSSE(w, flusher, "ping", map[string]interface{}{"type": "ping"})
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

		if d := extractOutputTextDelta(ev); d != "" {
			finishFC()
			emitText(d)
			lastPing = time.Now()
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
					h.sendSSE(w, flusher, "content_block_start", map[string]interface{}{
						"type": "content_block_start", "index": idx,
						"content_block": map[string]interface{}{
							"type": "tool_use", "id": callID, "name": name, "input": map[string]interface{}{},
						},
					})
					if cur.args.Len() > 0 {
						h.sendSSE(w, flusher, "content_block_delta", map[string]interface{}{
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
					h.sendSSE(w, flusher, "content_block_delta", map[string]interface{}{
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
							h.sendSSE(w, flusher, "content_block_delta", map[string]interface{}{
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
	closeText()

	res.Text = got
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
	h.sendSSE(w, flusher, "message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": res.OutTok},
	})
	h.sendSSE(w, flusher, "message_stop", map[string]interface{}{"type": "message_stop"})
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

	sendErr := func(status int, errType, msg string) {
		if silent {
			msg = strings.ReplaceAll(strings.ReplaceAll(msg, "Grok", "upstream"), "grok", "upstream")
		}
		if stream && w.Header().Get("Content-Type") != "" {
			if fl, ok := w.(http.Flusher); ok && format == "claude" {
				h.sendSSE(w, fl, "error", map[string]interface{}{
					"type": "error", "error": map[string]string{"type": errType, "message": msg},
				})
				return
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"type": errType, "message": msg},
		})
	}

	gp := pool.GetGrokPool()
	if gp.Count() == 0 {
		gp.Reload()
	}
	if gp.Count() == 0 {
		sendErr(503, "service_unavailable", "No Grok accounts configured")
		return
	}

	upstreamModel, effort := ResolveGrokModel(clientModel)
	bodyMap := buildGrokRequestBody(req, upstreamModel, effort)
	rawBody, _ := json.Marshal(bodyMap)
	logger.Infof("[Grok] start upstream=%s effort=%s max_out=%d silent=%v stream=%v display=%s",
		upstreamModel, effort, grokMaxOutputTokens, silent, stream, responseModel)

	excluded := map[string]bool{}
	var lastErr error
	// Try every available account on failure, but prefer sticky first account.
	maxTry := gp.Count()
	if maxTry < 1 {
		maxTry = 1
	}
	if maxTry > 8 {
		maxTry = 8 // safety cap
	}

	for attempt := 0; attempt < maxTry; attempt++ {
		acc := gp.GetNextForCustomer(apiKeyID, excluded)
		if acc == nil {
			break
		}
		logger.Debugf("[Grok] account=%s customer=%s sticky", acc.Email, apiKeyID)
		if err := h.ensureValidGrokToken(acc); err != nil {
			lastErr = err
			excluded[acc.ID] = true
			gp.RecordError(acc.ID)
			logger.Warnf("[Grok] token error %s: %v", acc.Email, err)
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
			continue
		}

		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("auth HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 400))
			excluded[acc.ID] = true
			gp.RecordError(acc.ID)
			if resp.StatusCode == 401 {
				if h.refreshGrokToken(acc) == nil {
					delete(excluded, acc.ID)
				} else {
					gp.Disable(acc.ID, lastErr.Error())
				}
			}
			continue
		}
		if resp.StatusCode == 402 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			msg := "Build credits exhausted (spending limit)."
			if s := strings.TrimSpace(string(b)); s != "" {
				msg += " " + truncateStr(s, 200)
			}
			_ = config.SetGrokAccountQuota(acc.ID, "exhausted", msg, 0, 0)
			gp.Reload()
			sendErr(402, "insufficient_quota", msg)
			return
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 400))
			excluded[acc.ID] = true
			gp.RecordError(acc.ID)
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
				// headers already sent â€” cannot retry accounts
				return
			}
		} else {
			result, cErr = collectGrokResponse(resp.Body)
			resp.Body.Close()
			if cErr != nil {
				lastErr = cErr
				excluded[acc.ID] = true
				gp.RecordError(acc.ID)
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
		logEndpoint := "openai-grok"
		if format == "claude" {
			logEndpoint = "claude-grok"
		}
		if silent {
			logEndpoint += "-silent"
		}
		h.recordSuccessLog(logEndpoint, clientModel, acc.ID, result.InTok+result.OutTok, credits, time.Since(reqStart).Milliseconds())
		logger.Infof("[Grok] done display=%s outTok=%d textLen=%d incomplete=%v stream=%v ms=%d",
			responseModel, result.OutTok, len([]rune(result.Text)), result.Incomplete, stream, time.Since(reqStart).Milliseconds())

		if stream {
			// live path already wrote SSE to completion
			return
		}
		if format == "claude" {
			if len(result.ToolCalls) > 0 && stopReason == "end_turn" {
				stopReason = "tool_use"
			}
			writeClaudeJSONWithTools(w, responseModel, result.Text, result.InTok, result.OutTok, stopReason, result.ToolCalls)
		} else {
			if len(result.ToolCalls) > 0 && finish == "stop" {
				finish = "tool_calls"
			}
			writeOpenAIJSONWithTools(w, responseModel, result.Text, result.InTok, result.OutTok, finish, result.ToolCalls)
		}
		return
	}

	msg := "All Grok accounts failed"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	sendErr(502, "api_error", msg)
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
	oai.Model = grokSilentUpstream
	oai.MaxTokens = 0
	logger.Infof("[GrokSilent] claude %s -> %s", requestedModel, grokSilentUpstream)
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
	proxyReq.Model = grokSilentUpstream
	proxyReq.MaxTokens = 0
	logger.Infof("[GrokSilent] openai %s -> %s", requestedModel, grokSilentUpstream)
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
	out := make([]view, 0, len(accs))
	for _, a := range accs {
		out = append(out, view{
			ID: a.ID, Email: a.Email, Nickname: a.Nickname, DisplayName: a.DisplayName,
			Enabled: a.Enabled, ExpiresAt: a.ExpiresAt, AuthMethod: a.AuthMethod,
			RequestCount: a.RequestCount, ErrorCount: a.ErrorCount,
			TotalTokens: a.TotalTokens, TotalCredits: a.TotalCredits,
			BanStatus: a.BanStatus, BanReason: a.BanReason,
			HasRefresh: a.RefreshToken != "",
			MachineId: a.MachineId, ProxyURL: a.ProxyURL, LastUsed: a.LastUsed, UserID: a.UserID,
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
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id": acc.ID, "email": acc.Email, "nickname": acc.Nickname, "displayName": acc.DisplayName,
		"enabled": acc.Enabled, "expiresAt": acc.ExpiresAt, "authMethod": acc.AuthMethod,
		"requestCount": acc.RequestCount, "errorCount": acc.ErrorCount,
		"totalTokens": acc.TotalTokens, "totalCredits": acc.TotalCredits,
		"banStatus": acc.BanStatus, "banReason": acc.BanReason,
		"hasRefreshToken": acc.RefreshToken != "",
		"machineId": acc.MachineId, "proxyURL": acc.ProxyURL,
		"lastUsed": acc.LastUsed, "userId": acc.UserID, "clientId": acc.ClientID,
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
