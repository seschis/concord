// Package provider defines the Provider interface each model implements and the
// input that selects single-shot vs agentic operation. It imports triage and
// finding, never the other way round, so there is no import cycle.
package provider

import (
	"context"

	"github.com/seschis/concord/internal/agent"
	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/triage"
)

// ToolEnv describes the sandboxed directories the agentic loop may read: the
// finding's own repository (SrcRoot) plus any supporting architecture-context
// roots the model can opt into.
type ToolEnv struct {
	SrcRoot      string
	ContextRoots []agent.Root
	MaxIters     int
}

// AnalyzeInput selects how a provider analyzes a finding. Exactly one of
// SharedContext or Tools is set; the strategy guarantees which.
//   - SharedContext non-empty: run single-shot with this brief injected.
//   - Tools non-nil: run the agentic tool loop over the repo (phase 2).
type AnalyzeInput struct {
	SharedContext string
	Tools         *ToolEnv
	Effort        string
}

// Provider is one model backend.
type Provider interface {
	Name() string // "claude" | "gemini" | "codex" | "azure"
	Model() string
	Analyze(ctx context.Context, f finding.Finding, in AnalyzeInput) (triage.Result, error)
}
