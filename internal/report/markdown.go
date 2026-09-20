package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seschis/concord/internal/triage"
)

// WriteMarkdown writes a human-readable report into dir and returns its path.
func WriteMarkdown(dir string, meta Meta, results []FindingResult) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var b strings.Builder

	b.WriteString("# Security Scan Triage Report\n\n")
	fmt.Fprintf(&b, "**Date:** %s\n\n", meta.Date)
	fmt.Fprintf(&b, "**Input file:** `%s`\n\n", meta.InputFile)
	fmt.Fprintf(&b, "**Models:** %s\n\n", strings.Join(DisplayNames(meta.Models), ", "))
	if up := UnpricedNames(meta.Models); len(up) > 0 {
		fmt.Fprintf(&b, "**Unpriced models:** %s (no price configured; costed at $0)\n\n", strings.Join(up, ", "))
	}
	if len(meta.Analysts) > 0 {
		fmt.Fprintf(&b, "**Analysts:** %s\n\n", strings.Join(meta.Analysts, ", "))
	}
	fmt.Fprintf(&b, "**Context strategy:** %s", meta.ContextStrategy)
	if meta.SrcRoot != "" {
		fmt.Fprintf(&b, " (srcroot `%s`)", meta.SrcRoot)
	}
	b.WriteString("\n\n")
	if meta.Effort != "" {
		fmt.Fprintf(&b, "**Effort:** %s\n\n", meta.Effort)
	}
	if len(meta.ContextDirs) > 0 {
		fmt.Fprintf(&b, "**Context dirs:** %s\n\n", strings.Join(meta.ContextDirs, ", "))
	}
	if len(meta.ContextGuides) > 0 {
		fmt.Fprintf(&b, "**Architecture guides found:** %s\n\n", strings.Join(meta.ContextGuides, ", "))
	} else if len(meta.ContextDirs) > 0 {
		b.WriteString("**Architecture guides found:** none (no `--context-guide` and no `TRIAGE_CONTEXT.md` at a context-dir root)\n\n")
	}
	fmt.Fprintf(&b, "**Findings analysed:** %d\n\n", meta.Total)
	fmt.Fprintf(&b, "**Total cost:** $%.4f\n\n", meta.TotalCostUSD)

	// Verdict tally.
	counts := map[string]int{}
	for _, r := range results {
		counts[r.FinalVerdict]++
	}
	b.WriteString("## Summary\n\n")
	for _, v := range []triage.Verdict{triage.ConfirmedReal, triage.LikelyReal, triage.Unlikely, triage.NotExploitable, triage.NeedsMoreContext} {
		fmt.Fprintf(&b, "- %s %s: %d\n", verdictIcon(v), v, counts[string(v)])
	}
	b.WriteString("\n---\n\n")

	// Meta-derived voter state, computed once for every finding.
	voterNames := make([]string, 0, len(meta.Models))
	for _, mi := range meta.Models {
		voterNames = append(voterNames, mi.Name)
	}
	priced := make(map[string]bool, len(meta.Models))
	for _, mi := range meta.Models {
		priced[mi.Name] = mi.Priced
	}
	sharedGatherer := meta.ContextStrategy == "shared" && meta.SrcRoot != ""

	// Per-finding detail.
	for _, r := range results {
		primary := pickPrimary(r.Results, triage.Verdict(r.FinalVerdict))
		mitigations := stringSliceField(primary.Part1, "mitigations_found")
		recharacterized := len(mitigations) > 0 && isReal(triage.Verdict(r.FinalVerdict))

		heading := r.Type
		if recharacterized {
			heading += " (recharacterized)"
		}
		fmt.Fprintf(&b, "## %s %s — %s\n\n", verdictIcon(triage.Verdict(r.FinalVerdict)), r.ID, heading)
		fmt.Fprintf(&b, "**File:** `%s`", r.File)
		if r.Line > 0 {
			fmt.Fprintf(&b, " line %d", r.Line)
		}
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "**Severity:** %s", r.Severity)
		if r.CWE != "" {
			fmt.Fprintf(&b, "  |  **CWE:** %s", r.CWE)
		}
		if r.CVSS != nil {
			fmt.Fprintf(&b, "  |  **CVSS 4.0:** %.1f %s", r.CVSS.Score, r.CVSS.Severity)
		}
		b.WriteString("\n\n")
		if r.CVSS != nil {
			fmt.Fprintf(&b, "**CVSS vector:** `%s`\n\n", r.CVSS.Vector)
		} else if r.CVSSError != nil {
			fmt.Fprintf(&b, "> **CVSS vector dropped (invalid):** `%s` — %s\n\n", r.CVSSError.Vector, r.CVSSError.Error)
		}
		fmt.Fprintf(&b, "**Final verdict:** %s  (agreement: %s)\n\n", r.FinalVerdict, r.Agreement)
		fmt.Fprintf(&b, "**Classification:** %s\n\n", r.ConcordAnalysis.Classification)

		if recharacterized {
			b.WriteString("> **Recharacterized.** The scanner's original description contained claims ")
			b.WriteString("that the triage contradicted after examining surrounding architecture. ")
			b.WriteString("The vulnerability is real but narrower than originally reported.\n\n")

			b.WriteString("**Original claims corrected:**\n\n")
			for _, m := range mitigations {
				fmt.Fprintf(&b, "- %s\n", m)
			}
			b.WriteString("\n")

			if primary.Summary != "" {
				fmt.Fprintf(&b, "**Actual vulnerability:** %s\n\n", primary.Summary)
			}
		} else if primary.Summary != "" {
			fmt.Fprintf(&b, "%s\n\n", primary.Summary)
		} else if r.ConcordAnalysis.Justification != "" {
			fmt.Fprintf(&b, "%s\n\n", r.ConcordAnalysis.Justification)
		}

		// Analysis section with the triage's reasoning chain.
		writeAnalysis(&b, primary, r.CVSS)

		// Per-model verdicts and cost breakdown.
		b.WriteString("| Role | Verdict | Cost | Notes |\n|---|---|---|---|\n")
		// The explorer row renders whenever a gatherer ran — including an
		// unpriced gatherer at $0 — and shows the error when the gather
		// failed, so a run triaged on metadata alone never claims context
		// was gathered.
		if r.ExplorerError != "" {
			fmt.Fprintf(&b, "| explorer | — | $%.4f | failed: %s |\n", r.ExplorerCostUSD, mdCell(r.ExplorerError))
		} else if r.ExplorerCostUSD > 0 || sharedGatherer {
			fmt.Fprintf(&b, "| explorer | — | $%.4f | shared context gathering |\n", r.ExplorerCostUSD)
		}
		for _, name := range voterNames {
			mr, ok := r.Results[name]
			if !ok {
				continue
			}
			note := mr.Summary
			if mr.Error != "" {
				note = "error: " + mr.Error
			}
			marker := ""
			if !priced[name] {
				marker = " (unpriced)"
			}
			fmt.Fprintf(&b, "| %s (voter) | %s | $%.4f%s | %s |\n", name, mr.FinalVerdict, mr.CostUSD, marker, mdCell(note))
		}
		// Analyst lenses (persona voters on the preferred model), if any.
		for _, name := range meta.Analysts {
			mr, ok := r.Results[name]
			if !ok {
				continue
			}
			note := mr.Summary
			if mr.Error != "" {
				note = "error: " + mr.Error
			}
			fmt.Fprintf(&b, "| %s (analyst) | %s | $%.4f | %s |\n", name, mr.FinalVerdict, mr.CostUSD, mdCell(note))
		}
		b.WriteString("\n")

		for _, j := range r.Adjudications {
			fmt.Fprintf(&b, "**Judge %s (%s):** verdict %s", j.Judge, j.Model, j.FinalVerdict)
			switch {
			case j.Error != "":
				fmt.Fprintf(&b, " — _error: %s_", j.Error)
			case j.Reasoning != "":
				fmt.Fprintf(&b, " — %s", j.Reasoning)
			}
			b.WriteString("\n\n")
			if j.KeyDecidingFactor != "" {
				fmt.Fprintf(&b, "_Key deciding factor:_ %s\n\n", j.KeyDecidingFactor)
			}
		}
		b.WriteString("---\n\n")
	}

	out := filepath.Join(dir, "report.md")
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// writeAnalysis renders the triage's reasoning chain under a ### Analysis heading.
func writeAnalysis(b *strings.Builder, r triage.Result, c *CVSS40) {
	scenario := stringField(r.Part1, "exploit_scenario")
	reasoning := stringField(r.Part1, "reasoning")
	challenge := stringField(r.DoubleCheck, "challenge")
	revision := stringField(r.DoubleCheck, "revision_reasoning")
	recommendation := stringField(r.Part3, "recommendation")
	priority := stringField(r.Part3, "priority")

	if scenario == "" && reasoning == "" && challenge == "" && recommendation == "" && c == nil {
		return
	}

	b.WriteString("### Analysis\n\n")

	if c != nil && len(c.Metrics) > 0 {
		b.WriteString("**CVSS 4.0 metric justifications:**\n\n")
		b.WriteString("| Metric | Value | Justification |\n|---|---|---|\n")
		for _, m := range cvssMetricOrder {
			if just, ok := c.Metrics[m]; ok {
				fmt.Fprintf(b, "| %s | %s | %s |\n", m, metricValueFromVector(c.Vector, m), mdCell(just))
			}
		}
		b.WriteString("\n")
	}

	if scenario != "" {
		fmt.Fprintf(b, "**Exploit scenario:**\n\n%s\n\n", scenario)
	}
	if reasoning != "" {
		fmt.Fprintf(b, "**Triage reasoning:**\n\n%s\n\n", reasoning)
	}
	if challenge != "" {
		fmt.Fprintf(b, "**Counter-argument considered:**\n\n%s\n\n", challenge)
	}
	if revision != "" {
		fmt.Fprintf(b, "**Why the counter-argument fails:**\n\n%s\n\n", revision)
	}
	if recommendation != "" {
		label := "Recommended fix"
		if priority != "" {
			label = fmt.Sprintf("Recommended fix (%s)", priority)
		}
		fmt.Fprintf(b, "**%s:**\n\n%s\n\n", label, recommendation)
	}
}

var cvssMetricOrder = []string{"AV", "AC", "AT", "PR", "UI", "VC", "VI", "VA", "SC", "SI", "SA"}

func metricValueFromVector(vector, metric string) string {
	prefix := metric + ":"
	idx := strings.Index(vector, prefix)
	if idx < 0 {
		return "?"
	}
	rest := vector[idx+len(prefix):]
	if end := strings.IndexByte(rest, '/'); end >= 0 {
		return rest[:end]
	}
	return rest
}

func isReal(v triage.Verdict) bool {
	return v == triage.ConfirmedReal || v == triage.LikelyReal
}

func verdictIcon(v triage.Verdict) string {
	switch v {
	case triage.ConfirmedReal:
		return "🔴"
	case triage.LikelyReal:
		return "🟠"
	case triage.Unlikely:
		return "🟡"
	case triage.NotExploitable:
		return "🟢"
	default:
		return "🔵"
	}
}

// mdCell keeps a value on one table line.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// stringSliceField extracts a []string from a map[string]any value that is
// stored as []any (the standard shape after JSON round-tripping).
func stringSliceField(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
