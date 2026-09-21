package provider

import "github.com/seschis/harmonia/internal/triage"

// NewAnalyst builds a triage-analyzer voter: a clone of the preferred model's
// provider (same client, model, and pricing) that carries a distinct label and a
// persona "lens" layered onto the base triage prompt.
//
// Analysts take part in the majority vote as ordinary voters — they are a plain
// *LLMProvider, so they flow through the context Strategy and Vote unchanged.
// The point is to get ensemble diversity on a single model: several analysts on
// the same model still reach different verdicts, which is what makes the vote
// (and, on a tie, the judge panel) actually exercise for a single-provider run.
//
// label is the progress/report name (the persona name, e.g. "strict"); focus is
// the persona's lens text.
func NewAnalyst(base *LLMProvider, label, focus string) *LLMProvider {
	return &LLMProvider{
		llm:           base.llm,
		name:          label,
		model:         base.model,
		maxTokens:     base.maxTokens,
		cost:          base.cost,
		contextWindow: base.contextWindow,
		priced:        base.priced,
		system:        triage.BuildAnalystSystemPrompt(focus),
	}
}
