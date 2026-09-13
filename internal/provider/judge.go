package provider

import (
	"context"

	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/triage"
)

// Judge is one member of the adjudication panel: a named persona (a system
// prompt) running on a model backend. Different judges may share the same model
// but carry different personas, so a panel stays diverse even on a single model.
type Judge struct {
	name   string
	llm    *LLMProvider
	system string
}

// NewJudge builds a judge. system is the judge's full system prompt (persona +
// the shared JSON contract); it is sent verbatim as the system message.
func NewJudge(name string, llm *LLMProvider, system string) *Judge {
	return &Judge{name: name, llm: llm, system: system}
}

func (j *Judge) Name() string  { return j.name }
func (j *Judge) Model() string { return j.llm.model }

// Adjudicate runs this judge on a full disagreement, delegating to the model
// backend with the judge's name as the progress label and its persona as the
// system prompt.
func (j *Judge) Adjudicate(ctx context.Context, f finding.Finding, results []triage.Result, effort string) (triage.AdjudicationResult, error) {
	return j.llm.AdjudicateAs(ctx, f, results, j.name, j.system, effort)
}
