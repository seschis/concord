// Package progress carries realtime run events from the triage pipeline to a
// display (the Bubble Tea TUI, or a plain line printer). It has no internal
// dependencies so every layer can import it without risking an import cycle.
//
// The sink is threaded through context.Context rather than added to every
// interface (Strategy, ContextGatherer, Adjudicator, Provider), which keeps the
// carefully one-directional package graph unchanged. Layers that emit events
// pull the sink with From(ctx); when none is installed From returns a Nop, so
// emitting is always safe.
package progress

import "context"

// Kind is the type of a progress Event.
type Kind int

const (
	// RunStart is emitted once, before any finding, carrying Total.
	RunStart Kind = iota
	// FindingStart is emitted when a finding begins, carrying FindingIdx and
	// the finding's identity fields.
	FindingStart
	// Action reports what one model is doing right now: a tool call during an
	// agentic loop ("reading Foo.java", "grep ...") or the single-shot phase
	// ("analyzing", "adjudicating"). CostUSD is the incremental cost of the
	// model call that produced this action, so a consumer can accrue a live
	// total; it is 0 for the pre-call "starting" action.
	Action
	// ModelDone reports a model's verdict for the current finding. For
	// single-shot calls CostUSD holds the call cost; for agentic calls the cost
	// was already reported via Action events, so CostUSD is 0 here to avoid
	// double counting a live total.
	ModelDone
	// FindingDone reports the adjudicated verdict for a finding.
	FindingDone
	// RunDone is emitted once after the last finding.
	RunDone
)

// Role labels which pipeline stage a model event belongs to.
const (
	RoleVoter       = "voter"
	RoleExplorer    = "explorer"
	RoleAdjudicator = "adjudicator"
)

// Event is one realtime signal. Only the fields relevant to its Kind are set.
type Event struct {
	Kind       Kind
	Total      int     // RunStart, FindingStart
	FindingIdx int     // FindingStart, FindingDone (0-based)
	FindingID  string  // finding-scoped events
	VulnType   string  // FindingStart
	Severity   string  // FindingStart
	Provider   string  // model name, e.g. "claude" (model events)
	Role       string  // one of the Role* constants (model events)
	Action     string  // human-readable current action (Action)
	CostUSD    float64 // incremental/aggregate cost (Action, ModelDone)
	Verdict    string  // ModelDone, FindingDone (raw verdict, e.g. CONFIRMED_REAL)
	Class      string  // FindingDone: TRUE_POSITIVE | FALSE_POSITIVE | UNKNOWN
	Agreement  string  // FindingDone ("unanimous" | "majority" | "adjudicated" | ...)
	CVSSScore  float64 // FindingDone: CVSS 4.0 score (0 if not computed)
}

// Sink receives events. Implementations must be safe for concurrent Emit, since
// voters within a finding run on separate goroutines.
type Sink interface{ Emit(Event) }

// Nop discards every event. From returns it when no sink is installed.
type Nop struct{}

// Emit implements Sink.
func (Nop) Emit(Event) {}

// Func adapts a plain function to a Sink.
type Func func(Event)

// Emit implements Sink.
func (f Func) Emit(e Event) { f(e) }

type ctxKey struct{}

// WithSink returns a context carrying s, retrievable with From.
func WithSink(ctx context.Context, s Sink) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// From returns the sink installed on ctx, or a Nop if none.
func From(ctx context.Context) Sink {
	if s, ok := ctx.Value(ctxKey{}).(Sink); ok && s != nil {
		return s
	}
	return Nop{}
}
