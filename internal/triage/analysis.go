// Package triage holds the verdict model, the triage prompt, response parsing,
// and (in later phases) voting and adjudication. It depends only on finding, so
// the provider package can import it without an import cycle.
package triage

// Verdict is a Part 1 / final reachability verdict.
type Verdict string

const (
	ConfirmedReal    Verdict = "CONFIRMED_REAL"
	LikelyReal       Verdict = "LIKELY_REAL"
	Unlikely         Verdict = "UNLIKELY"
	NotExploitable   Verdict = "NOT_EXPLOITABLE"
	NeedsMoreContext Verdict = "NEEDS_MORE_CONTEXT"
)

// Result is one model's analysis of one finding.
type Result struct {
	Provider       string            `json:"provider"`
	Model          string            `json:"model"`
	Part1          map[string]any    `json:"part1_reachability,omitempty"`
	Part2          map[string]any    `json:"part2_code_smell,omitempty"`
	Part3          map[string]any    `json:"part3_risk_reduction,omitempty"`
	DoubleCheck    map[string]any    `json:"double_check,omitempty"`
	CVSSVector     string            `json:"cvss_vector,omitempty"`
	CVSSMetrics    map[string]string `json:"cvss_metrics,omitempty"`
	FinalVerdict   Verdict           `json:"final_verdict"`
	Summary        string            `json:"summary,omitempty"`
	ThinkingTrace  string            `json:"thinking_trace,omitempty"`
	ElapsedSeconds float64           `json:"elapsed_seconds"`
	InputTokens    int               `json:"input_tokens"`
	OutputTokens   int               `json:"output_tokens"`
	CostUSD        float64           `json:"cost_usd"`
	Error          string            `json:"error,omitempty"`
}

// Classification maps a verdict to the concordAnalysis classification.
func Classification(v Verdict) string {
	switch v {
	case ConfirmedReal, LikelyReal:
		return "TRUE_POSITIVE"
	case Unlikely, NotExploitable:
		return "FALSE_POSITIVE"
	default:
		return "UNKNOWN"
	}
}
