package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

// TestNewFromSpecPresets is the preset-equivalence gate: each preset built
// from its spec value must be observably identical to the legacy constructor
// it replaces — same Name, Model, and cost behavior (the openai name is the
// one sanctioned rename) — and carry the 128000 default context window.
func TestNewFromSpecPresets(t *testing.T) {
	clearCredentialEnvs(t)
	// The openai and gemini presets only construct when a key resolves (the
	// legacy constructors error identically without one), so give each its
	// protocol default env key; claude constructs keyless (lazy SDK sentinel)
	// and azure takes its key/endpoint env vars, exactly as cmd resolves them.
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("GOOGLE_API_KEY", "gemini-key")
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://res.openai.azure.com")

	cases := []struct {
		spec   ModelSpec
		legacy func() (*LLMProvider, error)
		name   string
		model  string
		inCost float64
	}{
		{
			spec:   PresetClaude,
			legacy: func() (*LLMProvider, error) { return NewClaude("", "claude-opus-5", 100) },
			name:   "claude",
			model:  "claude-opus-5",
			inCost: 5.0, // claudePricing "claude-opus-5": input $5/M
		},
		{
			spec:   PresetGemini,
			legacy: func() (*LLMProvider, error) { return NewGemini(context.Background(), "", "gemini-2.5-flash", 100) },
			name:   "gemini",
			model:  "gemini-2.5-flash",
			inCost: 0.30, // geminiPricing "gemini-2.5-flash": input $0.30/M
		},
		{
			spec:   PresetOpenAI,
			legacy: func() (*LLMProvider, error) { return NewCodex("", "gpt-5.6-sol", 100) },
			name:   "openai",
			model:  "gpt-5.6-sol",
			inCost: 4.0, // openaiPricing "gpt-5.6-sol": input $4/M
		},
		{
			spec: PresetAzure,
			legacy: func() (*LLMProvider, error) {
				return NewAzure("azure-key", "https://res.openai.azure.com", "2024-12-01-preview", "gpt-5.5", 100)
			},
			name:   "azure",
			model:  "gpt-5.5",
			inCost: 4.0, // azurePricing "gpt-5.5": input $4/M
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewFromSpec(c.spec, 100)
			if err != nil {
				t.Fatalf("NewFromSpec(%q): %v", c.spec.Protocol, err)
			}
			leg, err := c.legacy()
			if err != nil {
				t.Fatalf("legacy constructor: %v", err)
			}

			if got.Name() != c.name {
				t.Errorf("Name() = %q, want %q", got.Name(), c.name)
			}
			if got.Model() != c.model {
				t.Errorf("Model() = %q, want %q", got.Model(), c.model)
			}
			if got.ContextWindow() != 128000 {
				t.Errorf("ContextWindow() = %d, want 128000", got.ContextWindow())
			}
			if !got.Priced() {
				t.Error("Priced() = false, want true (preset model ids match the built-in tables)")
			}

			if leg.Name() != got.Name() || leg.Model() != got.Model() {
				t.Errorf("factory %q/%q diverges from legacy %q/%q", got.Name(), got.Model(), leg.Name(), leg.Model())
			}

			// Cost behavior must be identical to the legacy constructor,
			// including the cache-token buckets.
			tokens := [][4]int{
				{1_000_000, 0, 0, 0},
				{0, 1_000_000, 0, 0},
				{1_000_000, 1_000_000, 0, 0},
				{1_000_000, 1_000_000, 1_000_000, 1_000_000},
			}
			for _, tk := range tokens {
				g := got.cost(got.model, tk[0], tk[1], tk[2], tk[3])
				l := leg.cost(leg.model, tk[0], tk[1], tk[2], tk[3])
				if g != l {
					t.Errorf("cost(%v) = %v, want legacy %v", tk, g, l)
				}
			}
			if got := got.cost(got.model, 1_000_000, 0, 0, 0); got != c.inCost {
				t.Errorf("1M input cost = %v, want %v", got, c.inCost)
			}
		})
	}
}

// TestNewFromSpecCustomOpenAILocal is the vLLM-shaped path: an explicit
// endpoint with no key anywhere must construct via a placeholder token,
// without dialing anything, and be unpriced.
func TestNewFromSpecCustomOpenAILocal(t *testing.T) {
	clearCredentialEnvs(t)
	spec := ModelSpec{
		Name:          "qwen",
		Protocol:      ProtocolOpenAI,
		Endpoint:      "http://127.0.0.1:8000/v1",
		Model:         "Qwen3.8-27B",
		ContextWindow: 262144,
	}
	p, err := NewFromSpec(spec, 100)
	if err != nil {
		t.Fatalf("NewFromSpec: %v (a keyless local endpoint should construct via a placeholder token)", err)
	}
	if p.Name() != "qwen" {
		t.Errorf("Name() = %q, want %q", p.Name(), "qwen")
	}
	if p.Model() != "Qwen3.8-27B" {
		t.Errorf("Model() = %q, want %q", p.Model(), "Qwen3.8-27B")
	}
	if p.ContextWindow() != 262144 {
		t.Errorf("ContextWindow() = %d, want 262144", p.ContextWindow())
	}
	if p.Priced() {
		t.Error("Priced() = true, want false (no explicit price, no table match)")
	}
}

// TestNewFromSpecOpenAIMissingToken is the resolvable=false path: no endpoint,
// no key, no env key. A later unit surfaces this as a skip reason; the
// factory itself must error.
func TestNewFromSpecOpenAIMissingToken(t *testing.T) {
	clearCredentialEnvs(t)
	spec := ModelSpec{
		Name:          "openai",
		Protocol:      ProtocolOpenAI,
		Model:         "gpt-5.6-sol",
		ContextWindow: 128000,
	}
	if _, err := NewFromSpec(spec, 100); err == nil {
		t.Fatal("NewFromSpec succeeded, want an error (no token resolvable and no explicit endpoint)")
	}
}

// TestNewFromSpecCustomAnthropicEndpoint: an explicit endpoint with no key
// anywhere constructs via placeholder auth.
func TestNewFromSpecCustomAnthropicEndpoint(t *testing.T) {
	clearCredentialEnvs(t)
	spec := ModelSpec{
		Name:          "local-anthropic",
		Protocol:      ProtocolAnthropic,
		Endpoint:      "http://127.0.0.1:9999",
		Model:         "some-local-model",
		ContextWindow: 8192,
	}
	p, err := NewFromSpec(spec, 100)
	if err != nil {
		t.Fatalf("NewFromSpec: %v (a keyless local endpoint should construct via placeholder auth)", err)
	}
	if p.Name() != "local-anthropic" || p.Model() != "some-local-model" {
		t.Errorf("got %q/%q, want local-anthropic/some-local-model", p.Name(), p.Model())
	}
	if p.ContextWindow() != 8192 {
		t.Errorf("ContextWindow() = %d, want 8192", p.ContextWindow())
	}
}

// TestNewFromSpecBedrock: the bedrock flag routes to the Bedrock construction
// with lazy AWS credentials — no credentials present, no error at
// construction, and the Bedrock pricing table (substring match) applies.
func TestNewFromSpecBedrock(t *testing.T) {
	clearCredentialEnvs(t)
	spec := ModelSpec{
		Name:          "claude",
		Protocol:      ProtocolAnthropic,
		Bedrock:       true,
		Region:        "us-east-1",
		Model:         "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		ContextWindow: 128000,
	}
	p, err := NewFromSpec(spec, 100)
	if err != nil {
		t.Fatalf("NewFromSpec: %v (Bedrock credentials resolve lazily; construction must not fail)", err)
	}
	if p.Name() != "claude" {
		t.Errorf("Name() = %q, want claude", p.Name())
	}
	if p.Model() != spec.Model {
		t.Errorf("Model() = %q, want %q", p.Model(), spec.Model)
	}
	if !p.Priced() {
		t.Error("Priced() = false, want true (the Bedrock id matches the Bedrock table)")
	}
	// claudeBedrockPricing "claude-sonnet-4": output $15/M.
	if got := p.cost(p.model, 0, 1_000_000, 0, 0); got != 15.0 {
		t.Errorf("1M output cost = %v, want 15.0 (bedrock table substring match)", got)
	}
}

// TestNewFromSpecPricing covers the KTD9 signal and the R14 precedence: an
// explicit spec price (even 0.0/0.0) wins over the table, a table match is
// priced, and neither is unpriced at $0.
func TestNewFromSpecPricing(t *testing.T) {
	clearCredentialEnvs(t)

	zero := 0.0
	spec := ModelSpec{
		Name: "free", Protocol: ProtocolOpenAI, Endpoint: "http://127.0.0.1:8000/v1",
		Model: "Qwen3.8-27B", ContextWindow: 262144, PriceIn: &zero, PriceOut: &zero,
	}
	p, err := NewFromSpec(spec, 100)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if !p.Priced() {
		t.Error("Priced() = false, want true (explicit 0.0/0.0 price is an explicit price)")
	}
	if got := p.cost(p.model, 1_000_000, 1_000_000, 1_000_000, 1_000_000); got != 0 {
		t.Errorf("cost = %v, want 0 (explicit zero price)", got)
	}

	spec = ModelSpec{Name: "openai", Protocol: ProtocolOpenAI, APIKey: "k", Model: "gpt-5.6", ContextWindow: 128000}
	p, err = NewFromSpec(spec, 100)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if !p.Priced() {
		t.Error("Priced() = false, want true (gpt-5.6 matches the openai table)")
	}
	if got := p.cost(p.model, 1_000_000, 0, 0, 0); got != 4.0 {
		t.Errorf("1M input cost = %v, want 4.0 (table price)", got)
	}

	spec = ModelSpec{
		Name: "qwen", Protocol: ProtocolOpenAI, Endpoint: "http://127.0.0.1:8000/v1",
		Model: "Qwen3.8-27B", ContextWindow: 262144,
	}
	p, err = NewFromSpec(spec, 100)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if p.Priced() {
		t.Error("Priced() = true, want false (no explicit price, no table match)")
	}
	if got := p.cost(p.model, 1_000_000, 1_000_000, 0, 0); got != 0 {
		t.Errorf("cost = %v, want 0 (unpriced models cost 0)", got)
	}

	one, two := 1.0, 2.0
	spec = ModelSpec{
		Name: "openai", Protocol: ProtocolOpenAI, APIKey: "k", Model: "gpt-5.6",
		ContextWindow: 128000, PriceIn: &one, PriceOut: &two,
	}
	p, err = NewFromSpec(spec, 100)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if !p.Priced() {
		t.Error("Priced() = false, want true")
	}
	// The explicit price must beat the table (gpt-5.6 would cost $4/$20).
	if got := p.cost(p.model, 1_000_000, 1_000_000, 0, 0); got != 3.0 {
		t.Errorf("cost = %v, want 3.0 (explicit $1/$2 per 1M must win over the table)", got)
	}
}

// TestOpenAIRequestBodyPin is the KTD8 pin: the pinned langchaingo version's
// openai client must send max_completion_tokens and must NOT send a
// reasoning_effort key, even when a thinking/effort option is requested. The
// factory-built client (endpoint = the httptest server) is driven with one
// chat completion so a future dependency bump that reintroduces the
// parameter fails loudly.
func TestOpenAIRequestBodyPin(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = b
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"1","object":"chat.completion","created":1,"model":"gpt-5.6-sol",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
		}`)
	}))
	defer srv.Close()

	clearCredentialEnvs(t)
	spec := ModelSpec{
		Name: "openai", Protocol: ProtocolOpenAI, Endpoint: srv.URL,
		Model: "gpt-5.6-sol", ContextWindow: 128000, APIKey: "k",
	}
	p, err := NewFromSpec(spec, 100)
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}

	// One chat completion through the openai-protocol client the factory
	// built. thinkingOpts is included on purpose: it is the option a future
	// langchaingo bump would map to reasoning_effort.
	opts := append([]llms.CallOption{llms.WithMaxTokens(100)}, thinkingOpts("high")...)
	_, err = p.llm.GenerateContent(context.Background(),
		[]llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "hi")},
		opts...)
	if err != nil {
		t.Fatalf("chat completion: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("the test server received no request")
	}
	if !strings.Contains(string(body), `"max_completion_tokens":100`) {
		t.Errorf("request body should carry max_completion_tokens:100, got: %s", body)
	}
	if strings.Contains(string(body), `"reasoning_effort"`) {
		t.Errorf("request body must not carry a reasoning_effort key, got: %s", body)
	}
}
