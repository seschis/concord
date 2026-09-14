package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/triage"
)

func sampleRow() FindingResult {
	f := finding.Finding{ID: "F001", File: "app/db.go", Line: 42, VulnType: "SQL Injection", Severity: "HIGH", CWE: "CWE-89"}
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.LikelyReal, Summary: "raw SQL from request", CostUSD: 0.01},
		"gemini": {Provider: "gemini", FinalVerdict: triage.ConfirmedReal, Summary: "confirmed sink", CostUSD: 0.002},
	}
	return NewFindingResult(f, results, triage.LikelyReal, "majority", nil, 0)
}

func TestConcordAnalysisMapping(t *testing.T) {
	row := sampleRow()
	if row.ConcordAnalysis.Classification != "TRUE_POSITIVE" {
		t.Fatalf("LIKELY_REAL should map to TRUE_POSITIVE, got %s", row.ConcordAnalysis.Classification)
	}
	if row.ConcordAnalysis.SchemaVersion != "1.0.0" {
		t.Fatalf("missing schema version")
	}
	if row.ConcordAnalysis.Justification == "" {
		t.Fatalf("justification should fall back to a model summary")
	}
}

func TestAdjudicationDrivesClassification(t *testing.T) {
	f := finding.Finding{ID: "F002", File: "x.go", VulnType: "XSS", Severity: "LOW"}
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.ConfirmedReal},
		"gemini": {Provider: "gemini", FinalVerdict: triage.NotExploitable},
	}
	adj := &triage.AdjudicationResult{FinalVerdict: triage.NotExploitable, Reasoning: "framework auto-escapes", KeyDecidingFactor: "template autoescape"}
	adjudications := []triage.AdjudicationResult{*adj}
	row := NewFindingResult(f, results, triage.NotExploitable, "none", adjudications, 0)
	if row.ConcordAnalysis.Classification != "FALSE_POSITIVE" {
		t.Fatalf("want FALSE_POSITIVE, got %s", row.ConcordAnalysis.Classification)
	}
	if row.ConcordAnalysis.Justification != "framework auto-escapes" {
		t.Fatalf("justification should come from adjudication, got %q", row.ConcordAnalysis.Justification)
	}
}

func TestAdjudicationsPanelRecorded(t *testing.T) {
	f := finding.Finding{ID: "F003", File: "y.go", VulnType: "XSS", Severity: "MEDIUM"}
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.LikelyReal, Summary: "voter says exploitable"},
	}
	adjudications := []triage.AdjudicationResult{
		{Judge: "strict", Model: "claude-opus", FinalVerdict: triage.NotExploitable, Reasoning: "strict says no taint"},
		{Judge: "business", Model: "gemini", FinalVerdict: triage.LikelyReal, Reasoning: "business says exploitable"},
	}
	row := NewFindingResult(f, results, triage.NotExploitable, "none", adjudications, 0)
	if len(row.Adjudications) != 2 {
		t.Fatalf("want 2 adjudications recorded, got %d", len(row.Adjudications))
	}
	if row.ConcordAnalysis.Justification != "strict says no taint" {
		t.Fatalf("justification should come from the judge matching the final verdict, got %q", row.ConcordAnalysis.Justification)
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "adjudications") {
		t.Fatalf("marshalled row should contain adjudications, got %s", string(b))
	}
}

func TestMetaJudgesMarshal(t *testing.T) {
	m := Meta{Judges: []string{"strict", "business"}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"judges"`) {
		t.Fatalf("Meta should marshal judges, got %s", string(b))
	}
}

func TestMarkdownSurfacesJudgeError(t *testing.T) {
	f := finding.Finding{ID: "F020", File: "z.go", VulnType: "XSS", Severity: "LOW"}
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.ConfirmedReal},
		"gemini": {Provider: "gemini", FinalVerdict: triage.Unlikely},
	}
	adjudications := []triage.AdjudicationResult{
		{Judge: "strict", Model: "claude", FinalVerdict: triage.Unlikely, Reasoning: "no taint"},
		{Judge: "failing", Model: "claude", FinalVerdict: triage.NeedsMoreContext, Error: "boom"},
	}
	row := NewFindingResult(f, results, triage.Unlikely, "none", adjudications, 0)
	dir := t.TempDir()
	if _, err := WriteMarkdown(dir, Meta{Date: "2026-01-01", InputFile: "x.sarif"}, []FindingResult{row}); err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	s := string(md)
	for _, want := range []string{"Judge strict", "Judge failing", "error: boom", "no taint"} {
		if !strings.Contains(s, want) {
			t.Fatalf("markdown missing %q:\n%s", want, s)
		}
	}
}

// An analyst lens must appear both in the report header and as its own "(analyst)"
// row in the per-finding verdict table, distinct from the model voters.
func TestMarkdownAnalystTable(t *testing.T) {
	f := finding.Finding{ID: "F030", File: "a.go", VulnType: "SQLi", Severity: "HIGH"}
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.LikelyReal, Summary: "claude sees it real"},
		"strict": {Provider: "strict", FinalVerdict: triage.NotExploitable, Summary: "strict disagrees"},
	}
	row := NewFindingResult(f, results, triage.LikelyReal, "majority", nil, 0)
	dir := t.TempDir()
	meta := Meta{Date: "2026-01-01", InputFile: "x.sarif", Analysts: []string{"strict"}}
	if _, err := WriteMarkdown(dir, meta, []FindingResult{row}); err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	s := string(md)
	for _, want := range []string{"**Analysts:** strict", "claude (voter)", "strict (analyst)", "strict disagrees"} {
		if !strings.Contains(s, want) {
			t.Fatalf("markdown missing %q:\n%s", want, s)
		}
	}
	// A run with no analysts must not render an empty Analysts header.
	if strings.Contains(s, "**Analysts:**\n") {
		t.Fatalf("should not render an empty Analysts header")
	}
}

// Meta.Analysts marshals like Meta.Judges.
func TestMetaAnalystsMarshal(t *testing.T) {
	b, err := json.Marshal(Meta{Models: []string{"Claude (claude-opus-5)"}, Analysts: []string{"strict", "business"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"analysts"`) {
		t.Fatalf("Meta should marshal analysts, got %s", string(b))
	}
}

func TestCVSSValidVectorScored(t *testing.T) {
	f := finding.Finding{ID: "F010", File: "x.go", VulnType: "SQLi", Severity: "HIGH"}
	vec := "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:N/SC:N/SI:N/SA:N"
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.ConfirmedReal, CVSSVector: vec},
	}
	row := NewFindingResult(f, results, triage.ConfirmedReal, "unanimous", nil, 0)
	if row.CVSS == nil {
		t.Fatalf("valid vector should produce a scored CVSS40 block")
	}
	if row.CVSSError != nil {
		t.Fatalf("valid vector should not set CVSSError, got %+v", row.CVSSError)
	}
	if row.CVSS.Score <= 0 {
		t.Fatalf("expected a positive computed score, got %v", row.CVSS.Score)
	}
}

func TestCVSSInvalidVectorSurfacedNotDropped(t *testing.T) {
	f := finding.Finding{ID: "F011", File: "x.go", VulnType: "SQLi", Severity: "HIGH"}
	// Missing several base metrics — ParseVector must reject it.
	bad := "CVSS:4.0/AV:N/AC:L/PR:N"
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.ConfirmedReal, CVSSVector: bad},
	}
	row := NewFindingResult(f, results, triage.ConfirmedReal, "unanimous", nil, 0)
	if row.CVSS != nil {
		t.Fatalf("invalid vector must not produce a scored CVSS40 block, got %+v", row.CVSS)
	}
	if row.CVSSError == nil {
		t.Fatalf("invalid vector must be surfaced via CVSSError, not silently dropped")
	}
	if row.CVSSError.Vector != bad {
		t.Fatalf("CVSSError should carry the offending vector, got %q", row.CVSSError.Vector)
	}
	if row.CVSSError.Error == "" {
		t.Fatalf("CVSSError should carry the parse error message")
	}
}

func TestWriteJSONAndMarkdown(t *testing.T) {
	dir := t.TempDir()
	meta := Meta{Date: "2026-08-26", InputFile: "scan.sarif", Models: []string{"Claude", "Gemini"}, ContextStrategy: "shared", Total: 1, TotalCostUSD: 0.012}
	rows := []FindingResult{sampleRow()}

	jp, err := WriteJSON(dir, meta, rows)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(jp)
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("results.json is not valid JSON: %v", err)
	}
	if !strings.Contains(string(raw), "concordAnalysis") {
		t.Fatalf("results.json missing concordAnalysis")
	}

	mp, err := WriteMarkdown(dir, meta, rows)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(mp)
	for _, want := range []string{"# Security Scan Triage Report", "F001", "SQL Injection", "TRUE_POSITIVE", "claude", "gemini"} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("report.md missing %q", want)
		}
	}
	if filepath.Base(mp) != "report.md" {
		t.Fatalf("unexpected report name %s", mp)
	}
}
