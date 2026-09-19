package provider

import (
	"context"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
)

// cacheTTL is the prompt-cache breakpoint lifetime the Claude adapter requests.
// 5m covers a single finding's back-to-back tool-loop turns while keeping the
// cheaper 1.25x write premium; below the model's minimum cacheable prefix the
// API silently no-ops, so this is safe on every model. If this changes to 1h,
// bump cacheWriteMult in pricing.go to 2.0 (TestCacheWriteMultMatchesTTL guards
// the pairing).
const cacheTTL = anthropicsdk.CacheControlEphemeralTTLTTL5m

// NewClaude builds the Anthropic-backed provider on the official anthropic-sdk-go
// (see anthropicNativeModel) for native prompt caching. apiKey falls back to
// ANTHROPIC_API_KEY when empty. Compatibility wrapper around the claude preset
// spec that a later unit removes once the config layer drives construction.
func NewClaude(apiKey, model string, maxTokens int) (*LLMProvider, error) {
	spec := PresetClaude
	spec.Model = model
	spec.APIKey = apiKey
	return NewFromSpec(spec, maxTokens)
}

// NewClaudeBedrock builds the Claude provider backed by Amazon Bedrock, on the
// official anthropic-sdk-go's bedrock helper (bedrock.WithLoadDefaultConfig).
// Auth flows through the AWS credential chain the helper loads: if the Bedrock
// API key env var AWS_BEARER_TOKEN_BEDROCK is set the SDK uses bearer-token auth
// and ignores SigV4; otherwise it falls back to the standard credential chain
// (AWS_PROFILE, SSO, env, or IAM role). Either way no key is passed in here.
// Region falls back to the SDK's own resolution (AWS_REGION / AWS_DEFAULT_REGION
// / profile) when empty; a region is still required even with a bearer token,
// for endpoint resolution. modelID must be a Bedrock model or inference-profile
// ID, e.g. "us.anthropic.claude-sonnet-4-5-20250929-v1:0".
//
// Credentials resolve lazily inside the AWS SDK config the SDK's Bedrock helper
// loads, so a missing or invalid token/profile surfaces on the first model call
// (as a per-finding error result), not here. Unlike the old langchaingo Bedrock
// path, this adapter wires prompt caching AND thinking uniformly with the direct
// API: current models get adaptive thinking + effort, legacy models get
// budget_tokens (see anthropicNativeModel.applyReasoning). Compatibility
// wrapper around the claude preset spec (bedrock=true) that a later unit
// removes; ctx is accepted for signature compatibility but the factory loads
// the AWS config on its own context.
func NewClaudeBedrock(ctx context.Context, modelID, region string, maxTokens int) (*LLMProvider, error) {
	spec := PresetClaude
	spec.Model = modelID
	spec.Bedrock = true
	spec.Region = region
	return NewFromSpec(spec, maxTokens)
}

// NewGemini builds the Google AI (Gemini) provider. apiKey falls back to
// GOOGLE_API_KEY when empty. Compatibility wrapper around the gemini preset
// spec that a later unit removes once the config layer drives construction.
func NewGemini(ctx context.Context, apiKey, model string, maxTokens int) (*LLMProvider, error) {
	spec := PresetGemini
	spec.Model = model
	spec.APIKey = apiKey
	return NewFromSpec(spec, maxTokens)
}

// NewCodex builds the direct-OpenAI provider used for Codex/GPT models. apiKey
// falls back to OPENAI_API_KEY when empty. Compatibility wrapper around the
// openai preset spec that a later unit removes once the config layer drives
// construction. The provider's Name() is now "openai" (the codex→openai
// rename); the constructor name survives as a deprecated alias.
func NewCodex(apiKey, model string, maxTokens int) (*LLMProvider, error) {
	spec := PresetOpenAI
	spec.Model = model
	spec.APIKey = apiKey
	return NewFromSpec(spec, maxTokens)
}

// NewAzure builds the Azure OpenAI provider. deployment is the Azure deployment
// name, endpoint the resource base URL. Compatibility wrapper around the azure
// preset spec that a later unit removes once the config layer drives
// construction.
func NewAzure(apiKey, endpoint, apiVersion, deployment string, maxTokens int) (*LLMProvider, error) {
	spec := PresetAzure
	spec.Model = deployment
	spec.APIKey = apiKey
	spec.Endpoint = endpoint
	spec.APIVersion = apiVersion
	return NewFromSpec(spec, maxTokens)
}
