// Package report writes the JSON results file and the Markdown report. Each
// finding's JSON entry carries a concordAnalysis block with the verdict's
// classification, justification, and remediation detail.
package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/seschis/concord/internal/cvss"
	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/triage"
)

// Meta is the run-level header.
type Meta struct {
	Date            string   `json:"date"`
	InputFile       string   `json:"input_file"`
	SrcRoot         string   `json:"srcroot,omitempty"`
	ContextStrategy string   `json:"context_strategy"`
	ContextDirs     []string `json:"context_dirs,omitempty"`
	ContextGuides   []string `json:"context_guides,omitempty"`
	Effort          string   `json:"effort,omitempty"`
	Models          []string `json:"models"`
	Total           int      `json:"total"`
	TotalCostUSD    float64  `json:"total_cost_usd"`
}

// ConcordAnalysis is the per-finding verdict block embedded in results.json
// under the "concordAnalysis" key.
type ConcordAnalysis struct {
	Classification string `json:"classification"`
	Justification  string `json:"justification"`
	WorkDetail     string `json:"workDetail"`
	SchemaVersion  string `json:"schemaVersion"`
}

// CVSS40 holds the triage-assigned CVSS 4.0 vector and its computed score.
type CVSS40 struct {
	Vector   string            `json:"vector"`
	Score    float64           `json:"score"`
	Severity string            `json:"severity"`
	Metrics  map[string]string `json:"metrics,omitempty"`
}

// CVSSParseError records a model-emitted vector that failed validation. It is
// set only when the primary result carried a non-empty vector that cvss could
// not parse, so a junk or partial vector is surfaced rather than silently
// dropped.
type CVSSParseError struct {
	Vector string `json:"vector"`
	Error  string `json:"error"`
}

// FindingResult is one finding plus every model's analysis and the derived
// classification.
type FindingResult struct {
	ID              string                     `json:"id"`
	File            string                     `json:"file"`
	Line            int                        `json:"line,omitempty"`
	Type            string                     `json:"type"`
	Severity        string                     `json:"severity"`
	CWE             string                     `json:"cwe,omitempty"`
	Description     string                     `json:"description"`
	FinalVerdict    string                     `json:"final_verdict"`
	Agreement       string                     `json:"agreement"`
	CVSS            *CVSS40                    `json:"cvss40,omitempty"`
	CVSSError       *CVSSParseError            `json:"cvss40_error,omitempty"`
	ExplorerCostUSD float64                    `json:"explorer_cost_usd,omitempty"`
	Results         map[string]triage.Result   `json:"results"`
	Adjudication    *triage.AdjudicationResult `json:"adjudication,omitempty"`
	ConcordAnalysis ConcordAnalysis            `json:"concordAnalysis"`
}

// NewFindingResult assembles a result row from every model's analysis, the voted
// final verdict, and (when the vote was a tie) the adjudication. The
// concordAnalysis block derives from the adjudication if present,
// otherwise from the result matching the final verdict.
func NewFindingResult(f finding.Finding, results map[string]triage.Result, final triage.Verdict, agreement string, adj *triage.AdjudicationResult, explorerCost float64) FindingResult {
	primary := pickPrimary(results, final)

	just := ""
	work := ""
	if adj != nil {
		just = adj.Reasoning
		work = adj.KeyDecidingFactor
	}
	if just == "" {
		just = justification(primary)
	}
	if work == "" {
		work = workDetail(primary)
	}

	fr := FindingResult{
		ID:              f.ID,
		File:            f.File,
		Line:            f.Line,
		Type:            f.VulnType,
		Severity:        f.Severity,
		CWE:             f.CWE,
		Description:     f.Description,
		FinalVerdict:    string(final),
		Agreement:       agreement,
		ExplorerCostUSD: explorerCost,
		Results:         results,
		Adjudication:    adj,
		ConcordAnalysis: ConcordAnalysis{
			Classification: triage.Classification(final),
			Justification:  just,
			WorkDetail:     work,
			SchemaVersion:  "1.0.0",
		},
	}
	if primary.CVSSVector != "" {
		if v, err := cvss.ParseVector(primary.CVSSVector); err == nil {
			fr.CVSS = &CVSS40{
				Vector:   v.String(),
				Score:    v.Score(),
				Severity: v.Severity(),
				Metrics:  primary.CVSSMetrics,
			}
		} else {
			fr.CVSSError = &CVSSParseError{
				Vector: primary.CVSSVector,
				Error:  err.Error(),
			}
		}
	}
	return fr
}

// pickPrimary chooses the result whose verdict matches the final vote, else the
// first non-errored result, else any result.
func pickPrimary(results map[string]triage.Result, final triage.Verdict) triage.Result {
	var firstOK, any triage.Result
	haveOK, haveAny := false, false
	for _, r := range results {
		if !haveAny {
			any, haveAny = r, true
		}
		if r.Error == "" && !haveOK {
			firstOK, haveOK = r, true
		}
		if r.FinalVerdict == final && r.Error == "" {
			return r
		}
	}
	if haveOK {
		return firstOK
	}
	return any
}

func justification(r triage.Result) string {
	if r.Summary != "" {
		return r.Summary
	}
	if s := stringField(r.Part1, "reasoning"); s != "" {
		return s
	}
	if r.Error != "" {
		return "triage error: " + r.Error
	}
	return ""
}

func workDetail(r triage.Result) string {
	if s := stringField(r.DoubleCheck, "revision_reasoning"); s != "" {
		return s
	}
	if s := stringField(r.Part3, "recommendation"); s != "" {
		return s
	}
	return stringField(r.Part1, "data_flow_trace")
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

// WriteJSON writes results.json into dir and returns its path.
func WriteJSON(dir string, meta Meta, results []FindingResult) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	payload := struct {
		Metadata Meta            `json:"metadata"`
		Results  []FindingResult `json:"results"`
	}{meta, results}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "results.json")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// WriteFindings writes the normalized findings (used by --dry-run) and returns
// its path.
func WriteFindings(dir string, findings []finding.Finding) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "findings.json")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return "", err
	}
	return out, nil
}
