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
type ContextPruner interface {
	Name() string
	Prune(msgs []llms.MessageContent) []llms.MessageContent
}

// NoPrune is the default strategy: it sends the full history unchanged, exactly
// as the loop behaved before pruning existed.
type NoPrune struct{}

func (NoPrune) Name() string { return "none" }

func (NoPrune) Prune(msgs []llms.MessageContent) []llms.MessageContent { return msgs }

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
	MinTrimChars int
	// HeadChars is how much of a trimmed result's head is preserved. The head of
	// a source file carries the package, imports, and declarations the model uses
	// to decide what to read next; the long method bodies below are the bulk.
	HeadChars int
}

// NewBreadcrumbPruner returns a BreadcrumbPruner with defaults tuned for the
// explorer: a six-turn full-detail window, trimming results over ~1.6 KB down to
// their first ~600 bytes plus a breadcrumb.
func NewBreadcrumbPruner() BreadcrumbPruner {
	return BreadcrumbPruner{RecentWindow: 6, MinTrimChars: 1600, HeadChars: 600}
}

func (BreadcrumbPruner) Name() string { return "breadcrumb" }

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

	out := make([]llms.MessageContent, len(msgs))
	for i, m := range msgs {
		if m.Role != llms.ChatMessageTypeTool || i >= keepFrom {
			out[i] = m // system, user, reasoning turns, and recent tool results pass through
			continue
		}
		out[i] = p.trimToolMsg(m, paths)
	}
	return out
}

// trimToolMsg returns a copy of an old tool-result message with each oversized
// response shrunk to its head plus a breadcrumb. ToolCallID and Name are copied
// verbatim so the tool_use/tool_result pairing the API requires stays intact.
func (p BreadcrumbPruner) trimToolMsg(m llms.MessageContent, paths map[string]string) llms.MessageContent {
	cp := llms.MessageContent{Role: m.Role, Parts: make([]llms.ContentPart, 0, len(m.Parts))}
	for _, part := range m.Parts {
		resp, ok := part.(llms.ToolCallResponse)
		if !ok || len(resp.Content) <= p.MinTrimChars {
			cp.Parts = append(cp.Parts, part)
			continue
		}
		resp.Content = p.trimContent(resp.Content, paths[resp.ToolCallID])
		cp.Parts = append(cp.Parts, resp)
	}
	return cp
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
