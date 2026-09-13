package provider

import (
	"context"
	"encoding/json"
	"path"
	"strconv"

	"github.com/seschis/concord/internal/agent"
	"github.com/seschis/concord/internal/progress"
	"github.com/seschis/concord/internal/transcript"
)

// stepReporter builds an agent.StepFunc that emits a progress.Action for each
// model call, tagged with this provider's name and the given role and priced
// per-call so a consumer can accrue a live cost total, and records the same
// step to the transcript.Writer installed on ctx (a Nop if none).
func (p *LLMProvider) stepReporter(ctx context.Context, sink progress.Sink, role, findingID string) agent.StepFunc {
	tw := transcript.From(ctx)
	return func(s agent.Step) {
		sink.Emit(progress.Event{
			Kind:      progress.Action,
			Provider:  p.name,
			Role:      role,
			FindingID: findingID,
			Action:    stepAction(s),
			CostUSD:   p.cost(p.model, s.InTokens, s.OutTokens, s.CacheWrite, s.CacheRead),
		})
		tw.Record(role, p.name, findingID, toTranscriptStep(s))
	}
}

// toTranscriptStep converts an agent.Step to the transcript package's own type,
// keeping transcript free of any dependency on agent.
func toTranscriptStep(s agent.Step) transcript.Step {
	calls := make([]transcript.ToolCall, len(s.ToolCalls))
	for i, tc := range s.ToolCalls {
		calls[i] = transcript.ToolCall{Tool: tc.Tool, Args: tc.Args, Result: tc.Result}
	}
	return transcript.Step{
		Iter: s.Iter, ToolCalls: calls, Text: s.Text,
		InTokens: s.InTokens, OutTokens: s.OutTokens,
		CacheWrite: s.CacheWrite, CacheRead: s.CacheRead, Final: s.Final,
	}
}

// stepAction renders a Step as a short human-readable action phrase.
func stepAction(s agent.Step) string {
	if s.Final || s.Tool == "" {
		return "reasoning"
	}
	switch s.Tool {
	case "read_file":
		if f := argString(s.Args, "path"); f != "" {
			return "reading " + path.Base(f)
		}
		return "reading a file"
	case "list_dir":
		if d := argString(s.Args, "path"); d != "" {
			return "listing " + d
		}
		return "listing a directory"
	case "search":
		if q := argString(s.Args, "pattern"); q != "" {
			return "search " + strconv.Quote(truncate(q, 32))
		}
		return "searching"
	default:
		return s.Tool
	}
}

// argString extracts one string field from a tool call's raw JSON args,
// tolerating malformed input by returning "".
func argString(argsJSON, key string) string {
	if argsJSON == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// manifestBlock renders a toolbox's context-root manifest as a prompt section,
// or "" when no context roots are configured.
func manifestBlock(tb *agent.ToolBox) string {
	m := tb.Manifest()
	if m == "" {
		return ""
	}
	return "\n\n" + m
}
