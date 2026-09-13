package agent

import (
	"context"
	"errors"

	"github.com/tmc/langchaingo/llms"
)

// ToolCall is one tool invocation within a Step and the result fed back to the
// model.
type ToolCall struct {
	Tool   string
	Args   string
	Result string
}

// Step describes one model call inside the loop, reported via OnStep so a
// caller can surface realtime progress, accrue cost, and record a transcript.
// Tokens are per-call deltas, not running totals. This type stays in the agent
// package to keep it free of any internal dependency beyond langchaingo.
type Step struct {
	Iter       int        // 1-based iteration
	Tool       string     // first requested tool's name; "" when the model answered
	Args       string     // first requested tool's raw JSON args (caller truncates)
	ToolCount  int        // number of tools requested in this call
	ToolCalls  []ToolCall // every tool call this iteration, in order, with results
	Text       string     // interstitial text alongside tool calls, or the final answer
	InTokens   int        // this call's uncached input tokens
	OutTokens  int        // this call's output tokens
	CacheWrite int        // this call's cache-creation input tokens
	CacheRead  int        // this call's cache-read input tokens
	Final      bool       // the model produced its final answer on this call
}

// Usage holds the token counts for one model call. CacheWrite/CacheRead are
// populated by providers that report prompt-cache activity (the anthropic-sdk-go
// path); others leave them zero.
type Usage struct {
	In         int
	Out        int
	CacheWrite int
	CacheRead  int
}

// StepFunc observes each model call. It must not block the loop for long.
type StepFunc func(Step)

// LoopOptions configures one run of the tool-calling loop.
type LoopOptions struct {
	Model     llms.Model
	ModelName string
	System    string
	User      string
	Tools     []llms.Tool
	Exec      func(name, argsJSON string) string
	MaxIters  int
	MaxTokens int
	OnStep    StepFunc      // optional realtime progress callback
	Pruner    ContextPruner // optional; nil means send full history (NoPrune)
}

// send applies the pruning strategy to the running history, returning the view
// to transmit. The loop keeps msgs untouched and re-derives this view each turn.
func (o LoopOptions) send(msgs []llms.MessageContent) []llms.MessageContent {
	if o.Pruner == nil {
		return msgs
	}
	return o.Pruner.Prune(msgs)
}

// LoopResult is the outcome of a tool loop.
type LoopResult struct {
	FinalText        string
	InputTokens      int
	OutputTokens     int
	CacheWriteTokens int
	CacheReadTokens  int
	Iters            int
}

// MaxItersForEffort maps the effort knob to a tool-call budget.
// "high" is unlimited (capped at 200 as a runaway guard).
func MaxItersForEffort(effort string) int {
	switch effort {
	case "high":
		return 200
	case "medium":
		return 30
	default:
		return 10
	}
}

// RunToolLoop drives a model through repeated tool calls until it answers without
// requesting a tool or the iteration budget is spent. On budget exhaustion it
// makes one final tool-free call asking for the answer. Token usage is summed
// across every call.
func RunToolLoop(ctx context.Context, o LoopOptions) (LoopResult, error) {
	if o.MaxIters <= 0 {
		o.MaxIters = 8
	}
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, o.System),
		llms.TextParts(llms.ChatMessageTypeHuman, o.User),
	}
	var res LoopResult

	for i := 0; i < o.MaxIters; i++ {
		res.Iters = i + 1
		resp, err := o.Model.GenerateContent(ctx, o.send(msgs),
			llms.WithModel(o.ModelName),
			llms.WithMaxTokens(o.MaxTokens),
			llms.WithTemperature(0),
			llms.WithTools(o.Tools),
		)
		if err != nil {
			return res, err
		}
		if len(resp.Choices) == 0 {
			return res, errors.New("empty response")
		}
		text, toolCalls, u := Collapse(resp)
		res.accrue(u)

		if len(toolCalls) == 0 {
			if o.OnStep != nil {
				o.OnStep(Step{Iter: i + 1, Text: text, InTokens: u.In, OutTokens: u.Out, CacheWrite: u.CacheWrite, CacheRead: u.CacheRead, Final: true})
			}
			res.FinalText = text
			return res, nil
		}

		// Record the assistant's tool-call turn.
		asst := llms.MessageContent{Role: llms.ChatMessageTypeAI}
		if text != "" {
			asst.Parts = append(asst.Parts, llms.TextContent{Text: text})
		}
		for _, tc := range toolCalls {
			asst.Parts = append(asst.Parts, tc)
		}
		msgs = append(msgs, asst)

		// Execute each tool and feed results back, keeping the full detail for
		// OnStep (real-time progress plus, for a caller that wired one up, a
		// transcript of what the model actually saw).
		toolMsg := llms.MessageContent{Role: llms.ChatMessageTypeTool}
		calls := make([]ToolCall, 0, len(toolCalls))
		for _, tc := range toolCalls {
			name, argsJSON := "", ""
			if tc.FunctionCall != nil {
				name = tc.FunctionCall.Name
				argsJSON = tc.FunctionCall.Arguments
			}
			result := o.Exec(name, argsJSON)
			calls = append(calls, ToolCall{Tool: name, Args: argsJSON, Result: result})
			toolMsg.Parts = append(toolMsg.Parts, llms.ToolCallResponse{
				ToolCallID: tc.ID,
				Name:       name,
				Content:    result,
			})
		}
		msgs = append(msgs, toolMsg)

		if o.OnStep != nil {
			first := calls[0]
			o.OnStep(Step{
				Iter: i + 1, Tool: first.Tool, Args: first.Args, ToolCount: len(calls),
				ToolCalls: calls, Text: text,
				InTokens: u.In, OutTokens: u.Out, CacheWrite: u.CacheWrite, CacheRead: u.CacheRead,
			})
		}
	}

	// Budget spent: force a final, tool-free answer.
	msgs = append(msgs, llms.TextParts(llms.ChatMessageTypeHuman,
		"Stop exploring. Provide your final answer now."))
	resp, err := o.Model.GenerateContent(ctx, o.send(msgs),
		llms.WithModel(o.ModelName),
		llms.WithMaxTokens(o.MaxTokens),
		llms.WithTemperature(0),
	)
	if err != nil {
		return res, err
	}
	if len(resp.Choices) == 0 {
		return res, errors.New("empty final response")
	}
	text, _, u := Collapse(resp)
	res.accrue(u)
	if o.OnStep != nil {
		o.OnStep(Step{Iter: res.Iters, Text: text, InTokens: u.In, OutTokens: u.Out, CacheWrite: u.CacheWrite, CacheRead: u.CacheRead, Final: true})
	}
	res.FinalText = text
	return res, nil
}

// accrue adds one call's usage to the running loop totals.
func (r *LoopResult) accrue(u Usage) {
	r.InputTokens += u.In
	r.OutputTokens += u.Out
	r.CacheWriteTokens += u.CacheWrite
	r.CacheReadTokens += u.CacheRead
}

// Usage returns the loop's total token usage as a single value.
func (r LoopResult) Usage() Usage {
	return Usage{In: r.InputTokens, Out: r.OutputTokens, CacheWrite: r.CacheWriteTokens, CacheRead: r.CacheReadTokens}
}

// Collapse flattens a ContentResponse into combined text, all tool calls, and
// token usage. Anthropic returns one choice per content block (a "thinking"
// block with empty content, each tool_use as its own choice, then text), so
// reading only Choices[0] loses tool calls and, with thinking on, the text.
// Token usage is identical across choices, so it is read once, not summed.
func Collapse(resp *llms.ContentResponse) (text string, toolCalls []llms.ToolCall, u Usage) {
	for _, c := range resp.Choices {
		if c == nil {
			continue
		}
		if c.Content != "" {
			text += c.Content
		}
		toolCalls = append(toolCalls, c.ToolCalls...)
	}
	for _, c := range resp.Choices {
		if c != nil && c.GenerationInfo != nil {
			u = UsageTokens(c.GenerationInfo)
			break
		}
	}
	return
}

// UsageTokens reads token counts from GenerationInfo, tolerating the differing
// key names langchaingo providers use. Cache-token keys match the ones both
// langchaingo's own anthropic provider and this project's anthropic-sdk-go
// adapter emit, so the reader is backend-agnostic.
func UsageTokens(gi map[string]any) Usage {
	return Usage{
		In:         firstInt(gi, "InputTokens", "PromptTokens", "input_tokens", "prompt_tokens"),
		Out:        firstInt(gi, "OutputTokens", "CompletionTokens", "output_tokens", "completion_tokens"),
		CacheWrite: firstInt(gi, "CacheCreationInputTokens", "cache_creation_input_tokens"),
		CacheRead:  firstInt(gi, "CacheReadInputTokens", "cache_read_input_tokens"),
	}
}

func firstInt(gi map[string]any, keys ...string) int {
	if gi == nil {
		return 0
	}
	for _, k := range keys {
		if v, ok := gi[k]; ok {
			switch n := v.(type) {
			case int:
				return n
			case int64:
				return int(n)
			case float64:
				return int(n)
			}
		}
	}
	return 0
}
