package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/openai"
)

// Protocol names the wire protocol a ModelSpec targets.
type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
	ProtocolGemini    Protocol = "gemini"
	ProtocolAzure     Protocol = "azure"
)

// defaultContextWindow is the context window every preset model claims: the
// presets-only run stays a no-op under the window clamp (a half-window of
// 64000 is far above the default 16000 output budget).
const defaultContextWindow = 128000

// placeholderToken stands in for a real API key when a spec points at an
// explicit local endpoint with no key resolvable anywhere. The openai client
// requires a token at construction, and a local server without auth never
// checks the value, so a placeholder keeps keyless local endpoints usable.
const placeholderToken = "concord-local-no-auth"

// ModelSpec describes one model by data: which protocol to speak, where, with
// which credentials, and what the model costs. Every provider — preset or
// custom — is built from a ModelSpec through NewFromSpec; nothing is
// hardcoded per vendor anymore.
//
// APIKey is a literal key, an "env:NAME" reference to an environment
// variable, or empty to use the protocol's default credential environment.
// PriceIn/PriceOut are per-1M-token USD prices; when both are set they win
// over the protocol's built-in pricing table.
type ModelSpec struct {
	Name          string
	Protocol      Protocol
	Endpoint      string
	APIKey        string
	Model         string
	ContextWindow int
	PriceIn       *float64
	PriceOut      *float64
	Bedrock       bool   // anthropic protocol only
	Region        string // anthropic protocol only; the Bedrock region
	APIVersion    string // azure protocol only
}

// The four built-in presets: the spec values the legacy hardcoded providers
// used to build, with today's default model ids and the 128000 default
// context window. A preset run through NewFromSpec behaves exactly like the
// constructor it replaces (TestNewFromSpecPresets).
var (
	PresetClaude = ModelSpec{
		Name:          "claude",
		Protocol:      ProtocolAnthropic,
		Model:         "claude-opus-5",
		ContextWindow: defaultContextWindow,
	}
	PresetGemini = ModelSpec{
		Name:          "gemini",
		Protocol:      ProtocolGemini,
		Model:         "gemini-2.5-flash",
		ContextWindow: defaultContextWindow,
	}
	PresetOpenAI = ModelSpec{
		Name:          "openai",
		Protocol:      ProtocolOpenAI,
		Model:         "gpt-5.6-sol",
		ContextWindow: defaultContextWindow,
	}
	PresetAzure = ModelSpec{
		Name:          "azure",
		Protocol:      ProtocolAzure,
		Model:         "gpt-5.5",
		APIVersion:    "2024-12-01-preview",
		ContextWindow: defaultContextWindow,
	}
)

// explicitKey resolves a spec's APIKey literal or env: reference, returning
// "" when unset.
func explicitKey(apiKey string) string {
	if name, ok := strings.CutPrefix(apiKey, "env:"); ok {
		return os.Getenv(name)
	}
	return apiKey
}

// resolveKey returns the spec's API key: the literal or a set env: reference
// when it yields a value, otherwise the protocol's default credential
// environment variable. Anthropic returns "" on purpose: its SDK reads
// ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN / ANTHROPIC_PROFILE lazily, and
// lifting one into option.WithAPIKey would downgrade a bearer AUTH_TOKEN to
// an x-api-key header.
func resolveKey(spec ModelSpec) string {
	if k := explicitKey(spec.APIKey); k != "" {
		return k
	}
	switch spec.Protocol {
	case ProtocolOpenAI:
		return os.Getenv("OPENAI_API_KEY")
	case ProtocolAzure:
		return os.Getenv("AZURE_OPENAI_API_KEY")
	case ProtocolGemini:
		return os.Getenv("GOOGLE_API_KEY")
	}
	return ""
}

// anthropicEnvKey reports whether the anthropic SDK's environment credential
// chain has anything to read, in the SDK's order: ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN, ANTHROPIC_PROFILE. The value itself is never lifted
// out of the environment (see resolveKey).
func anthropicEnvKey() string {
	for _, env := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_PROFILE"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return ""
}

// Resolvable reports whether the spec's credentials can resolve for its
// protocol, and a skip reason when they cannot (KTD5). A later unit calls it
// per spec to turn unresolvable models into skip lines instead of per-finding
// error stubs. An explicit endpoint with no key anywhere is resolvable: the
// factory constructs it with a placeholder token so local servers that take
// no auth still work.
func (spec ModelSpec) Resolvable() (bool, string) {
	switch spec.Protocol {
	case ProtocolOpenAI:
		if resolveKey(spec) != "" {
			return true, ""
		}
		if spec.Endpoint != "" {
			return true, ""
		}
		return false, "no api key set and no OPENAI_API_KEY env var; set one or use an explicit endpoint"

	case ProtocolAzure:
		if resolveKey(spec) != "" {
			return true, ""
		}
		return false, "no api key set and no AZURE_OPENAI_API_KEY env var"

	case ProtocolGemini:
		if resolveKey(spec) != "" {
			return true, ""
		}
		return false, "no api key set and no GOOGLE_API_KEY env var"

	case ProtocolAnthropic:
		if spec.Bedrock {
			if spec.Region != "" || os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_DEFAULT_REGION") != "" {
				return true, ""
			}
			return false, "bedrock requires a region (spec Region, AWS_REGION, or AWS_DEFAULT_REGION)"
		}
		if explicitKey(spec.APIKey) != "" || anthropicEnvKey() != "" {
			return true, ""
		}
		if spec.Endpoint != "" {
			return true, ""
		}
		return false, "no api key set and no ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, or ANTHROPIC_PROFILE env var"
	}
	return false, "unknown protocol " + string(spec.Protocol)
}

// NewFromSpec builds an LLMProvider for spec. This is the single construction
// path for every provider — presets and custom servers alike. Construction
// performs no network I/O: clients dial lazily and credentials resolve
// lazily, so a missing key surfaces at request time, not here — except where
// the underlying client requires a token at construction (the openai
// client), in which case an explicit endpoint falls back to a placeholder
// token and the keyless-no-endpoint case errors.
func NewFromSpec(spec ModelSpec, maxTokens int) (*LLMProvider, error) {
	if spec.ContextWindow <= 0 {
		spec.ContextWindow = defaultContextWindow
	}
	name := spec.Name
	if name == "" {
		name = string(spec.Protocol)
	}

	var (
		llm      llms.Model
		table    map[string]pricePer1M
		contains bool
	)

	switch spec.Protocol {
	case ProtocolOpenAI:
		opts := []openai.Option{openai.WithModel(spec.Model)}
		if spec.Endpoint != "" {
			opts = append(opts, openai.WithBaseURL(spec.Endpoint))
		}
		if token := resolveKey(spec); token != "" {
			opts = append(opts, openai.WithToken(token))
		} else if spec.Endpoint != "" {
			opts = append(opts, openai.WithToken(placeholderToken))
		}
		model, err := openai.New(opts...)
		if err != nil {
			return nil, fmt.Errorf("init openai: %w", err)
		}
		llm, table, contains = model, openaiPricing, false

	case ProtocolAzure:
		key := resolveKey(spec)
		endpoint := spec.Endpoint
		if endpoint == "" {
			endpoint = os.Getenv("AZURE_OPENAI_ENDPOINT")
		}
		if key == "" || endpoint == "" {
			return nil, errors.New("azure requires an api key and endpoint")
		}
		opts := []openai.Option{
			openai.WithToken(key),
			openai.WithBaseURL(endpoint),
			openai.WithAPIType(openai.APITypeAzure),
			openai.WithModel(spec.Model),
		}
		if spec.APIVersion != "" {
			opts = append(opts, openai.WithAPIVersion(spec.APIVersion))
		}
		model, err := openai.New(opts...)
		if err != nil {
			return nil, fmt.Errorf("init azure: %w", err)
		}
		llm, table, contains = model, azurePricing, false

	case ProtocolGemini:
		opts := []googleai.Option{googleai.WithDefaultModel(spec.Model)}
		if key := resolveKey(spec); key != "" {
			opts = append(opts, googleai.WithAPIKey(key))
		}
		model, err := googleai.New(context.Background(), opts...)
		if err != nil {
			return nil, fmt.Errorf("init gemini: %w", err)
		}
		llm, table, contains = model, geminiPricing, false

	case ProtocolAnthropic:
		if spec.Bedrock {
			var cfgOpts []func(*awsconfig.LoadOptions) error
			if spec.Region != "" {
				cfgOpts = append(cfgOpts, awsconfig.WithRegion(spec.Region))
			}
			client := anthropicsdk.NewClient(bedrock.WithLoadDefaultConfig(context.Background(), cfgOpts...))
			llm = &anthropicNativeModel{client: client, model: spec.Model, cacheTTL: cacheTTL}
			table, contains = claudeBedrockPricing, true
			break
		}
		var opts []option.RequestOption
		if key := explicitKey(spec.APIKey); key != "" {
			opts = append(opts, option.WithAPIKey(key))
		} else if spec.Endpoint != "" && anthropicEnvKey() == "" {
			// A local endpoint with no credential anywhere: a placeholder
			// token so the client can be constructed; local servers without
			// auth never check it.
			opts = append(opts, option.WithAPIKey(placeholderToken))
		}
		if spec.Endpoint != "" {
			opts = append(opts, option.WithBaseURL(spec.Endpoint))
		}
		client := anthropicsdk.NewClient(opts...)
		llm = &anthropicNativeModel{client: client, model: spec.Model, cacheTTL: cacheTTL}
		table, contains = claudePricing, false

	default:
		return nil, fmt.Errorf("unknown protocol %q", spec.Protocol)
	}

	// KTD9: priced is true when an explicit spec price is set or the model id
	// matches the protocol's built-in table (anthropic consults the Bedrock
	// table when bedrock). An explicit price (both fields) wins over the
	// table in the cost function.
	explicitPrice := spec.PriceIn != nil && spec.PriceOut != nil
	isPriced := explicitPrice
	if !isPriced {
		_, isPriced = priceMatch(table, spec.Model, contains)
	}
	var cost costFunc
	switch {
	case explicitPrice:
		p := pricePer1M{in: *spec.PriceIn, out: *spec.PriceOut}
		cost = func(model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
			return priced(p, inTok, outTok, cacheWrite, cacheRead)
		}
	case contains:
		cost = func(model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
			return costContains(table, model, inTok, outTok, cacheWrite, cacheRead)
		}
	default:
		cost = func(model string, inTok, outTok, cacheWrite, cacheRead int) float64 {
			return costFrom(table, model, inTok, outTok, cacheWrite, cacheRead)
		}
	}

	return &LLMProvider{
		llm: llm, name: name, model: spec.Model, maxTokens: maxTokens,
		cost: cost, contextWindow: spec.ContextWindow, priced: isPriced,
	}, nil
}
