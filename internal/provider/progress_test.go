package provider

import (
	"context"
	"testing"

	"github.com/seschis/concord/internal/agent"
	"github.com/seschis/concord/internal/progress"
	"github.com/seschis/concord/internal/transcript"
)

type recordingSink struct{ events []progress.Event }

func (r *recordingSink) Emit(e progress.Event) { r.events = append(r.events, e) }

type recordingTranscript struct {
	role, provider, findingID string
	steps                     []transcript.Step
}

func (r *recordingTranscript) Record(role, provider, findingID string, s transcript.Step) {
	r.role, r.provider, r.findingID = role, provider, findingID
	r.steps = append(r.steps, s)
}

func TestStepReporterEmitsProgressAndTranscript(t *testing.T) {
	// Capture every argument the cost function receives so the test fails if cache
	// tokens are dropped on the way to pricing, and price cache tokens distinctly
	// so a dropped bucket changes CostUSD.
	var gotCost struct {
		model           string
		in, out, cw, cr int
	}
	cost := func(model string, in, out, cw, cr int) float64 {
		gotCost.model, gotCost.in, gotCost.out, gotCost.cw, gotCost.cr = model, in, out, cw, cr
		return float64(in) + float64(out) + 2*float64(cw) + 3*float64(cr)
	}
	p := &LLMProvider{name: "claude", model: "claude-fake", cost: cost}

	sink := &recordingSink{}
	tw := &recordingTranscript{}
	ctx := transcript.WithWriter(context.Background(), tw)

	report := p.stepReporter(ctx, sink, progress.RoleVoter, "42")
	report(agent.Step{
		Iter:      1,
		ToolCalls: []agent.ToolCall{{Tool: "read_file", Args: `{"path":"a.go"}`, Result: "contents"}},
		Text:      "looking around",
		InTokens:  10, OutTokens: 5, CacheWrite: 100, CacheRead: 4096,
	})

	if len(sink.events) != 1 {
		t.Fatalf("want 1 progress event, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Provider != "claude" || ev.Role != progress.RoleVoter || ev.FindingID != "42" {
		t.Errorf("progress event mismatch: %+v", ev)
	}

	// The cost function must receive all four token buckets, including cache.
	if gotCost != (struct {
		model           string
		in, out, cw, cr int
	}{"claude-fake", 10, 5, 100, 4096}) {
		t.Errorf("cost args mismatch: %+v", gotCost)
	}
	// ...and the emitted CostUSD must reflect the cache tokens.
	if want := 10.0 + 5.0 + 2*100.0 + 3*4096.0; ev.CostUSD != want {
		t.Errorf("CostUSD = %v, want %v (cache tokens dropped?)", ev.CostUSD, want)
	}

	if len(tw.steps) != 1 {
		t.Fatalf("want 1 transcript step, got %d", len(tw.steps))
	}
	if tw.role != progress.RoleVoter || tw.provider != "claude" || tw.findingID != "42" {
		t.Errorf("transcript labels mismatch: role=%q provider=%q findingID=%q", tw.role, tw.provider, tw.findingID)
	}
	got := tw.steps[0]
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Tool != "read_file" || got.ToolCalls[0].Result != "contents" {
		t.Errorf("transcript step tool call mismatch: %+v", got)
	}
	if got.Text != "looking around" || got.Iter != 1 {
		t.Errorf("transcript step fields mismatch: %+v", got)
	}
	// The transcript must record the cache tokens too.
	if got.InTokens != 10 || got.OutTokens != 5 || got.CacheWrite != 100 || got.CacheRead != 4096 {
		t.Errorf("transcript token fields mismatch: %+v", got)
	}
}

func TestStepReporterWithNoTranscriptWriterStillEmitsProgress(t *testing.T) {
	p := &LLMProvider{name: "claude", model: "claude-fake", cost: func(string, int, int, int, int) float64 { return 0 }}
	sink := &recordingSink{}

	// No transcript.WithWriter on ctx: From falls back to Nop, which must not panic.
	report := p.stepReporter(context.Background(), sink, progress.RoleExplorer, "7")
	report(agent.Step{Iter: 1, Final: true, Text: "done"})

	if len(sink.events) != 1 {
		t.Fatalf("want 1 progress event, got %d", len(sink.events))
	}
}
