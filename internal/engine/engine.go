package engine

import (
	"context"

	"github.com/seschis/concord/internal/agent"
	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/progress"
	"github.com/seschis/concord/internal/provider"
	"github.com/seschis/concord/internal/report"
	"github.com/seschis/concord/internal/triage"
)

// Adjudicator resolves a full disagreement between voters.
type Adjudicator interface {
	Adjudicate(ctx context.Context, f finding.Finding, results []triage.Result, effort string) (triage.AdjudicationResult, error)
}

// Engine ties together the voters, the context strategy, and the adjudicator.
type Engine struct {
	Voters       []provider.Provider
	Strategy     Strategy
	Adjudicator  Adjudicator
	SrcRoot      string
	ContextRoots []agent.Root
	Effort       string
}

// Progress is an optional per-finding callback for CLI output.
type Progress func(i int, f finding.Finding, results []triage.Result, final triage.Verdict, agreement string, adjudicated bool)

// TriageOne runs the full pipeline for a single finding and returns its result
// row plus the total cost incurred (voters, explorer, and any adjudication).
func (e *Engine) TriageOne(ctx context.Context, f finding.Finding) (report.FindingResult, float64) {
	run := e.Strategy.Run(ctx, f, e.Voters, Opts{SrcRoot: e.SrcRoot, ContextRoots: e.ContextRoots, Effort: e.Effort})

	cost := run.ExtraCost
	for _, r := range run.Results {
		cost += r.CostUSD
	}

	final, agreement := Vote(run.Results)

	var adj *triage.AdjudicationResult
	if agreement == "none" && e.Adjudicator != nil {
		a, err := e.Adjudicator.Adjudicate(ctx, f, run.Results, e.Effort)
		if err == nil {
			final = a.FinalVerdict
			cost += a.CostUSD
			adj = &a
		}
	}

	resultsMap := make(map[string]triage.Result, len(run.Results))
	for _, r := range run.Results {
		resultsMap[r.Provider] = r
	}
	return report.NewFindingResult(f, resultsMap, final, agreement, adj, run.ExtraCost), cost
}

// TriageAll processes every finding sequentially (voters within a finding run
// concurrently). It returns the result rows and the total cost.
func (e *Engine) TriageAll(ctx context.Context, findings []finding.Finding, prog Progress) ([]report.FindingResult, float64) {
	sink := progress.From(ctx)
	sink.Emit(progress.Event{Kind: progress.RunStart, Total: len(findings)})

	out := make([]report.FindingResult, 0, len(findings))
	var total float64
	for i, f := range findings {
		// Stop at a finding boundary if the run was cancelled (e.g. a graceful
		// TUI quit). Findings already completed stay in `out` for a partial
		// report; we don't burn through the remainder producing error stubs.
		if ctx.Err() != nil {
			break
		}
		sink.Emit(progress.Event{
			Kind: progress.FindingStart, Total: len(findings), FindingIdx: i,
			FindingID: f.ID, VulnType: f.VulnType, Severity: f.Severity,
		})

		fr, cost := e.TriageOne(ctx, f)
		total += cost
		out = append(out, fr)

		agreement := fr.Agreement
		if fr.Adjudication != nil {
			agreement = "adjudicated"
		}
		cvssScore := 0.0
		if fr.CVSS != nil {
			cvssScore = fr.CVSS.Score
		}
		sink.Emit(progress.Event{
			Kind: progress.FindingDone, FindingIdx: i, FindingID: f.ID,
			Verdict:   fr.FinalVerdict,
			Class:     triage.Classification(triage.Verdict(fr.FinalVerdict)),
			Agreement: agreement,
			CVSSScore: cvssScore,
		})

		if prog != nil {
			prog(i, f, resultsFromRow(fr), triage.Verdict(fr.FinalVerdict), fr.Agreement, fr.Adjudication != nil)
		}
	}

	sink.Emit(progress.Event{Kind: progress.RunDone, Total: len(findings)})
	return out, total
}

func resultsFromRow(fr report.FindingResult) []triage.Result {
	rs := make([]triage.Result, 0, len(fr.Results))
	for _, r := range fr.Results {
		rs = append(rs, r)
	}
	return rs
}
