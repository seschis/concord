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
	row := NewFindingResult(f, results, triage.NotExploitable, "none", adj, 0)
	if row.ConcordAnalysis.Classification != "FALSE_POSITIVE" {
		t.Fatalf("want FALSE_POSITIVE, got %s", row.ConcordAnalysis.Classification)
	}
	if row.ConcordAnalysis.Justification != "framework auto-escapes" {
		t.Fatalf("justification should come from adjudication, got %q", row.ConcordAnalysis.Justification)
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
