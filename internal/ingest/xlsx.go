package ingest

import (
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/seschis/concord/internal/finding"
)

// parseXLSX reads the first worksheet, treating the first row as a header and
// mapping columns to finding fields by flexible name matching.
func parseXLSX(path string) ([]finding.Finding, error) {
	fh, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open xlsx: %w", err)
	}
	defer fh.Close()

	sheets := fh.GetSheetList()
	if len(sheets) == 0 {
		return nil, nil
	}
	rows, err := fh.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("read xlsx: %w", err)
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
			File:        firstString(m, "file", "path", "filename", "location"),
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
