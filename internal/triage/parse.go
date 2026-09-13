package triage

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

// rawResponse matches the JSON contract in SystemPrompt.
type rawResponse struct {
	Part1       map[string]any `json:"part1_reachability"`
	Part2       map[string]any `json:"part2_code_smell"`
	Part3       map[string]any `json:"part3_risk_reduction"`
	DoubleCheck map[string]any `json:"double_check"`
	CVSS40      *rawCVSS       `json:"cvss40,omitempty"`
	Summary     string         `json:"summary"`
}

// rawCVSS matches the cvss40 block in the model response.
type rawCVSS struct {
	Vector  string            `json:"vector"`
	Metrics map[string]string `json:"metrics"`
}

// ParseResponse turns a model's raw text into a Result. It strips code fences,
// then falls back to extracting the first {...} block. On parse failure it
// returns a Result flagged NEEDS_MORE_CONTEXT with the error recorded.
func ParseResponse(raw string) (Result, error) {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var rr rawResponse
	if err := json.Unmarshal([]byte(text), &rr); err != nil {
		if m := jsonObjectRe.FindString(text); m != "" {
			if err2 := json.Unmarshal([]byte(m), &rr); err2 != nil {
				return failed(text), fmt.Errorf("json parse failed: %w", err2)
			}
		} else {
			return failed(text), fmt.Errorf("json parse failed: %w", err)
		}
	}

	res := Result{
		Part1:        rr.Part1,
		Part2:        rr.Part2,
		Part3:        rr.Part3,
		DoubleCheck:  rr.DoubleCheck,
		Summary:      rr.Summary,
		FinalVerdict: finalVerdict(rr),
	}
	if rr.CVSS40 != nil {
		res.CVSSVector = rr.CVSS40.Vector
		res.CVSSMetrics = rr.CVSS40.Metrics
	}
	return res, nil
}

// finalVerdict prefers double_check.final_verdict, then part1.verdict.
func finalVerdict(rr rawResponse) Verdict {
	if v := verdictFrom(rr.DoubleCheck, "final_verdict"); v != "" {
		return v
	}
	if v := verdictFrom(rr.Part1, "verdict"); v != "" {
		return v
	}
	return NeedsMoreContext
}

func verdictFrom(m map[string]any, key string) Verdict {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		switch Verdict(strings.ToUpper(strings.TrimSpace(s))) {
		case ConfirmedReal, LikelyReal, Unlikely, NotExploitable, NeedsMoreContext:
			return Verdict(strings.ToUpper(strings.TrimSpace(s)))
		}
	}
	return ""
}

func failed(text string) Result {
	snippet := text
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	return Result{FinalVerdict: NeedsMoreContext, Error: "unparseable response: " + snippet}
}
