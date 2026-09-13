package provider

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/tmc/langchaingo/llms"
)

func lowEffortOpts() llms.CallOptions {
	var co llms.CallOptions
	for _, o := range thinkingOpts("high") {
		o(&co)
	}
	co.MaxTokens = 16000
	return co
}

// currentModel is an Opus-4.8-class ID (adaptive thinking + effort, no
// temperature); legacyModel is a Sonnet-4.5 Bedrock ID (budget_tokens +
// temperature). See thinkingSupportFor.
const (
	currentModel = "claude-opus-4-8"
	legacyModel  = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
)

func TestThinkingSupportFor(t *testing.T) {
	cases := map[string]thinkingSupport{
		// adaptive
		"claude-opus-4-8": thinkingAdaptive,
		"claude-opus-4-7": thinkingAdaptive,
		"claude-opus-4-6": thinkingAdaptive,
		"claude-sonnet-5": thinkingAdaptive,
		"claude-fable-5":  thinkingAdaptive,
		"claude-opus-5":   thinkingAdaptive,
		// budget
		legacyModel:         thinkingBudget,
		"claude-sonnet-4-5": thinkingBudget,
		"claude-haiku-4-5":  thinkingBudget,
		"claude-opus-4-5":   thinkingBudget,
		"claude-3-7-sonnet": thinkingBudget,
		// none: no extended thinking, or unknown
		"claude-3-5-sonnet-v2": thinkingNone,
		"claude-3-5-haiku":     thinkingNone,
		"claude-3-haiku":       thinkingNone,
		"claude-3-opus":        thinkingNone,
		"gpt-5.6":              thinkingNone,
		"some-future-model":    thinkingNone,
	}
	for model, want := range cases {
		if got := thinkingSupportFor(model); got != want {
			t.Errorf("thinkingSupportFor(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestApplyReasoningCurrentModel(t *testing.T) {
	m := &anthropicNativeModel{model: currentModel}
	var params anthropic.MessageNewParams
	params.MaxTokens = 16000
	m.applyReasoning(&params, lowEffortOpts(), currentModel)

	if params.Thinking.OfAdaptive == nil {
		t.Fatal("current model should use adaptive thinking")
	}
	if params.Thinking.OfEnabled != nil {
		t.Error("current model must not set budget_tokens (400s)")
	}
	if params.OutputConfig.Effort != anthropic.OutputConfigEffortHigh {
		t.Errorf("effort = %q, want high", params.OutputConfig.Effort)
	}
	if params.Temperature.Valid() {
		t.Error("current model must not set temperature (400s)")
	}
}

func TestApplyReasoningLegacyModel(t *testing.T) {
	m := &anthropicNativeModel{model: legacyModel}
	var params anthropic.MessageNewParams
	params.MaxTokens = 16000
	m.applyReasoning(&params, lowEffortOpts(), legacyModel)

	if params.Thinking.OfEnabled == nil {
		t.Fatal("legacy model should use budget_tokens thinking")
	}
	if got := params.Thinking.OfEnabled.BudgetTokens; got != 12800 {
		t.Errorf("budget = %d, want 12800 (80%% of 16000)", got)
	}
	if params.Thinking.OfAdaptive != nil {
		t.Error("legacy model must not use adaptive thinking")
	}
	if !params.Temperature.Valid() {
		t.Error("legacy model should set temperature")
	}
}

// TestApplyReasoningNoThinkingModel covers a no-extended-thinking / unknown ID:
// even with an effort request, it must omit BOTH thinking and temperature so it
// can never 400 on an unsupported field.
func TestApplyReasoningNoThinkingModel(t *testing.T) {
	for _, model := range []string{"claude-3-5-sonnet-v2", "some-future-model"} {
		m := &anthropicNativeModel{model: model}
		var params anthropic.MessageNewParams
		params.MaxTokens = 16000
		m.applyReasoning(&params, lowEffortOpts(), model)

		if params.Thinking.OfEnabled != nil || params.Thinking.OfAdaptive != nil {
			t.Errorf("%s: must not enable any thinking", model)
		}
		if params.Temperature.Valid() {
			t.Errorf("%s: must not set temperature (may 400 on an unknown current model)", model)
		}
	}
}

// TestApplyReasoningAgenticNoThinking mirrors the tool-loop path: no thinking
// config, temperature 0. Current models must drop the temperature; neither class
// should enable thinking.
func TestApplyReasoningAgenticNoThinking(t *testing.T) {
	var co llms.CallOptions
	llms.WithTemperature(0)(&co)
	co.MaxTokens = 16000

	current := &anthropicNativeModel{model: currentModel}
	var cp anthropic.MessageNewParams
	current.applyReasoning(&cp, co, currentModel)
	if cp.Thinking.OfAdaptive != nil || cp.Thinking.OfEnabled != nil {
		t.Error("agentic current call should not enable thinking")
	}
	if cp.Temperature.Valid() {
		t.Error("agentic current call must not set temperature")
	}

	legacy := &anthropicNativeModel{model: legacyModel}
	var lp anthropic.MessageNewParams
	legacy.applyReasoning(&lp, co, legacyModel)
	if lp.Thinking.OfEnabled != nil {
		t.Error("agentic legacy call should not enable thinking without a config")
	}
}

func TestTranslateMessagesSystemAndConvo(t *testing.T) {
	m := &anthropicNativeModel{model: currentModel, cacheTTL: cacheTTL}
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "sys prompt"),
		llms.TextParts(llms.ChatMessageTypeHuman, "user prompt"),
	}
	system, convo := m.translateMessages(msgs)

	if len(system) != 1 || system[0].Text != "sys prompt" {
		t.Fatalf("system = %+v", system)
	}
	// Cache breakpoint on the last system block.
	if system[0].CacheControl.TTL != cacheTTL {
		t.Error("expected cache_control on last system block")
	}
	if len(convo) != 1 || convo[0].Role != anthropic.MessageParamRoleUser {
		t.Fatalf("convo = %+v", convo)
	}
	// Cache breakpoint on the last conversation block.
	if cc := convo[0].Content[len(convo[0].Content)-1].GetCacheControl(); cc == nil || cc.TTL != cacheTTL {
		t.Error("expected cache_control on last conversation block")
	}
}

func TestTranslateMessagesNoCacheWhenDisabled(t *testing.T) {
	m := &anthropicNativeModel{model: currentModel, cacheTTL: ""}
	system, convo := m.translateMessages([]llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "sys"),
		llms.TextParts(llms.ChatMessageTypeHuman, "u"),
	})
	if system[0].CacheControl.TTL != "" {
		t.Error("cacheTTL empty should place no system breakpoint")
	}
	if cc := convo[0].Content[0].GetCacheControl(); cc != nil && cc.TTL != "" {
		t.Error("cacheTTL empty should place no conversation breakpoint")
	}
}

// TestTranslateMessagesToolRoundTrip verifies an assistant tool-call turn and a
// tool-result turn map to the SDK's tool_use / tool_result blocks.
func TestTranslateMessagesToolRoundTrip(t *testing.T) {
	m := &anthropicNativeModel{model: currentModel}
	asst := llms.MessageContent{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{
		llms.ToolCall{ID: "t1", FunctionCall: &llms.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
	}}
	tool := llms.MessageContent{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{
		llms.ToolCallResponse{ToolCallID: "t1", Name: "read_file", Content: "file body"},
	}}
	_, convo := m.translateMessages([]llms.MessageContent{asst, tool})

	if len(convo) != 2 {
		t.Fatalf("want 2 messages, got %d", len(convo))
	}
	if convo[0].Role != anthropic.MessageParamRoleAssistant {
		t.Errorf("tool-call turn role = %q, want assistant", convo[0].Role)
	}
	if convo[0].Content[0].OfToolUse == nil || convo[0].Content[0].OfToolUse.ID != "t1" {
		t.Error("assistant turn should carry a tool_use block with id t1")
	}
	if convo[1].Role != anthropic.MessageParamRoleUser {
		t.Errorf("tool-result turn role = %q, want user", convo[1].Role)
	}
	if convo[1].Content[0].OfToolResult == nil || convo[1].Content[0].OfToolResult.ToolUseID != "t1" {
		t.Error("tool turn should carry a tool_result block for t1")
	}
}

func TestToToolParams(t *testing.T) {
	tools := []llms.Tool{{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        "search",
			Description: "search the repo",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"pattern": map[string]any{"type": "string"}},
				"required":   []any{"pattern"},
			},
		},
	}}
	out := toToolParams(tools)
	if len(out) != 1 || out[0].OfTool == nil {
		t.Fatalf("out = %+v", out)
	}
	tp := out[0].OfTool
	if tp.Name != "search" {
		t.Errorf("name = %q", tp.Name)
	}
	if tp.Description.Value != "search the repo" {
		t.Errorf("description = %q", tp.Description.Value)
	}
	if len(tp.InputSchema.Required) != 1 || tp.InputSchema.Required[0] != "pattern" {
		t.Errorf("required = %+v", tp.InputSchema.Required)
	}
	if tp.InputSchema.Properties == nil {
		t.Error("properties not carried through")
	}
}

func TestToContentResponse(t *testing.T) {
	// Build the Message the way the SDK does — by unmarshaling an API response —
	// so ContentBlockUnion.AsAny() repacks each block from its JSON.
	raw := `{
		"stop_reason": "tool_use",
		"content": [
			{"type": "text", "text": "some thinking narration"},
			{"type": "tool_use", "id": "t9", "name": "list_dir", "input": {"path": "."}}
		],
		"usage": {
			"input_tokens": 10, "output_tokens": 5,
			"cache_creation_input_tokens": 100, "cache_read_input_tokens": 4096
		}
	}`
	var msg anthropic.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}

	resp := toContentResponse(&msg)
	text, tools, u := agentCollapse(resp)
	if text != "some thinking narration" {
		t.Errorf("text = %q", text)
	}
	if len(tools) != 1 || tools[0].FunctionCall.Name != "list_dir" || tools[0].ID != "t9" {
		t.Errorf("tools = %+v", tools)
	}
	if u.In != 10 || u.Out != 5 || u.CacheWrite != 100 || u.CacheRead != 4096 {
		t.Errorf("usage = %+v", u)
	}
}

// TestToContentResponseRefusal ensures a content-less refusal still yields one
// choice (so the loop doesn't treat it as a hard empty-response error) carrying
// the stop reason.
func TestToContentResponseRefusal(t *testing.T) {
	msg := &anthropic.Message{StopReason: anthropic.StopReasonRefusal}
	resp := toContentResponse(msg)
	if len(resp.Choices) != 1 {
		t.Fatalf("want 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].StopReason != "refusal" {
		t.Errorf("stop reason = %q", resp.Choices[0].StopReason)
	}
}

func TestCacheAwarePricing(t *testing.T) {
	// input $3/M, output $15/M. 1M in, 1M out, 1M cache-write, 1M cache-read.
	got := costClaude("claude-sonnet-5", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	want := 3.0 + 15.0 + 3.0*cacheWriteMult + 3.0*cacheReadMult
	if got != want {
		t.Errorf("cost = %v, want %v", got, want)
	}

	// Zero cache tokens must reproduce the old input+output-only figure.
	plain := costClaude("claude-sonnet-5", 1_000_000, 1_000_000, 0, 0)
	if plain != 18.0 {
		t.Errorf("plain cost = %v, want 18.0", plain)
	}
}

// TestCacheWriteMultMatchesTTL guards the coupling the reviewer flagged: the
// cache-write price multiplier must match the configured cache TTL (2x for 1h,
// 1.25x for 5m). If the TTL const changes, this fails until the multiplier is
// updated.
func TestCacheWriteMultMatchesTTL(t *testing.T) {
	want := map[anthropic.CacheControlEphemeralTTL]float64{
		anthropic.CacheControlEphemeralTTLTTL1h: 2.0,
		anthropic.CacheControlEphemeralTTLTTL5m: 1.25,
	}[cacheTTL]
	if want == 0 {
		t.Fatalf("unexpected cacheTTL %q; add its write multiplier", cacheTTL)
	}
	if cacheWriteMult != want {
		t.Errorf("cacheWriteMult = %v for TTL %q, want %v", cacheWriteMult, cacheTTL, want)
	}
}

func TestCacheAwarePricingBedrock(t *testing.T) {
	// claudeBedrockPricing "claude-sonnet-4" -> input $3/M.
	got := costClaudeBedrock(legacyModel, 0, 0, 1_000_000, 1_000_000)
	want := 3.0*cacheWriteMult + 3.0*cacheReadMult
	if got != want {
		t.Errorf("bedrock cache cost = %v, want %v", got, want)
	}
}

// agentCollapse is a thin indirection so this test reads the same folding path
// the loop uses without importing the agent package here.
func agentCollapse(resp *llms.ContentResponse) (string, []llms.ToolCall, usage) {
	var text string
	var tools []llms.ToolCall
	var u usage
	for _, c := range resp.Choices {
		if c == nil {
			continue
		}
		text += c.Content
		tools = append(tools, c.ToolCalls...)
	}
	for _, c := range resp.Choices {
		if c != nil && c.GenerationInfo != nil {
			u = usage{
				In:         toInt(c.GenerationInfo["InputTokens"]),
				Out:        toInt(c.GenerationInfo["OutputTokens"]),
				CacheWrite: toInt(c.GenerationInfo["CacheCreationInputTokens"]),
				CacheRead:  toInt(c.GenerationInfo["CacheReadInputTokens"]),
			}
			break
		}
	}
	return text, tools, u
}

type usage struct{ In, Out, CacheWrite, CacheRead int }

func toInt(v any) int {
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}
