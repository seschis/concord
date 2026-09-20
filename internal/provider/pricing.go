package provider

import "strings"

// pricePer1M is (inputUSD, outputUSD) per 1,000,000 tokens.
type pricePer1M struct{ in, out float64 }

// claudePricing mirrors the ANTHROPIC_PRICING table in the Python tool. Matched
// by longest key prefix, so keep specific keys ahead of shorter ones.
var claudePricing = map[string]pricePer1M{
	"claude-opus-5":     {5.0, 25.0},
	"claude-sonnet-5":   {3.0, 15.0},
	"claude-opus-4-7":   {5.0, 25.0},
	"claude-sonnet-4-6": {3.0, 15.0},
	"claude-haiku-4-5":  {1.0, 5.0},
}

// claudeBedrockPricing approximates on-demand Bedrock pricing for the Claude
// family. Matched by longest substring since Bedrock IDs carry a region prefix
// and version suffix (e.g. "us.anthropic.claude-sonnet-4-5-20250929-v1:0"), so
// prefix matching would never hit. Prices are region-independent estimates;
// confirm against the AWS Bedrock pricing page before relying on the figures.
var claudeBedrockPricing = map[string]pricePer1M{
	"claude-opus-4":     {15.0, 75.0},
	"claude-sonnet-4":   {3.0, 15.0},
	"claude-3-7-sonnet": {3.0, 15.0},
	"claude-3-5-sonnet": {3.0, 15.0},
	"claude-3-5-haiku":  {0.80, 4.0},
	"claude-3-haiku":    {0.25, 1.25},
}

// geminiPricing mirrors GEMINI_PRICING in the Python tool.
var geminiPricing = map[string]pricePer1M{
	"gemini-2.5-pro":   {1.25, 10.0},
	"gemini-2.5-flash": {0.30, 2.50},
	"gemini-2.0-flash": {0.075, 0.30},
	"gemini-1.5-pro":   {1.25, 5.0},
	"gemini-1.5-flash": {0.075, 0.30},
}

// openaiPricing mirrors CODEX_PRICING in the Python tool (the table keys are
// real model ids and stay put through the codex→openai rename).
var openaiPricing = map[string]pricePer1M{
	"gpt-5.6-sol":         {4.0, 20.0},
	"gpt-5.6":             {4.0, 20.0},
	"gpt-5.5":             {4.0, 20.0},
	"openai.gpt-oss-120b": {3.0, 12.0},
	"openai.gpt-oss-20b":  {0.5, 2.0},
}

// azurePricing mirrors AZURE_PRICING in the Python tool.
var azurePricing = map[string]pricePer1M{
	"gpt-5.5":      {4.0, 20.0},
	"gpt-4o":       {2.50, 10.0},
	"gpt-4o-mini":  {0.15, 0.60},
	"gpt-4.1":      {2.00, 8.0},
	"gpt-4.1-mini": {0.40, 1.60},
	"o3":           {10.0, 40.0},
	"o4-mini":      {1.10, 4.40},
}

// thinkingSupport is how a Claude model exposes extended thinking, which decides
// how the adapter translates the effort knob (see applyReasoning).
type thinkingSupport int

const (
	// thinkingNone: no extended thinking at all (Claude 3.5 Sonnet/Haiku, Claude 3
	// Opus/Haiku) OR an unknown/unlisted ID. Both omit the thinking parameter, the
	// only 400-safe choice: a no-thinking model rejects budget_tokens, and an
	// unknown model might be a newer current model that rejects budget_tokens AND
	// temperature. Omitting thinking (and temperature) can never 400.
	thinkingNone thinkingSupport = iota
	// thinkingBudget: extended thinking via the legacy
	// thinking:{type:"enabled",budget_tokens:N} API, temperature accepted. These
	// models predate adaptive thinking.
	thinkingBudget
	// thinkingAdaptive: adaptive thinking + output_config.effort; budget_tokens and
	// temperature are both rejected.
	thinkingAdaptive
)

// adaptiveThinkingModels support (and for the newest, require) adaptive thinking.
// budgetThinkingModels support extended thinking only through budget_tokens.
// Everything else — including Claude 3.5/3 families with no extended thinking and
// any unknown ID — falls through to thinkingNone. Matched by substring so Bedrock
// IDs (us.anthropic.claude-sonnet-4-5-...v1:0) classify correctly; the two lists
// are disjoint under substring matching.
var (
	adaptiveThinkingModels = []string{
		"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5",
		"claude-sonnet-4-6", "claude-sonnet-5",
		"claude-fable-5", "claude-mythos-5",
	}
	budgetThinkingModels = []string{
		"claude-opus-4-5", "claude-opus-4-1", "claude-opus-4-0",
		"claude-sonnet-4-5", "claude-sonnet-4-0",
		"claude-haiku-4-5",
		"claude-3-7-sonnet",
	}
)

// thinkingSupportFor classifies model into one of the three thinking states.
// Unknown IDs return thinkingNone.
func thinkingSupportFor(model string) thinkingSupport {
	if containsAny(model, adaptiveThinkingModels) {
		return thinkingAdaptive
	}
	if containsAny(model, budgetThinkingModels) {
		return thinkingBudget
	}
	return thinkingNone
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Anthropic prompt-cache multipliers on the base input rate: a cache read costs
// 0.1x input, and a cache write 1.25x input for the 5-minute TTL the adapter
// uses (see cacheTTL in cache.go). Keep cacheWriteMult in sync with that
// TTL: it would be 2x for a 1-hour TTL. Only the Claude paths report cache
// tokens; every other provider passes 0 for both, so these terms vanish.
const (
	cacheWriteMult = 1.25
	cacheReadMult  = 0.10
)

// priceMatch finds the pricing entry for model. When contains is true it matches
// the longest table key contained in model (Bedrock IDs carry a region prefix
// and version suffix); otherwise it matches by longest prefix.
func priceMatch(table map[string]pricePer1M, model string, contains bool) (pricePer1M, bool) {
	best := ""
	for k := range table {
		hit := strings.HasPrefix(model, k)
		if contains {
			hit = strings.Contains(model, k)
		}
		if hit && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return pricePer1M{}, false
	}
	return table[best], true
}

// priced applies a rate to the four token buckets. Cache write/read are priced
// as multiples of the model's own input rate.
func priced(p pricePer1M, inTok, outTok, cacheWrite, cacheRead int) float64 {
	return float64(inTok)/1e6*p.in +
		float64(outTok)/1e6*p.out +
		float64(cacheWrite)/1e6*p.in*cacheWriteMult +
		float64(cacheRead)/1e6*p.in*cacheReadMult
}

// costFrom prices a call by longest-prefix match, including prompt-cache tokens.
func costFrom(table map[string]pricePer1M, model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
	if p, ok := priceMatch(table, model, false); ok {
		return priced(p, inTok, outTok, cacheWrite, cacheRead)
	}
	return 0
}

// costContains prices a call by longest-substring match, for IDs (like
// Bedrock's) that carry region prefixes and version suffixes around the family
// name.
func costContains(table map[string]pricePer1M, model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
	if p, ok := priceMatch(table, model, true); ok {
		return priced(p, inTok, outTok, cacheWrite, cacheRead)
	}
	return 0
}

// costClaude returns USD for a Claude call. Unknown models cost 0 (reported as
// such rather than guessed).
func costClaude(model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
	return costFrom(claudePricing, model, inTok, outTok, cacheWrite, cacheRead)
}

// costClaudeBedrock returns USD for a Claude-on-Bedrock call. Unknown models
// cost 0 (reported as such rather than guessed).
func costClaudeBedrock(model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
	return costContains(claudeBedrockPricing, model, inTok, outTok, cacheWrite, cacheRead)
}

func costGemini(model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
	return costFrom(geminiPricing, model, inTok, outTok, cacheWrite, cacheRead)
}

func costOpenAI(model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
	return costFrom(openaiPricing, model, inTok, outTok, cacheWrite, cacheRead)
}

func costAzure(model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
	return costFrom(azurePricing, model, inTok, outTok, cacheWrite, cacheRead)
}
