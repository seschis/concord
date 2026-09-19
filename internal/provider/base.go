package provider

import (
	"context"
	"time"

	"github.com/tmc/langchaingo/llms"

	"github.com/seschis/concord/internal/agent"
	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/progress"
	"github.com/seschis/concord/internal/triage"
)

const explorerMaxTokens = 4000

// costFunc returns USD for a call given a model and token counts, including the
// two prompt-cache buckets (cache write / cache read). Non-Claude providers
// report zero cache tokens, so those terms vanish for them.
type costFunc func(model string, inTok, outTok, cacheWrite, cacheRead int) float64

// LLMProvider is the shared implementation behind every model backend. Each
// concrete provider is just an LLMProvider built with a different langchaingo
// client, model name, and pricing function. It carries the single-shot path, the
// agentic tool-loop path, the context explorer, and adjudication.
type LLMProvider struct {
	llm       llms.Model
	name      string
	model     string
	maxTokens int
	cost      costFunc
	// system, when non-empty, replaces the default triage SystemPrompt for the
	// Analyze paths. Analyst voters set it to a persona-lens prompt (see
	// NewAnalyst); ordinary model providers leave it empty.
	system string
	// contextWindow is the model's context window in tokens (0 = unknown).
	// It clamps the per-call output budget to half the window (input + output
	// must fit the window) and feeds the window-aware pruner in tool loops.
	contextWindow int
	// priced is true when the model has an explicit spec price or a built-in
	// table price (KTD9); unpriced models cost $0 and get a visible marker.
	priced bool
}

func (p *LLMProvider) Name() string  { return p.name }
func (p *LLMProvider) Model() string { return p.model }

// ContextWindow returns the model's context window in tokens (0 = unknown).
func (p *LLMProvider) ContextWindow() int { return p.contextWindow }

// Priced reports whether the model has an explicit or built-in-table price.
func (p *LLMProvider) Priced() bool { return p.priced }

// effectiveMaxTokens is the per-call output budget: the global max-tokens
// budget clamped to half the model's context window, so output can never
// consume more than half the window a single input+output exchange needs.
// An unknown window (0) leaves the budget unclamped; the clamp never raises
// the budget above --max-tokens. Presets default to a 128000 window, so with
// the default 16000 budget the clamp is a no-op and presets-only requests are
// byte-identical to before.
func (p *LLMProvider) effectiveMaxTokens() int {
	if p.contextWindow > 0 {
		if half := p.contextWindow / 2; half < p.maxTokens {
			return half
		}
	}
	return p.maxTokens
}

// explorerMaxTokens is the explorer's output budget: the fixed 4000 cap,
// clamped to half the model's context window when that is smaller.
func (p *LLMProvider) explorerMaxTokens() int {
	if p.contextWindow > 0 {
		if half := p.contextWindow / 2; half < explorerMaxTokens {
			return half
		}
	}
	return explorerMaxTokens
}

// systemPrompt returns the system prompt for the Analyze paths: the persona-lens
// override when present, else the shared triage prompt.
func (p *LLMProvider) systemPrompt() string {
	if p.system != "" {
		return p.system
	}
	return triage.SystemPrompt
}

// Analyze triages one finding. With in.Tools set it runs the agentic loop;
// otherwise it runs single-shot with any injected shared context.
func (p *LLMProvider) Analyze(ctx context.Context, f finding.Finding, in AnalyzeInput) (triage.Result, error) {
	if in.Tools != nil {
		return p.analyzeAgentic(ctx, f, in)
	}
	user := triage.BuildUserPrompt(f, in.SharedContext)
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, p.systemPrompt()),
		llms.TextParts(llms.ChatMessageTypeHuman, user),
	}

	opts := []llms.CallOption{llms.WithModel(p.model), llms.WithMaxTokens(p.effectiveMaxTokens())}
	opts = append(opts, thinkingOpts(in.Effort)...)

	sink := progress.From(ctx)
	sink.Emit(progress.Event{Kind: progress.Action, Provider: p.name, Role: progress.RoleVoter, FindingID: f.ID, Action: "analyzing"})

	start := time.Now()
	resp, err := p.llm.GenerateContent(ctx, msgs, opts...)
	elapsed := time.Since(start).Seconds()
	if err != nil {
		r := p.errResult(elapsed, err.Error())
		sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: p.name, Role: progress.RoleVoter, FindingID: f.ID, Verdict: string(r.FinalVerdict)})
		return r, nil
	}
	if len(resp.Choices) == 0 {
		r := p.errResult(elapsed, "empty response")
		sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: p.name, Role: progress.RoleVoter, FindingID: f.ID, Verdict: string(r.FinalVerdict)})
		return r, nil
	}
	text, _, u := agent.Collapse(resp)
	res, _ := triage.ParseResponse(text)
	p.stamp(&res, elapsed, u)
	sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: p.name, Role: progress.RoleVoter, FindingID: f.ID, Verdict: string(res.FinalVerdict), CostUSD: res.CostUSD})
	return res, nil
}

// thinkingOpts maps the effort knob to portable reasoning options. It emits
// WithThinkingMode (carrying the effort level) and temperature 1. Each backend
// reinterprets these: the anthropic-sdk-go adapter turns them into adaptive
// thinking + output_config.effort for current Claude models (dropping the
// temperature they reject) or budget_tokens + temperature for legacy models;
// OpenAI/Azure map WithThinkingMode to reasoning_effort; Gemini ignores it.
func thinkingOpts(effort string) []llms.CallOption {
	var mode llms.ThinkingMode
	switch effort {
	case "high":
		mode = llms.ThinkingModeHigh
	case "medium":
		mode = llms.ThinkingModeMedium
	default:
		mode = llms.ThinkingModeLow
	}
	return []llms.CallOption{llms.WithThinkingMode(mode), llms.WithTemperature(1)}
}

func (p *LLMProvider) analyzeAgentic(ctx context.Context, f finding.Finding, in AnalyzeInput) (triage.Result, error) {
	tb, err := agent.NewToolBox(in.Tools.SrcRoot, in.Tools.ContextRoots...)
	if err != nil {
		return p.errResult(0, err.Error()), nil
	}
	tb.WithGuide(triage.GuideFrom(ctx))
	user := triage.BuildUserPrompt(f, "") + manifestBlock(tb) +
		"\n\nExplore the repository with the tools to gather context, then output ONLY the triage JSON."
	maxIters := in.Tools.MaxIters
	if maxIters <= 0 {
		maxIters = agent.MaxItersForEffort(in.Effort)
	}

	sink := progress.From(ctx)
	start := time.Now()
	lr, err := agent.RunToolLoop(ctx, agent.LoopOptions{
		Model: p.llm, ModelName: p.model, System: p.systemPrompt(), User: user,
		Tools: tb.Definitions(), Exec: tb.Exec, MaxIters: maxIters, MaxTokens: p.effectiveMaxTokens(),
		OnStep: p.stepReporter(ctx, sink, progress.RoleVoter, f.ID),
		Pruner: agent.PrunerFrom(ctx), ContextWindow: p.contextWindow,
	})
	elapsed := time.Since(start).Seconds()
	if err != nil {
		r := p.errResult(elapsed, err.Error())
		p.stamp(&r, elapsed, lr.Usage())
		r.FinalVerdict = triage.NeedsMoreContext
		sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: p.name, Role: progress.RoleVoter, FindingID: f.ID, Verdict: string(r.FinalVerdict)})
		return r, nil
	}
	res, _ := triage.ParseResponse(lr.FinalText)
	p.stamp(&res, elapsed, lr.Usage())
	sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: p.name, Role: progress.RoleVoter, FindingID: f.ID, Verdict: string(res.FinalVerdict)})
	return res, nil
}

// Gather runs the explorer to produce a shared context brief for a finding. The
// explorer can read the finding's repo plus any supporting context roots.
func (p *LLMProvider) Gather(ctx context.Context, f finding.Finding, srcRoot string, contextRoots []agent.Root, effort string) (string, float64, error) {
	tb, err := agent.NewToolBox(srcRoot, contextRoots...)
	if err != nil {
		return "", 0, err
	}
	tb.WithGuide(triage.GuideFrom(ctx))
	user := triage.BuildUserPrompt(f, "") + manifestBlock(tb) +
		"\n\nExplore the repository and produce the context brief."
	sink := progress.From(ctx)
	lr, err := agent.RunToolLoop(ctx, agent.LoopOptions{
		Model: p.llm, ModelName: p.model, System: triage.ExplorerPrompt, User: user,
		Tools: tb.Definitions(), Exec: tb.Exec,
		MaxIters: agent.MaxItersForEffort(effort), MaxTokens: p.explorerMaxTokens(),
		OnStep: p.stepReporter(ctx, sink, progress.RoleExplorer, f.ID),
		Pruner: agent.PrunerFrom(ctx), ContextWindow: p.contextWindow,
	})
	cost := p.cost(p.model, lr.InputTokens, lr.OutputTokens, lr.CacheWriteTokens, lr.CacheReadTokens)
	verdict := "context ready"
	if err != nil {
		verdict = "explorer failed"
	}
	sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: p.name, Role: progress.RoleExplorer, FindingID: f.ID, Verdict: verdict})
	if err != nil {
		return "", cost, err
	}
	return lr.FinalText, cost, nil
}

// AdjudicateAs resolves a full disagreement by reviewing every analysis,
// labeling progress events with label and sending system as the system prompt.
func (p *LLMProvider) AdjudicateAs(ctx context.Context, f finding.Finding, results []triage.Result, label, system, effort string) (triage.AdjudicationResult, error) {
	prompt := triage.BuildAdjudicationPrompt(f, results)
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, system),
		llms.TextParts(llms.ChatMessageTypeHuman, prompt),
	}
	opts := []llms.CallOption{llms.WithModel(p.model), llms.WithMaxTokens(p.effectiveMaxTokens())}
	opts = append(opts, thinkingOpts(effort)...)

	sink := progress.From(ctx)
	sink.Emit(progress.Event{Kind: progress.Action, Provider: label, Role: progress.RoleAdjudicator, FindingID: f.ID, Action: "adjudicating tie"})

	resp, err := p.llm.GenerateContent(ctx, msgs, opts...)
	if err != nil {
		sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: label, Role: progress.RoleAdjudicator, FindingID: f.ID, Verdict: "error"})
		return triage.AdjudicationResult{Error: err.Error()}, err
	}
	if len(resp.Choices) == 0 {
		sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: label, Role: progress.RoleAdjudicator, FindingID: f.ID, Verdict: "error"})
		return triage.AdjudicationResult{Error: "empty response"}, nil
	}
	text, _, u := agent.Collapse(resp)
	adj := triage.ParseAdjudication(text)
	adj.InputTokens = u.In
	adj.OutputTokens = u.Out
	adj.CostUSD = p.cost(p.model, u.In, u.Out, u.CacheWrite, u.CacheRead)
	sink.Emit(progress.Event{Kind: progress.ModelDone, Provider: label, Role: progress.RoleAdjudicator, FindingID: f.ID, Verdict: string(adj.FinalVerdict), CostUSD: adj.CostUSD})
	return adj, nil
}

// Adjudicate resolves a full disagreement by reviewing every analysis.
func (p *LLMProvider) Adjudicate(ctx context.Context, f finding.Finding, results []triage.Result, effort string) (triage.AdjudicationResult, error) {
	return p.AdjudicateAs(ctx, f, results, p.name, triage.AdjudicationSystemPrompt, effort)
}

func (p *LLMProvider) stamp(r *triage.Result, elapsed float64, u agent.Usage) {
	r.Provider = p.name
	r.Model = p.model
	r.ElapsedSeconds = elapsed
	r.InputTokens = u.In
	r.OutputTokens = u.Out
	r.CostUSD = p.cost(p.model, u.In, u.Out, u.CacheWrite, u.CacheRead)
}

func (p *LLMProvider) errResult(elapsed float64, msg string) triage.Result {
	return triage.Result{
		Provider: p.name, Model: p.model, FinalVerdict: triage.NeedsMoreContext,
		ElapsedSeconds: elapsed, Error: msg,
	}
}
