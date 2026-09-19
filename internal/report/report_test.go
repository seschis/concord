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
	b, err := json.Marshal(Meta{Models: []ModelInfo{{Name: "claude", Model: "claude-opus-5"}}, Analysts: []string{"strict", "business"}})
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
	meta := Meta{Date: "2026-08-26", InputFile: "scan.sarif", Models: []ModelInfo{{Name: "claude", Model: "Claude"}, {Name: "gemini", Model: "Gemini"}}, ContextStrategy: "shared", Total: 1, TotalCostUSD: 0.012}
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

// voterRowLine returns the markdown table line containing marker, or "".
func voterRowLine(s, marker string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	return ""
}

// Data-driven voter rows: every configured voter gets a row, rendered in
// Meta.Models order (voter order), never in results-map iteration order. The
// results map is populated in deliberately adversarial order (custom model
// first) to prove the ordering comes from Meta.Models.
func TestMarkdownVoterRowsDataDriven(t *testing.T) {
	meta := Meta{
		Date:      "2026-01-01",
		InputFile: "x.sarif",
		Models: []ModelInfo{
			{Name: "claude", Model: "claude-opus-5", ContextWindow: 128000, Priced: true},
			{Name: "qwen", Model: "Qwen3.8-27B", ContextWindow: 262144, Priced: true},
		},
	}
	results := map[string]triage.Result{
		"qwen":   {Provider: "qwen", FinalVerdict: triage.Unlikely, Summary: "qwen note", CostUSD: 0.002},
		"claude": {Provider: "claude", FinalVerdict: triage.LikelyReal, Summary: "claude note", CostUSD: 0.01},
	}
	row := NewFindingResult(finding.Finding{ID: "F040", File: "a.go", VulnType: "SQLi", Severity: "HIGH"}, results, triage.LikelyReal, "majority", nil, 0)
	dir := t.TempDir()
	if _, err := WriteMarkdown(dir, meta, []FindingResult{row}); err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	s := string(md)
	for _, want := range []string{"| claude (voter) |", "| qwen (voter) |", "claude note", "qwen note"} {
		if !strings.Contains(s, want) {
			t.Fatalf("markdown missing %q:\n%s", want, s)
		}
	}
	if i, j := strings.Index(s, "| claude (voter) |"), strings.Index(s, "| qwen (voter) |"); i > j {
		t.Fatalf("voter rows must render in Meta.Models order (claude then qwen), not map order:\n%s", s)
	}
	// The header lists every configured model with its model id.
	if !strings.Contains(s, "claude (claude-opus-5)") || !strings.Contains(s, "qwen (Qwen3.8-27B)") {
		t.Fatalf("header must list all configured models with model ids:\n%s", s)
	}
}

// An unpriced model carries a visible marker on its row and is named in the
// header note; priced models carry neither.
func TestMarkdownUnpricedMarkerAndHeaderNote(t *testing.T) {
	meta := Meta{
		Date:      "2026-01-01",
		InputFile: "x.sarif",
		Models: []ModelInfo{
			{Name: "claude", Model: "claude-opus-5", ContextWindow: 128000, Priced: true},
			{Name: "qwen", Model: "Qwen3.8-27B", ContextWindow: 262144, Priced: false},
		},
	}
	results := map[string]triage.Result{
		"qwen":   {Provider: "qwen", FinalVerdict: triage.Unlikely, Summary: "local model", CostUSD: 0},
		"claude": {Provider: "claude", FinalVerdict: triage.LikelyReal, Summary: "real", CostUSD: 0.01},
	}
	row := NewFindingResult(finding.Finding{ID: "F041", File: "a.go", VulnType: "SQLi", Severity: "HIGH"}, results, triage.LikelyReal, "majority", nil, 0)
	dir := t.TempDir()
	if _, err := WriteMarkdown(dir, meta, []FindingResult{row}); err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	s := string(md)
	if !strings.Contains(s, "**Unpriced models:** qwen") {
		t.Fatalf("header note must name the unpriced model:\n%s", s)
	}
	qwenRow := voterRowLine(s, "qwen (voter)")
	claudeRow := voterRowLine(s, "claude (voter)")
	if qwenRow == "" || !strings.Contains(qwenRow, "unpriced") {
		t.Fatalf("qwen row must carry the unpriced marker: %q", qwenRow)
	}
	if strings.Contains(claudeRow, "unpriced") {
		t.Fatalf("priced claude row must not carry the marker: %q", claudeRow)
	}
	// The unpriced model still costs $0 and shows it.
	if !strings.Contains(qwenRow, "$0.0000") {
		t.Fatalf("unpriced row must show a $0 cost: %q", qwenRow)
	}
}

// A spec-priced model with an explicit $0 price is Priced=true: it costs $0
// but must NOT carry the unpriced marker, and no unpriced header note appears.
func TestExplicitZeroPricePricedNoMarker(t *testing.T) {
	meta := Meta{
		Date:      "2026-01-01",
		InputFile: "x.sarif",
		Models:    []ModelInfo{{Name: "claude", Model: "claude-opus-5", ContextWindow: 128000, Priced: true}},
	}
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.LikelyReal, Summary: "real", CostUSD: 0},
	}
	row := NewFindingResult(finding.Finding{ID: "F042", File: "a.go", VulnType: "SQLi", Severity: "HIGH"}, results, triage.LikelyReal, "unanimous", nil, 0)
	dir := t.TempDir()
	if _, err := WriteMarkdown(dir, meta, []FindingResult{row}); err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	s := string(md)
	claudeRow := voterRowLine(s, "claude (voter)")
	if claudeRow == "" {
		t.Fatalf("claude row missing:\n%s", s)
	}
	if strings.Contains(claudeRow, "unpriced") {
		t.Fatalf("explicitly priced model (even at $0) must not carry the marker: %q", claudeRow)
	}
	if strings.Contains(s, "**Unpriced models:**") {
		t.Fatalf("no unpriced header note when every model is priced:\n%s", s)
	}
	if !strings.Contains(claudeRow, "$0.0000") {
		t.Fatalf("$0 price must still render as a cost: %q", claudeRow)
	}
}

// metadata.models serializes as a structured per-model list (R16): name, model
// id, context window, and priced flag for every configured voter, in order.
func TestMetaModelsStructuredJSON(t *testing.T) {
	meta := Meta{
		Date:      "2026-01-01",
		InputFile: "x.sarif",
		Models: []ModelInfo{
			{Name: "qwen", Model: "Qwen3.8-27B", ContextWindow: 262144, Priced: false},
			{Name: "claude", Model: "claude-opus-5", ContextWindow: 128000, Priced: true},
		},
	}
	dir := t.TempDir()
	jp, err := WriteJSON(dir, meta, []FindingResult{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(jp)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Metadata struct {
			Models []struct {
				Name          string `json:"name"`
				Model         string `json:"model"`
				ContextWindow int    `json:"context_window"`
				Priced        bool   `json:"priced"`
			} `json:"models"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("results.json is not valid JSON: %v", err)
	}
	if len(payload.Metadata.Models) != 2 {
		t.Fatalf("metadata.models must be a two-element array, got %d: %s", len(payload.Metadata.Models), raw)
	}
	q := payload.Metadata.Models[0]
	if q.Name != "qwen" || q.Model != "Qwen3.8-27B" || q.ContextWindow != 262144 || q.Priced {
		t.Fatalf("metadata.models[0] wrong: %+v", q)
	}
	c := payload.Metadata.Models[1]
	if c.Name != "claude" || c.Model != "claude-opus-5" || c.ContextWindow != 128000 || !c.Priced {
		t.Fatalf("metadata.models[1] wrong: %+v", c)
	}
	// priced:false must serialize explicitly (no omitempty regression).
	if !strings.Contains(string(raw), `"priced": false`) {
		t.Fatalf("priced:false must serialize explicitly: %s", raw)
	}
}

// The explorer row renders whenever a gatherer ran — including an unpriced
// gatherer at $0 — and not when no gatherer ran. A shared strategy with a
// srcroot always gathers; per-model and shared-without-srcroot never do.
func TestExplorerRowRendersWhenGathererRan(t *testing.T) {
	f := finding.Finding{ID: "F050", File: "a.go", VulnType: "SQLi", Severity: "HIGH"}
	results := map[string]triage.Result{
		"claude": {Provider: "claude", FinalVerdict: triage.LikelyReal, CostUSD: 0.01},
	}
	zeroRow := NewFindingResult(f, results, triage.LikelyReal, "majority", nil, 0)
	costRow := NewFindingResult(f, results, triage.LikelyReal, "majority", nil, 0.005)

	cases := []struct {
		name string
		meta Meta
		row  FindingResult
		want bool
	}{
		{"unpriced gatherer: shared + srcroot, $0 cost", Meta{ContextStrategy: "shared", SrcRoot: "/repo"}, zeroRow, true},
		{"priced gatherer: shared + srcroot, nonzero cost", Meta{ContextStrategy: "shared", SrcRoot: "/repo"}, costRow, true},
		{"per-model: no shared gatherer, $0 cost", Meta{ContextStrategy: "per-model", SrcRoot: "/repo"}, zeroRow, false},
		{"shared without srcroot: no gatherer, $0 cost", Meta{ContextStrategy: "shared"}, zeroRow, false},
	}
	const wantRow = "| explorer |"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteMarkdown(dir, tc.meta, []FindingResult{tc.row}); err != nil {
				t.Fatal(err)
			}
			md, _ := os.ReadFile(filepath.Join(dir, "report.md"))
			if got := strings.Contains(string(md), wantRow); got != tc.want {
				t.Fatalf("explorer row rendered = %v, want %v:\n%s", got, tc.want, md)
			}
		})
	}
}
