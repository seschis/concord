// Package transcript records the tool calls and results an agentic loop made
// while triaging a finding, so a run can be debugged after the fact without
// reconstructing what the model explored from file timestamps and guesswork.
//
// Like progress, the writer is threaded through context.Context rather than
// added to every interface (Strategy, ContextGatherer, Provider), keeping the
// one-directional package graph unchanged. It has no internal dependencies so
// every layer can import it without risking an import cycle.
package transcript

import "context"

// ToolCall is one tool invocation and the result fed back to the model.
type ToolCall struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result"`
}

// Step is one model turn in an agentic loop: a batch of tool calls, or the
// model's final answer.
type Step struct {
	Iter       int        `json:"iter"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Text       string     `json:"text,omitempty"`
	InTokens   int        `json:"in_tokens"`
	OutTokens  int        `json:"out_tokens"`
	CacheWrite int        `json:"cache_write_tokens,omitempty"`
	CacheRead  int        `json:"cache_read_tokens,omitempty"`
	Final      bool       `json:"final"`
}

// Writer records agentic-loop steps for later inspection. Implementations must
// be safe for concurrent Record, since voters within a finding run on separate
// goroutines.
type Writer interface {
	// Record appends one step for the given role ("voter" | "explorer" |
	// "adjudicator"), provider name (e.g. "claude"), and finding ID.
	Record(role, provider, findingID string, s Step)
}

// Nop discards every step. From returns it when no writer is installed.
type Nop struct{}

// Record implements Writer.
func (Nop) Record(string, string, string, Step) {}

type ctxKey struct{}

// WithWriter returns a context carrying w, retrievable with From.
func WithWriter(ctx context.Context, w Writer) context.Context {
	return context.WithValue(ctx, ctxKey{}, w)
}

// From returns the writer installed on ctx, or a Nop if none.
func From(ctx context.Context) Writer {
	if w, ok := ctx.Value(ctxKey{}).(Writer); ok && w != nil {
		return w
	}
	return Nop{}
}
