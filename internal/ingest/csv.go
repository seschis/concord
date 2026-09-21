package ingest

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/seschis/harmonia/internal/finding"
)

// parseCSV reads a CSV whose first row is a header, mapping columns to finding
// fields by flexible name matching.
func parseCSV(data []byte) ([]finding.Finding, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse csv: %w", err)
	}
	if len(rows) < 2 {
		return nil, nil
	}

	header := make([]string, len(rows[0]))
	for i, h := range rows[0] {
		header[i] = strings.ToLower(strings.TrimSpace(h))
	}

	var out []finding.Finding
	for i, row := range rows[1:] {
		m := map[string]any{}
		for j, cell := range row {
			if j < len(header) {
				m[header[j]] = cell
			}
		}
		f := finding.Finding{
			ID:          firstString(m, "id", "finding_id"),
			File:        firstString(m, "file", "file path", "filepath", "path", "filename", "location"),
			VulnType:    firstString(m, "vuln_type", "type", "rule", "category", "title", "vulnerability"),
			Severity:    cleanSeverity(firstString(m, "severity", "level", "priority")),
			CWE:         firstString(m, "cwe"),
			Description: firstString(m, "description", "message", "desc"),
			Code:        firstString(m, "code", "snippet"),
			Fix:         firstString(m, "fix", "remediation", "suggestion"),
			SourceTool:  firstString(m, "source_tool", "tool", "scanner"),
			Raw:         m,
		}
		if f.ID == "" {
			f.ID = makeID(i)
		}
		f.Line = firstInt(m, "line", "line_number", "startline")
		out = append(out, f)
	}
	return out, nil
}
