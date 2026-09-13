package provider

import (
	"context"
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/tmc/langchaingo/llms"
)

// anthropicNativeModel adapts the official anthropic-sdk-go to langchaingo's
// llms.Model interface, so both Claude paths (direct API and Bedrock) share one
// implementation with native prompt caching. Gemini/OpenAI/Azure stay on
// langchaingo. Nothing upstream changes: the agent tool loop, single-shot path,
// and adjudication all call through this like any other llms.Model.
//
// The adapter also modernizes the request per model capability: langchaingo maps
// --effort to the legacy thinking:{type:"enabled",budget_tokens} API, which 400s
// on Opus 4.7/4.8, Sonnet 5, and Fable 5. applyReasoning instead classifies the
// model (thinkingSupportFor) into adaptive (adaptive thinking + effort, no
// temperature), budget (budget_tokens + temperature), or none (omit both —
// no-extended-thinking models and any unknown ID).
type anthropicNativeModel struct {
	client   anthropic.Client
	model    string
	cacheTTL anthropic.CacheControlEphemeralTTL // "" disables cache breakpoints
}

// GenerateContent implements llms.Model.
func (m *anthropicNativeModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	var co llms.CallOptions
	for _, o := range options {
		o(&co)
	}
	params := m.buildParams(co, messages)
	msg, err := m.client.Messages.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return toContentResponse(msg), nil
}

// Call implements llms.Model's text-only convenience method.
func (m *anthropicNativeModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	resp, err := m.GenerateContent(ctx,
		[]llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, prompt)}, options...)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", nil
	}
	return resp.Choices[0].Content, nil
}

func (m *anthropicNativeModel) buildParams(co llms.CallOptions, messages []llms.MessageContent) anthropic.MessageNewParams {
	model := m.model
	if co.Model != "" {
		model = co.Model
	}

	system, convo := m.translateMessages(messages)

	params := anthropic.MessageNewParams{
		Model:    anthropic.Model(model),
		Messages: convo,
	}
	if co.MaxTokens > 0 {
		params.MaxTokens = int64(co.MaxTokens)
	}
	if len(system) > 0 {
		params.System = system
	}
	if tools := toToolParams(co.Tools); len(tools) > 0 {
		params.Tools = tools
	}
	m.applyReasoning(&params, co, model)
	return params
}

// applyReasoning translates the effort/temperature call options into the SDK's
// request surface, differing by model class (see isLegacyThinkingModel).
func (m *anthropicNativeModel) applyReasoning(params *anthropic.MessageNewParams, co llms.CallOptions, model string) {
	mode := thinkingModeFrom(co)
	wantThinking := mode != llms.ThinkingModeNone && mode != ""

	switch thinkingSupportFor(model) {
	case thinkingAdaptive:
		// Adaptive thinking + effort; temperature is rejected on these models, so
		// it is never set (the loop's WithTemperature(0) and thinkingOpts' 1 are
		// dropped). On agentic calls no mode is supplied, so thinking stays off.
		if wantThinking {
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
			params.OutputConfig = anthropic.OutputConfigParam{Effort: effortFromMode(mode)}
		}

	case thinkingBudget:
		// Legacy extended thinking via budget_tokens; temperature accepted.
		if wantThinking {
			if budget := clampBudget(llms.CalculateThinkingBudget(mode, co.MaxTokens), co.MaxTokens); budget > 0 {
				params.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(budget))
			}
		}
		params.Temperature = anthropic.Float(co.Temperature)

	case thinkingNone:
		// No extended thinking, or an unknown ID. Omit both thinking and
		// temperature: that can never 400, whereas guessing either could.
	}
}

// clampBudget keeps a thinking budget within Claude's [1024, maxTokens-1] range,
// returning 0 when no valid budget is possible.
func clampBudget(budget, maxTokens int) int {
	if budget <= 0 {
		return 0
	}
	if budget < 1024 {
		budget = 1024
	}
	if maxTokens > 0 && budget >= maxTokens {
		budget = maxTokens - 1
	}
	if budget < 1024 {
		return 0
	}
	return budget
}

// thinkingModeFrom pulls the thinking mode langchaingo's WithThinkingMode stored
// on the call options' metadata, or "" when thinking was not requested.
func thinkingModeFrom(co llms.CallOptions) llms.ThinkingMode {
	if co.Metadata == nil {
		return ""
	}
	if cfg, ok := co.Metadata["thinking_config"].(*llms.ThinkingConfig); ok && cfg != nil {
		return cfg.Mode
	}
	return ""
}

func effortFromMode(mode llms.ThinkingMode) anthropic.OutputConfigEffort {
	switch mode {
	case llms.ThinkingModeLow:
		return anthropic.OutputConfigEffortLow
	case llms.ThinkingModeMedium:
		return anthropic.OutputConfigEffortMedium
	default:
		return anthropic.OutputConfigEffortHigh
	}
}

// translateMessages splits system messages from the conversation and converts
// each langchaingo message to the SDK's shape. Cache-control breakpoints are
// placed on the last system block (caches tools+system together) and the last
// content block of the last conversation message (caches the running prefix).
func (m *anthropicNativeModel) translateMessages(messages []llms.MessageContent) ([]anthropic.TextBlockParam, []anthropic.MessageParam) {
	var system []anthropic.TextBlockParam
	var convo []anthropic.MessageParam

	for _, msg := range messages {
		switch msg.Role {
		case llms.ChatMessageTypeSystem:
			for _, p := range msg.Parts {
				if tc, ok := p.(llms.TextContent); ok {
					system = append(system, anthropic.TextBlockParam{Text: tc.Text})
				}
			}
		case llms.ChatMessageTypeAI:
			if blocks := toBlocks(msg.Parts); len(blocks) > 0 {
				convo = append(convo, anthropic.NewAssistantMessage(blocks...))
			}
		default: // Human, Tool, Generic, Function -> a user-role message
			if blocks := toBlocks(msg.Parts); len(blocks) > 0 {
				convo = append(convo, anthropic.NewUserMessage(blocks...))
			}
		}
	}

	if m.cacheTTL != "" {
		if len(system) > 0 {
			system[len(system)-1].CacheControl = anthropic.CacheControlEphemeralParam{TTL: m.cacheTTL}
		}
		if len(convo) > 0 {
			last := convo[len(convo)-1].Content
			if len(last) > 0 {
				if cc := last[len(last)-1].GetCacheControl(); cc != nil {
					cc.TTL = m.cacheTTL
				}
			}
		}
	}
	return system, convo
}

// toBlocks converts langchaingo message parts to SDK content blocks.
func toBlocks(parts []llms.ContentPart) []anthropic.ContentBlockParamUnion {
	var blocks []anthropic.ContentBlockParamUnion
	for _, p := range parts {
		switch v := p.(type) {
		case llms.TextContent:
			if v.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(v.Text))
			}
		case llms.ToolCall:
			if v.FunctionCall == nil {
				continue
			}
			var input any = json.RawMessage(v.FunctionCall.Arguments)
			if v.FunctionCall.Arguments == "" {
				input = json.RawMessage("{}")
			}
			blocks = append(blocks, anthropic.NewToolUseBlock(v.ID, input, v.FunctionCall.Name))
		case llms.ToolCallResponse:
			blocks = append(blocks, anthropic.NewToolResultBlock(v.ToolCallID, v.Content, false))
		}
	}
	return blocks
}

// toToolParams converts langchaingo tool definitions to SDK tool params.
func toToolParams(tools []llms.Tool) []anthropic.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		if t.Function == nil {
			continue
		}
		tp := anthropic.ToolParam{
			Name:        t.Function.Name,
			InputSchema: toInputSchema(t.Function.Parameters),
		}
		if t.Function.Description != "" {
			tp.Description = anthropic.String(t.Function.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out
}

// toInputSchema converts a JSON-schema value (map[string]any as langchaingo
// carries it) into the SDK's typed input schema.
func toInputSchema(params any) anthropic.ToolInputSchemaParam {
	schema := anthropic.ToolInputSchemaParam{}
	if params == nil {
		return schema
	}
	b, err := json.Marshal(params)
	if err != nil {
		return schema
	}
	var raw struct {
		Properties any      `json:"properties"`
		Required   []string `json:"required"`
	}
	if json.Unmarshal(b, &raw) == nil {
		schema.Properties = raw.Properties
		schema.Required = raw.Required
	}
	return schema
}

// toContentResponse converts an SDK Message into langchaingo's multi-choice
// shape (one ContentChoice per text/tool_use block), matching what agent.Collapse
// already folds. Token usage — including the two cache buckets — is written to
// every choice's GenerationInfo under the same keys langchaingo's own anthropic
// provider uses, so agent.UsageTokens reads them backend-agnostically.
func toContentResponse(msg *anthropic.Message) *llms.ContentResponse {
	gi := map[string]any{
		"InputTokens":              int(msg.Usage.InputTokens),
		"OutputTokens":             int(msg.Usage.OutputTokens),
		"CacheCreationInputTokens": int(msg.Usage.CacheCreationInputTokens),
		"CacheReadInputTokens":     int(msg.Usage.CacheReadInputTokens),
	}
	stop := string(msg.StopReason)

	var choices []*llms.ContentChoice
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			choices = append(choices, &llms.ContentChoice{Content: b.Text})
		case anthropic.ToolUseBlock:
			choices = append(choices, &llms.ContentChoice{
				ToolCalls: []llms.ToolCall{{
					ID:   b.ID,
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      b.Name,
						Arguments: string(b.Input),
					},
				}},
			})
		}
		// thinking / redacted_thinking and any other block types are skipped:
		// the agent loop doesn't consume them.
	}

	// Always emit at least one choice so the loop's len(Choices)==0 guard doesn't
	// treat a refusal or a thinking-only turn as a hard "empty response" error.
	if len(choices) == 0 {
		choices = append(choices, &llms.ContentChoice{})
	}
	for _, c := range choices {
		c.GenerationInfo = gi
		c.StopReason = stop
	}
	return &llms.ContentResponse{Choices: choices}
}
