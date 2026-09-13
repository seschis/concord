package triage

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/seschis/concord/internal/finding"
)

// AdjudicationResult is the outcome of a single judge resolving (or weighing in
// on) a full disagreement. Judge and Model identify which judge produced it.
type AdjudicationResult struct {
	Judge             string  `json:"judge,omitempty"`
	Model             string  `json:"model,omitempty"`
	FinalVerdict      Verdict `json:"final_verdict"`
	FinalConfidence   string  `json:"final_confidence,omitempty"`
	Reasoning         string  `json:"final_reasoning,omitempty"`
	KeyDecidingFactor string  `json:"key_deciding_factor,omitempty"`
	StrongerAnalysis  string  `json:"stronger_analysis,omitempty"`
	InputTokens       int     `json:"input_tokens"`
	OutputTokens      int     `json:"output_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	Error             string  `json:"error,omitempty"`
}

const AdjudicationSystemPrompt = `You are a senior application security engineer adjudicating between independent triage analyses of the same finding. The analysts reached different verdict categories and you must determine the final verdict.

Consider which analysis traced the data flow more completely, which correctly identified or missed mitigations, which is more grounded in what the code actually shows, and whether facts from one analysis invalidate another's conclusion.

Respond with ONLY a valid JSON object, no markdown, no text outside the JSON:
{
  "agreement_analysis": "<where the analyses agree and disagree>",
  "stronger_analysis": "<which model's analysis is stronger, or 'neither'>",
  "final_verdict": "CONFIRMED_REAL|LIKELY_REAL|UNLIKELY|NOT_EXPLOITABLE|NEEDS_MORE_CONTEXT",
  "final_confidence": "HIGH|MEDIUM|LOW",
  "final_reasoning": "<comprehensive reasoning drawing on the analyses>",
  "key_deciding_factor": "<the single most important factor>"
}`

// BuildAdjudicationPrompt renders the finding and every model's analysis.
func BuildAdjudicationPrompt(f finding.Finding, results []Result) string {
	var b strings.Builder
	b.WriteString("Adjudicate the following finding.\n\n")
	b.WriteString(BuildUserPrompt(f, ""))
	b.WriteString("\n\nIndependent analyses:\n")
	for _, r := range results {
		fmt.Fprintf(&b, "\n--- %s (verdict: %s) ---\n", strings.ToUpper(r.Provider), r.FinalVerdict)
		if r.Error != "" {
			fmt.Fprintf(&b, "error: %s\n", r.Error)
			continue
		}
		if s := stringField(r.Part1, "reasoning"); s != "" {
			fmt.Fprintf(&b, "reachability: %s\n", s)
		}
		if s := stringField(r.Part1, "data_flow_trace"); s != "" {
			fmt.Fprintf(&b, "data flow: %s\n", s)
		}
		if r.Summary != "" {
			fmt.Fprintf(&b, "summary: %s\n", r.Summary)
		}
	}
	return b.String()
}

// ParseAdjudication parses the adjudicator's JSON response.
func ParseAdjudication(raw string) AdjudicationResult {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		if x := jsonObjectRe.FindString(text); x != "" {
			_ = json.Unmarshal([]byte(x), &m)
		}
	}
	if m == nil {
		return AdjudicationResult{FinalVerdict: NeedsMoreContext, Error: "unparseable adjudication"}
	}
	adj := AdjudicationResult{
		FinalVerdict:      verdictFrom(m, "final_verdict"),
		FinalConfidence:   strVal(m, "final_confidence"),
		Reasoning:         strVal(m, "final_reasoning"),
		KeyDecidingFactor: strVal(m, "key_deciding_factor"),
		StrongerAnalysis:  strVal(m, "stronger_analysis"),
	}
	if adj.FinalVerdict == "" {
		adj.FinalVerdict = NeedsMoreContext
	}
	return adj
}

func strVal(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
