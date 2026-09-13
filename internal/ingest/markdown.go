package ingest

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/seschis/concord/internal/finding"
)

var (
	findingSplitRe = regexp.MustCompile(`(?mi)^#{1,6}\s*FINDING\b.*$`)
	codeFenceRe    = regexp.MustCompile("(?s)```[a-zA-Z0-9]*\\n(.*?)```")
)

// parseMarkdown handles our scanner's "### FINDING N ###" block format and, when
// no such blocks exist, a GitHub-style findings table.
func parseMarkdown(data []byte) ([]finding.Finding, error) {
	text := string(data)
	locs := findingSplitRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return parseMarkdownTable(text), nil
	}

	var out []finding.Finding
	for i := range locs {
		start := locs[i][0]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := text[start:end]

		f := finding.Finding{
			ID:          fieldVal(block, "id", "finding id"),
			File:        fieldVal(block, "file", "path", "location"),
			VulnType:    fieldVal(block, "vulnerability type", "vuln type", "type", "rule", "title"),
			Severity:    cleanSeverity(fieldVal(block, "severity", "level", "priority")),
			CWE:         fieldVal(block, "cwe"),
			Description: fieldVal(block, "description", "message"),
			Fix:         fieldVal(block, "fix", "remediation", "suggestion"),
			SourceTool:  fieldVal(block, "detected by", "source tool", "tool", "scanner"),
		}
		if m := codeFenceRe.FindStringSubmatch(block); m != nil {
			f.Code = strings.TrimRight(m[1], "\n")
		}
		if ln := fieldVal(block, "line", "line number"); ln != "" {
			f.Line, _ = strconv.Atoi(strings.TrimSpace(ln))
		}
		if f.ID == "" {
			f.ID = makeID(i)
		}
		out = append(out, f)
	}
	return out, nil
}

// fieldVal finds the first "name: value" (also **name:** value or - name: value)
// for any of the given aliases, case-insensitively.
func fieldVal(block string, names ...string) string {
	for _, name := range names {
		re := regexp.MustCompile(`(?mi)^\s*(?:[-*]\s*)?\**` + regexp.QuoteMeta(name) + `\**\s*[:=]\s*(.+?)\s*$`)
		if m := re.FindStringSubmatch(block); m != nil {
			return strings.TrimSpace(strings.Trim(m[1], "`*"))
		}
	}
	return ""
}

// parseMarkdownTable reads the first pipe table with a recognizable header.
func parseMarkdownTable(text string) []finding.Finding {
	var out []finding.Finding
	lines := strings.Split(text, "\n")
	var header []string
	idx := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			header = nil
			continue
		}
		cells := splitTableRow(line)
		if header == nil {
			header = make([]string, len(cells))
			for i, c := range cells {
				header[i] = strings.ToLower(c)
			}
			continue
		}
		if isDividerRow(cells) {
			continue
		}
		m := map[string]any{}
		for i, c := range cells {
			if i < len(header) {
				m[header[i]] = c
			}
		}
		f := finding.Finding{
			ID:          firstString(m, "id"),
			File:        firstString(m, "file", "path", "location"),
			VulnType:    firstString(m, "vuln_type", "type", "rule", "title", "vulnerability"),
			Severity:    cleanSeverity(firstString(m, "severity", "level")),
			CWE:         firstString(m, "cwe"),
			Description: firstString(m, "description", "message"),
			SourceTool:  firstString(m, "tool", "scanner", "source_tool"),
			Raw:         m,
		}
		if f.ID == "" {
			f.ID = makeID(idx)
		}
		f.Line = firstInt(m, "line")
		out = append(out, f)
		idx++
	}
	return out
}

func splitTableRow(line string) []string {
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isDividerRow(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}
