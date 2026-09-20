package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

// ContextPruner transforms the running tool-loop message history before each
// model call. A long agentic loop re-sends its whole conversation prefix every
// turn (priced as cache reads), so shedding stale bulk from that prefix is the
// main lever on explorer cost. Implementations MUST be pure with respect to the
// input: return a new slice and never mutate the caller's messages, because the
// loop keeps the untouched history as the source of truth and re-derives the
// sent view every turn. They MUST also preserve every message (never drop one),
// so each tool_use keeps its matching tool_result; shrink result content only.
//
// WithWindow returns the per-loop form of the pruner: concurrent loops run for
// different models with different context windows, so a loop never shares a
// pruner instance (and its window) with another model's loop.
type ContextPruner interface {
	Name() string
	Prune(msgs []llms.MessageContent) []llms.MessageContent
	WithWindow(window int) ContextPruner
}

// NoPrune is the default strategy: it sends the full history unchanged, exactly
// as the loop behaved before pruning existed.
type NoPrune struct{}

func (NoPrune) Name() string { return "none" }

func (NoPrune) Prune(msgs []llms.MessageContent) []llms.MessageContent { return msgs }

// WithWindow has no effect: NoPrune trims nothing, so a window is irrelevant.
func (NoPrune) WithWindow(int) ContextPruner { return NoPrune{} }

// BreadcrumbPruner keeps the navigation cues and reasoning the model steers by
// while trimming the bulk it is only paying to re-read. It leaves the system and
// user turns intact, leaves every assistant reasoning turn intact (that is where
// the model records what it learned and what it still wants to check), keeps the
// most recent tool results in full (those drive the immediate next decision), and
// replaces only OLDER large tool results with their head plus a breadcrumb that
// names the file and tells the model it can re-read it. Because an old result's
// stub is a pure function of its content, once a result ages past the window its
// sent form is byte-stable turn to turn, so the cacheable prefix keeps growing
// rather than churning.
type BreadcrumbPruner struct {
	// RecentWindow is how many trailing tool-result turns are kept in full.
	RecentWindow int
	// MinTrimChars is the size a tool result must exceed before it is trimmed;
	// small results (short files, search hits) are cheap to carry and left alone.
	// Under window pressure (Window set and the estimated history past 80% of
	// it) stale results trim even below this floor.
	MinTrimChars int
	// HeadChars is how much of a trimmed result's head is preserved. The head of
	// a source file carries the package, imports, and declarations the model uses
	// to decide what to read next; the long method bodies below are the bulk.
	HeadChars int
	// Window is the model's context window in tokens (0 = window-blind, today's
	// behavior). A loop derives a per-call copy carrying its own model's window
	// via WithWindow, so concurrent models never see each other's window.
	Window int
}

// NewBreadcrumbPruner returns a BreadcrumbPruner with defaults tuned for the
// explorer: a six-turn full-detail window, trimming results over ~1.6 KB down to
// their first ~600 bytes plus a breadcrumb.
func NewBreadcrumbPruner() BreadcrumbPruner {
	return BreadcrumbPruner{RecentWindow: 6, MinTrimChars: 1600, HeadChars: 600}
}

func (BreadcrumbPruner) Name() string { return "breadcrumb" }

// WithWindow returns a copy carrying the model's context window, the per-loop
// isolation step a tool loop takes before a run.
func (p BreadcrumbPruner) WithWindow(window int) ContextPruner {
	p.Window = window
	return p
}

func (p BreadcrumbPruner) Prune(msgs []llms.MessageContent) []llms.MessageContent {
	// Map each tool_use ID to the path it read, recovered from the assistant
	// turn's call arguments, so a breadcrumb can name the file to re-read.
	paths := map[string]string{}
	for _, m := range msgs {
		if m.Role != llms.ChatMessageTypeAI {
			continue
		}
		for _, part := range m.Parts {
			if tc, ok := part.(llms.ToolCall); ok && tc.FunctionCall != nil {
				if path := argPath(tc.FunctionCall.Arguments); path != "" {
					paths[tc.ID] = path
				}
			}
		}
	}

	// Index the tool-result turns so we can tell "recent" from "old".
	var toolIdx []int
	for i, m := range msgs {
		if m.Role == llms.ChatMessageTypeTool {
			toolIdx = append(toolIdx, i)
		}
	}
	keepFrom := len(msgs) // by default trim nothing
	if len(toolIdx) > p.RecentWindow {
		keepFrom = toolIdx[len(toolIdx)-p.RecentWindow]
	}

	over := p.overWindow(msgs)
	out := make([]llms.MessageContent, len(msgs))
	for i, m := range msgs {
		if m.Role != llms.ChatMessageTypeTool || i >= keepFrom {
			out[i] = m // system, user, reasoning turns, and recent tool results pass through
			continue
		}
		out[i] = p.trimToolMsg(m, paths, over)
	}
	return out
}

// trimToolMsg returns a copy of an old tool-result message with each oversized
// response shrunk to its head plus a breadcrumb. ToolCallID and Name are copied
// verbatim so the tool_use/tool_result pairing the API requires stays intact.
// When over is set (window pressure) a response trims even below MinTrimChars,
// unless the trim would grow it — a tiny response's breadcrumb suffix alone is
// larger than the response itself.
func (p BreadcrumbPruner) trimToolMsg(m llms.MessageContent, paths map[string]string, over bool) llms.MessageContent {
	cp := llms.MessageContent{Role: m.Role, Parts: make([]llms.ContentPart, 0, len(m.Parts))}
	for _, part := range m.Parts {
		resp, ok := part.(llms.ToolCallResponse)
		if !ok || (len(resp.Content) <= p.MinTrimChars && !over) {
			cp.Parts = append(cp.Parts, part)
			continue
		}
		trimmed := p.trimContent(resp.Content, paths[resp.ToolCallID])
		if over && len(trimmed) >= len(resp.Content) {
			cp.Parts = append(cp.Parts, part)
			continue
		}
		resp.Content = trimmed
		cp.Parts = append(cp.Parts, resp)
	}
	return cp
}

// overWindow reports whether the estimated message tokens exceed 80% of the
// context window, the point at which stale tool results are shed even below
// the MinTrimChars floor. A zero window is window-blind: never over.
func (p BreadcrumbPruner) overWindow(msgs []llms.MessageContent) bool {
	if p.Window <= 0 {
		return false
	}
	// tokens*5 > 4*window is the integer form of tokens > 0.8*window.
	return estimateTokens(msgs)*5 > 4*p.Window
}

// estimateTokens estimates the token count of a message history with a
// chars/4 heuristic. It is a pressure signal for trimming decisions, never an
// exact budget.
func estimateTokens(msgs []llms.MessageContent) int {
	var chars int
	for _, m := range msgs {
		for _, part := range m.Parts {
			switch p := part.(type) {
			case llms.TextContent:
				chars += len(p.Text)
			case llms.ToolCallResponse:
				chars += len(p.Content)
			case llms.ToolCall:
				if p.FunctionCall != nil {
					chars += len(p.FunctionCall.Arguments)
				}
			}
		}
	}
	return chars / 4
}

func (p BreadcrumbPruner) trimContent(content, path string) string {
	head := content
	if len(head) > p.HeadChars {
		head = head[:p.HeadChars]
		if nl := strings.LastIndexByte(head, '\n'); nl > 0 {
			head = head[:nl] // avoid cutting mid-line
		}
	}
	elided := len(content) - len(head)
	where := "the tool"
	if path != "" {
		where = fmt.Sprintf("%q", path)
	}
	return fmt.Sprintf("%s\n\n…[%d chars elided to save context. Re-run the read to see all of %s again.]…",
		head, elided, where)
}

// argPath pulls a "path" field out of a tool call's JSON arguments, if present.
func argPath(argsJSON string) string {
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(argsJSON), &a) == nil {
		return a.Path
	}
	return ""
}

// Context threading, mirroring the progress sink and architecture guide: the
// chosen pruner is run-scoped and carried on the context rather than added to
// interfaces, so the one-way import graph is unchanged.

type prunerKey struct{}

// WithPruner returns a context carrying the pruning strategy for tool loops.
func WithPruner(ctx context.Context, p ContextPruner) context.Context {
	return context.WithValue(ctx, prunerKey{}, p)
}

// PrunerFrom returns the pruning strategy on ctx, or NoPrune if none is set.
func PrunerFrom(ctx context.Context) ContextPruner {
	if p, ok := ctx.Value(prunerKey{}).(ContextPruner); ok && p != nil {
		return p
	}
	return NoPrune{}
}
