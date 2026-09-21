package engine

import (
	"context"
	"math"
	"testing"

	"github.com/seschis/harmonia/internal/finding"
	"github.com/seschis/harmonia/internal/provider"
	"github.com/seschis/harmonia/internal/triage"
)

type fakeStrategy struct {
	results []triage.Result
	extra   float64
}

func (fakeStrategy) Name() string { return "fake" }
func (s fakeStrategy) Run(_ context.Context, _ finding.Finding, _ []provider.Provider, _ Opts) Run {
	return Run{Results: s.results, ExtraCost: s.extra}
}

// fakeJudge is a panel member that returns a fixed verdict. It deliberately omits
// Judge/Model from its result so the test proves the engine stamps them.
type fakeJudge struct {
	name    string
	model   string
	verdict triage.Verdict
	cost    float64
	calls   int
}

func (j *fakeJudge) Name() string  { return j.name }
func (j *fakeJudge) Model() string { return j.model }
func (j *fakeJudge) Adjudicate(_ context.Context, _ finding.Finding, _ []triage.Result, _ string) (triage.AdjudicationResult, error) {
	j.calls++
	return triage.AdjudicationResult{FinalVerdict: j.verdict, CostUSD: j.cost}, nil
}

func TestTriageOneJudgePanelResolvesTie(t *testing.T) {
	voters := []triage.Result{
		{Provider: "claude", FinalVerdict: triage.ConfirmedReal},
		{Provider: "gemini", FinalVerdict: triage.LikelyReal},
		{Provider: "openai", FinalVerdict: triage.Unlikely},
		{Provider: "azure", FinalVerdict: triage.NotExploitable},
	}
	strict := &fakeJudge{name: "strict", model: "claude", verdict: triage.NotExploitable, cost: 0.1}
	business := &fakeJudge{name: "business", model: "claude", verdict: triage.Unlikely, cost: 0.2}
	codeflow := &fakeJudge{name: "codeflow", model: "claude", verdict: triage.ConfirmedReal, cost: 0.3}
	e := &Engine{Strategy: fakeStrategy{results: voters}, Judges: []Judge{strict, business, codeflow}}

	fr, cost := e.TriageOne(context.Background(), finding.Finding{ID: "F1"})

	// Panel: not_real (NotExploitable, Unlikely)=2 beats real (ConfirmedReal)=1 -> UNLIKELY.
	if fr.FinalVerdict != "UNLIKELY" {
		t.Fatalf("panel should break the 2-2 voter tie to UNLIKELY, got %s", fr.FinalVerdict)
	}
	if len(fr.Adjudications) != 3 {
		t.Fatalf("want 3 adjudications, got %d", len(fr.Adjudications))
	}
	// The engine stamps each adjudication with its judge name and model, in panel order.
	if fr.Adjudications[0].Judge != "strict" || fr.Adjudications[0].Model != "claude" {
		t.Fatalf("adjudication[0] not stamped: %+v", fr.Adjudications[0])
	}
	if fr.Adjudications[1].Judge != "business" || fr.Adjudications[2].Judge != "codeflow" {
		t.Fatalf("adjudications out of panel order: %+v", fr.Adjudications)
	}
	if strict.calls != 1 || business.calls != 1 || codeflow.calls != 1 {
		t.Fatalf("each judge should run once: strict=%d business=%d codeflow=%d", strict.calls, business.calls, codeflow.calls)
	}
	if math.Abs(cost-0.6) > 1e-9 {
		t.Fatalf("cost = %v, want 0.6 (voters 0 + judges 0.6)", cost)
	}
}

func TestTriageOneNoJudgesKeepsTie(t *testing.T) {
	voters := []triage.Result{
		{Provider: "claude", FinalVerdict: triage.ConfirmedReal},
		{Provider: "gemini", FinalVerdict: triage.Unlikely},
	}
	e := &Engine{Strategy: fakeStrategy{results: voters}, Judges: nil}
	fr, _ := e.TriageOne(context.Background(), finding.Finding{ID: "F1"})
	if fr.FinalVerdict != "NEEDS_MORE_CONTEXT" {
		t.Fatalf("no judges: tie should stay NEEDS_MORE_CONTEXT, got %s", fr.FinalVerdict)
	}
	if len(fr.Adjudications) != 0 {
		t.Fatalf("no judges: want 0 adjudications, got %d", len(fr.Adjudications))
	}
}

// errJudge models a judge whose call fails (API error / empty response): it
// returns an AdjudicationResult with an Error set and an empty FinalVerdict.
type errJudge struct{ err string }

func (e *errJudge) Name() string  { return "failing" }
func (e *errJudge) Model() string { return "claude" }
func (e *errJudge) Adjudicate(_ context.Context, _ finding.Finding, _ []triage.Result, _ string) (triage.AdjudicationResult, error) {
	return triage.AdjudicationResult{Error: e.err}, nil
}

func TestTriageOneFailingJudgeDoesNotLeakEmptyVerdict(t *testing.T) {
	voters := []triage.Result{
		{Provider: "claude", FinalVerdict: triage.ConfirmedReal},
		{Provider: "gemini", FinalVerdict: triage.Unlikely},
	}
	e := &Engine{Strategy: fakeStrategy{results: voters}, Judges: []Judge{&errJudge{err: "boom"}}}
	fr, _ := e.TriageOne(context.Background(), finding.Finding{ID: "F1"})
	if fr.FinalVerdict == "" {
		t.Fatalf("final verdict must never be empty, got %q", fr.FinalVerdict)
	}
	if fr.FinalVerdict != "NEEDS_MORE_CONTEXT" {
		t.Fatalf("a failing single judge should leave NEEDS_MORE_CONTEXT, got %s", fr.FinalVerdict)
	}
	if len(fr.Adjudications) != 1 || fr.Adjudications[0].Error == "" {
		t.Fatalf("the failing adjudication should be recorded with its error: %+v", fr.Adjudications)
	}
	if fr.Adjudications[0].Judge != "failing" || fr.Adjudications[0].Model != "claude" {
		t.Fatalf("failed adjudication should still be stamped: %+v", fr.Adjudications[0])
	}
}

// A failing judge must not swing a panel away from a clear majority of the
// remaining judges.
func TestTriageOneFailingJudgeDoesNotSwingPanel(t *testing.T) {
	voters := []triage.Result{
		{Provider: "claude", FinalVerdict: triage.ConfirmedReal},
		{Provider: "gemini", FinalVerdict: triage.LikelyReal},
		{Provider: "openai", FinalVerdict: triage.Unlikely},
		{Provider: "azure", FinalVerdict: triage.NotExploitable},
	}
	ok1 := &fakeJudge{name: "strict", model: "claude", verdict: triage.NotExploitable}
	ok2 := &fakeJudge{name: "business", model: "claude", verdict: triage.Unlikely}
	e := &Engine{Strategy: fakeStrategy{results: voters}, Judges: []Judge{ok1, ok2, &errJudge{err: "boom"}}}
	fr, _ := e.TriageOne(context.Background(), finding.Finding{ID: "F1"})
	// not_real (2) beats real (0) and unknown (1, the failing judge) -> UNLIKELY.
	if fr.FinalVerdict != "UNLIKELY" {
		t.Fatalf("panel majority should win over the failing judge, got %s", fr.FinalVerdict)
	}
}

func TestTriageOneMajoritySkipsPanel(t *testing.T) {
	voters := []triage.Result{
		{Provider: "claude", FinalVerdict: triage.ConfirmedReal},
		{Provider: "gemini", FinalVerdict: triage.LikelyReal},
		{Provider: "openai", FinalVerdict: triage.Unlikely},
	}
	strict := &fakeJudge{name: "strict", model: "claude", verdict: triage.NotExploitable}
	e := &Engine{Strategy: fakeStrategy{results: voters}, Judges: []Judge{strict}}
	fr, _ := e.TriageOne(context.Background(), finding.Finding{ID: "F1"})
	// real=2, not_real=1 -> majority real -> CONFIRMED_REAL; not a tie, so the panel is not consulted.
	if fr.FinalVerdict != "CONFIRMED_REAL" {
		t.Fatalf("majority should be CONFIRMED_REAL, got %s", fr.FinalVerdict)
	}
	if len(fr.Adjudications) != 0 {
		t.Fatalf("no tie: want 0 adjudications, got %d", len(fr.Adjudications))
	}
	if strict.calls != 0 {
		t.Fatalf("panel should not run on a majority, but judge ran %d times", strict.calls)
	}
}
