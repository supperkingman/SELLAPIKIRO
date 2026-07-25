package proxy

// Reasoning-effort support.
//
// Kiro IDE lets a user pick how hard a model should think (low through max), but
// neither the OpenAI nor the Anthropic wire format has a field for it. Effort is
// therefore accepted from three places, highest priority first:
//
//  1. a suffix on the model name  — claude-opus-5-max
//  2. a native hint in the body   — OpenAI reasoning_effort, Claude output_config.effort
//  3. the configured default      — GetEffortConfig().DefaultEffort
//
// The suffix wins because it is the only channel available to clients with no
// effort control of their own (Claude Code, Cline), which is also why the model
// list advertises one variant per level.

import (
	"strings"

	"kiro-go/config"
	"kiro-go/logger"
)

// Effort levels, weakest to strongest. Order is meaningful: reduceEffort walks it
// downwards when a request keeps getting cut off mid-stream.
const (
	effortLow    = "low"
	effortMedium = "medium"
	effortHigh   = "high"
	effortXHigh  = "xhigh"
	effortMax    = "max"
)

// effortLadder is the reduction order used by reduceEffort, strongest first.
var effortLadder = []string{effortMax, effortXHigh, effortHigh, effortMedium, effortLow}

// EffortLevels lists valid levels for the admin API and for validation.
func EffortLevels() []string {
	return []string{effortLow, effortMedium, effortHigh, effortXHigh, effortMax}
}

// Wire shapes for attaching effort to a request.
const (
	// effortFormatReasoning emits {"reasoning": {"effort": "..."}}. The minimal
	// shape, and the safest guess for a model whose schema we cannot read.
	effortFormatReasoning = "reasoning"
	// effortFormatOutputConfig emits the thinking + output_config pair.
	effortFormatOutputConfig = "output_config"
	// effortFormatAuto picks a shape per model, falling back to reasoning.
	effortFormatAuto = "auto"
	// effortFormatOff disables effort entirely.
	effortFormatOff = "off"
)

// EffortFormats lists valid formats for the admin API and for validation.
func EffortFormats() []string {
	return []string{effortFormatAuto, effortFormatReasoning, effortFormatOutputConfig, effortFormatOff}
}

// normalizeEffort maps a client-supplied value onto a known level, returning ""
// when it is not recognizable. OpenAI's "minimal" and "none" have no direct
// equivalent, so they collapse to the weakest level we support rather than being
// dropped, which would silently promote the request to the default instead.
func normalizeEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "minimal", "none", effortLow:
		return effortLow
	case effortMedium:
		return effortMedium
	case effortHigh:
		return effortHigh
	case "xhigh", "x-high", "extra-high", "veryhigh", "very-high":
		return effortXHigh
	case effortMax, "maximum", "ultra":
		return effortMax
	default:
		return ""
	}
}

// stripEffortSuffix removes a trailing effort level from a model name and returns
// the bare name plus the level found.
//
// This has to run BEFORE the model name is resolved or classified. Every other
// consumer matches on the name: ParseModelAndThinking compares against aliases and
// version patterns, and disguiseTargetForModel keys off the prefix to decide which
// provider a request should be dressed up as. Leaving "-max" attached makes those
// comparisons miss, so the model would be rejected upstream as an unknown id.
//
// Only an exact trailing "-<level>" counts, so a hypothetical real model ending in
// one of these words is not silently mangled.
func stripEffortSuffix(model string) (string, string) {
	lower := strings.ToLower(strings.TrimSpace(model))

	// Longest first. The spelled-out variants end in a shorter level name, so
	// checking "-high" before "-x-high" would leave a stray "-x" on the model name
	// and report the wrong level.
	type candidate struct {
		suffix string
		level  string
	}
	candidates := []candidate{
		{"-extra-high", effortXHigh},
		{"-very-high", effortXHigh},
		{"-x-high", effortXHigh},
	}
	for _, level := range EffortLevels() {
		candidates = append(candidates, candidate{"-" + level, level})
	}

	for _, c := range candidates {
		if strings.HasSuffix(lower, c.suffix) {
			return model[:len(model)-len(c.suffix)], c.level
		}
	}
	return model, ""
}

// modelSupportsEffort reports whether attaching effort to a model is worth trying.
// Only Claude models are known to accept it; sending it to a Grok or Codex target
// would be forwarded to a provider whose API does not define the field.
func modelSupportsEffort(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	if IsGrokModel(m) || IsCodexModel(m) {
		return false
	}
	return strings.HasPrefix(m, "claude-")
}

// resolveEffort decides the level for a request from the three sources, in
// priority order, and reports whether effort should be attached at all.
//
// suffixLevel comes from stripEffortSuffix, nativeHint from the request body.
func resolveEffort(model, suffixLevel, nativeHint string) (level string, enabled bool) {
	cfg := config.GetEffortConfig()
	if cfg.EffortFormat == effortFormatOff {
		return "", false
	}
	if !modelSupportsEffort(model) {
		return "", false
	}

	if lvl := normalizeEffort(suffixLevel); lvl != "" {
		return lvl, true
	}
	if lvl := normalizeEffort(nativeHint); lvl != "" {
		return lvl, true
	}
	if lvl := normalizeEffort(cfg.DefaultEffort); lvl != "" {
		return lvl, true
	}
	return "", false
}

// effortWireFormat resolves the configured format to a concrete shape.
//
// "auto" would ideally read additionalModelRequestFieldsSchema from
// ListAvailableModels, but this fork's ModelInfo does not carry that field, and
// API-key accounts cannot call that endpoint at all. Both cases therefore fall
// back to the reasoning shape, which is the minimal one and the most widely
// accepted; a wrong guess costs one retry, handled by isEffortRejection400.
func effortWireFormat() string {
	switch config.GetEffortConfig().EffortFormat {
	case effortFormatOutputConfig:
		return effortFormatOutputConfig
	case effortFormatReasoning:
		return effortFormatReasoning
	case effortFormatOff:
		return effortFormatOff
	default:
		return effortFormatReasoning
	}
}

// applyEffortToPayload attaches the effort level to an outgoing Kiro request.
// A no-op when the level is empty, so a request without effort keeps exactly the
// body it had before this feature existed.
func applyEffortToPayload(payload *KiroPayload, level string) {
	if payload == nil || level == "" {
		return
	}

	fields := map[string]interface{}{}
	switch effortWireFormat() {
	case effortFormatOff:
		return
	case effortFormatOutputConfig:
		fields["thinking"] = map[string]interface{}{
			"type":    "adaptive",
			"display": "summarized",
		}
		fields["output_config"] = map[string]interface{}{"effort": level}
	default:
		fields["reasoning"] = map[string]interface{}{"effort": level}
	}

	payload.ConversationState.CurrentMessage.UserInputMessage.AdditionalModelRequestFields = fields
	payload.EffortLevel = level
}

// reduceEffort returns the next level down, or "" when already at the bottom.
func reduceEffort(level string) string {
	for i, l := range effortLadder {
		if l == level && i+1 < len(effortLadder) {
			return effortLadder[i+1]
		}
	}
	return ""
}

// reduceEffortOnPayload lowers a payload's effort one step in place and reports
// whether it changed anything.
//
// Used when a stream is cut off before any content was committed: a higher effort
// means a longer time to first token and a longer generation, which is exactly
// when upstream tends to give up. Retrying the same account list at the same
// effort would most likely be cut the same way, so each retry asks for a little
// less thinking.
func reduceEffortOnPayload(payload *KiroPayload) (from, to string, ok bool) {
	if payload == nil || payload.EffortLevel == "" {
		return "", "", false
	}
	next := reduceEffort(payload.EffortLevel)
	if next == "" {
		return payload.EffortLevel, "", false
	}
	from = payload.EffortLevel
	applyEffortToPayload(payload, next)
	return from, next, true
}

// isEffortRejection400 guesses whether a 400 was caused by the effort fields
// rather than by the request itself, so the caller can retry once without them.
//
// Being wrong here is cheap in one direction only: a needless retry costs one
// request, while failing to retry would break a request that used to work. The
// match is therefore kept to messages that name the fields we added.
func isEffortRejection400(msg string) bool {
	low := strings.ToLower(msg)
	if !strings.Contains(low, "400") && !strings.Contains(low, "validation") &&
		!strings.Contains(low, "invalid") && !strings.Contains(low, "unsupported") &&
		!strings.Contains(low, "unexpected") {
		return false
	}
	return strings.Contains(low, "additionalmodelrequestfields") ||
		strings.Contains(low, "output_config") ||
		strings.Contains(low, "reasoning") ||
		strings.Contains(low, "effort") ||
		strings.Contains(low, "thinking")
}

// stripEffortFromPayload removes the effort fields for the retry-without-effort
// path, leaving the request as it would have been with the feature disabled.
func stripEffortFromPayload(payload *KiroPayload) {
	if payload == nil {
		return
	}
	payload.ConversationState.CurrentMessage.UserInputMessage.AdditionalModelRequestFields = nil
	payload.EffortLevel = ""
}

// logEffortFallback records an effort reduction, so an operator can see that a
// request succeeded only at a lower level.
func logEffortFallback(endpoint, model, from, to string) {
	logger.Infof("[EffortFallback] %s %s aborted pre-commit; reducing effort %s -> %s for retry",
		endpoint, model, from, to)
}
