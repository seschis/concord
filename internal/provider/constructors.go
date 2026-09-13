package provider

import (
	"context"
	"fmt"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/openai"
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
// ANTHROPIC_API_KEY when empty.
func NewClaude(apiKey, model string, maxTokens int) (*LLMProvider, error) {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	client := anthropicsdk.NewClient(opts...)
	m := &anthropicNativeModel{client: client, model: model, cacheTTL: cacheTTL}
	return &LLMProvider{llm: m, name: "claude", model: model, maxTokens: maxTokens, cost: costClaude}, nil
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
// budget_tokens (see anthropicNativeModel.applyReasoning).
func NewClaudeBedrock(ctx context.Context, modelID, region string, maxTokens int) (*LLMProvider, error) {
	var cfgOpts []func(*awsconfig.LoadOptions) error
	if region != "" {
		cfgOpts = append(cfgOpts, awsconfig.WithRegion(region))
	}
	client := anthropicsdk.NewClient(bedrock.WithLoadDefaultConfig(ctx, cfgOpts...))
	m := &anthropicNativeModel{client: client, model: modelID, cacheTTL: cacheTTL}
	return &LLMProvider{llm: m, name: "claude", model: modelID, maxTokens: maxTokens, cost: costClaudeBedrock}, nil
}

// NewGemini builds the Google AI (Gemini) provider. apiKey falls back to
// GOOGLE_API_KEY when empty.
func NewGemini(ctx context.Context, apiKey, model string, maxTokens int) (*LLMProvider, error) {
	opts := []googleai.Option{googleai.WithDefaultModel(model)}
	if apiKey != "" {
		opts = append(opts, googleai.WithAPIKey(apiKey))
	}
	llm, err := googleai.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("init gemini: %w", err)
	}
	return &LLMProvider{llm: llm, name: "gemini", model: model, maxTokens: maxTokens, cost: costGemini}, nil
}

// NewCodex builds the direct-OpenAI provider used for Codex/GPT models. apiKey
// falls back to OPENAI_API_KEY when empty.
func NewCodex(apiKey, model string, maxTokens int) (*LLMProvider, error) {
	opts := []openai.Option{openai.WithModel(model)}
	if apiKey != "" {
		opts = append(opts, openai.WithToken(apiKey))
	}
	llm, err := openai.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("init codex: %w", err)
	}
	return &LLMProvider{llm: llm, name: "codex", model: model, maxTokens: maxTokens, cost: costCodex}, nil
}

// NewAzure builds the Azure OpenAI provider. deployment is the Azure deployment
// name, endpoint the resource base URL.
func NewAzure(apiKey, endpoint, apiVersion, deployment string, maxTokens int) (*LLMProvider, error) {
	if apiKey == "" || endpoint == "" {
		return nil, fmt.Errorf("azure requires an api key and endpoint")
	}
	llm, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(endpoint),
		openai.WithAPIVersion(apiVersion),
		openai.WithAPIType(openai.APITypeAzure),
		openai.WithModel(deployment),
	)
	if err != nil {
		return nil, fmt.Errorf("init azure: %w", err)
	}
	return &LLMProvider{llm: llm, name: "azure", model: deployment, maxTokens: maxTokens, cost: costAzure}, nil
}
