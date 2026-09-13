package ingest

import (
	"fmt"
	"strconv"

	"github.com/seschis/concord/internal/finding"
)

// parseJSON handles our scanner format, a {"findings":[...]} object, or a bare
// array. Field names are matched flexibly.
func parseJSON(data []byte) ([]finding.Finding, error) {
	rows, err := asFindingSlice(data)
	if err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	out := make([]finding.Finding, 0, len(rows))
	for i, row := range rows {
		f := finding.Finding{
			ID:          firstString(row, "id", "ID", "finding_id"),
			File:        firstString(row, "file", "path", "filename", "location"),
			VulnType:    firstString(row, "vuln_type", "type", "rule", "ruleId", "category", "title"),
			Severity:    cleanSeverity(firstString(row, "severity", "level", "priority")),
			CWE:         firstString(row, "cwe", "CWE"),
			Description: firstString(row, "description", "message", "desc"),
			Code:        firstString(row, "code", "snippet", "code_context"),
			Fix:         firstString(row, "fix", "remediation", "suggestion"),
			SourceTool:  firstString(row, "source_tool", "tool", "scanner"),
			Raw:         row,
		}
		if f.ID == "" {
			f.ID = makeID(i)
		}
		f.Line = firstInt(row, "line", "line_number", "startLine")
		out = append(out, f)
	}
	return out, nil
}

// firstInt returns the first present integer among keys, tolerating numeric
// strings and JSON float64 decoding.
func firstInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case string:
			if parsed, err := strconv.Atoi(n); err == nil {
				return parsed
			}
		}
	}
	return 0
}
