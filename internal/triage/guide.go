package triage

import "context"

// The architecture guide is run-scoped, read-only prompt content (the text of a
// --context-guide file). It is threaded through context.Context rather than added
// to every prompt-building signature, the same way the progress sink is, so the
// pipeline's interfaces stay unchanged. The guide itself is surfaced to the model
// by the agent ToolBox's manifest; these helpers just carry the text to the
// point where the ToolBox is built.

type guideKey struct{}

// WithGuide returns a context carrying the architecture-guide text.
func WithGuide(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, guideKey{}, text)
}

// GuideFrom returns the architecture-guide text on ctx, or "" if none.
func GuideFrom(ctx context.Context) string {
	if s, ok := ctx.Value(guideKey{}).(string); ok {
		return s
	}
	return ""
}
