package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/seschis/concord/internal/agent"
	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/provider"
	"github.com/seschis/concord/internal/triage"
)

// Opts carries the per-run knobs a strategy needs.
type Opts struct {
	SrcRoot      string
	ContextRoots []agent.Root
	Effort       string
}

// Run is a strategy's output for one finding.
type Run struct {
	Results   []triage.Result
	ExtraCost float64 // cost of the shared explorer; 0 for per-model
	// GatherErr is the shared gatherer's failure, when one ran and failed:
	// the brief degrades to empty (metadata-only) and the report must show
	// it rather than claim context was gathered.
	GatherErr string
}

// Strategy decides how the voters get their code context.
type Strategy interface {
	Name() string
	Run(ctx context.Context, f finding.Finding, voters []provider.Provider, o Opts) Run
}

// ContextGatherer builds a shared context brief by exploring the repo (and any
// supporting context roots) once.
type ContextGatherer interface {
	Gather(ctx context.Context, f finding.Finding, srcRoot string, contextRoots []agent.Root, effort string) (string, float64, error)
}

// SharedContext (default) gathers one brief and feeds it to every voter
// single-shot. All voters see identical evidence.
type SharedContext struct {
	Gatherer ContextGatherer
}

func (SharedContext) Name() string { return "shared" }

func (s SharedContext) Run(ctx context.Context, f finding.Finding, voters []provider.Provider, o Opts) Run {
	brief := ""
	var extra float64
	var gatherErr string
	if o.SrcRoot != "" && s.Gatherer != nil {
		b, c, err := s.Gatherer.Gather(ctx, f, o.SrcRoot, o.ContextRoots, o.Effort)
		extra = c
		if err == nil {
			brief = b
		} else {
			gatherErr = err.Error()
		}
	}
	results := fanOut(voters, func(p provider.Provider) triage.Result {
		r, _ := p.Analyze(ctx, f, provider.AnalyzeInput{SharedContext: brief, Effort: o.Effort})
		return r
	})
	return Run{Results: results, ExtraCost: extra, GatherErr: gatherErr}
}

// PerModelAgent gives each voter its own tool loop to crawl the repo.
type PerModelAgent struct{}

func (PerModelAgent) Name() string { return "per-model" }

func (PerModelAgent) Run(ctx context.Context, f finding.Finding, voters []provider.Provider, o Opts) Run {
	results := fanOut(voters, func(p provider.Provider) triage.Result {
		in := provider.AnalyzeInput{Effort: o.Effort}
		if o.SrcRoot != "" {
			in.Tools = &provider.ToolEnv{SrcRoot: o.SrcRoot, ContextRoots: o.ContextRoots}
		}
		r, _ := p.Analyze(ctx, f, in)
		return r
	})
	return Run{Results: results}
}

// NewStrategy builds a strategy by name. The gatherer is only used by shared.
func NewStrategy(name string, gatherer ContextGatherer) (Strategy, error) {
	switch name {
	case "", "shared":
		return SharedContext{Gatherer: gatherer}, nil
	case "per-model":
		return PerModelAgent{}, nil
	default:
		return nil, fmt.Errorf("unknown context strategy %q (want shared|per-model)", name)
	}
}

// fanOut runs fn for every voter concurrently and returns results in order. Voter
// failures surface inside the Result (Error field), never as a panic, so one
// model dropping out does not abort the finding.
func fanOut(voters []provider.Provider, fn func(provider.Provider) triage.Result) []triage.Result {
	results := make([]triage.Result, len(voters))
	var wg sync.WaitGroup
	for i, p := range voters {
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			results[i] = fn(p)
		}(i, p)
	}
	wg.Wait()
	return results
}
