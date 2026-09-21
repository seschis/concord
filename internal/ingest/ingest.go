// Package ingest loads scanner output into normalized findings. Phase 1 handles
// SARIF and JSON; CSV, Markdown, and XLSX arrive in phase 4.
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seschis/harmonia/internal/finding"
)

// Load reads a scanner file and returns normalized findings. Format is chosen by
// extension, with a content sniff so a .json file that is really SARIF is routed
// correctly.
func Load(path string) ([]finding.Finding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// Strip a leading UTF-8 BOM (EF BB BF). Excel and several scanner UIs
	// export CSVs with one, which otherwise lands inside the first field and
	// breaks the CSV reader, the JSON decoder, and the SARIF content sniff.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".sarif":
		return parseSARIF(data)
	case ".json":
		if looksLikeSARIF(data) {
			return parseSARIF(data)
		}
		return parseJSON(data)
	case ".csv":
		return parseCSV(data)
	case ".md", ".markdown":
		return parseMarkdown(data)
	case ".xlsx":
		return parseXLSX(path)
	default:
		return nil, fmt.Errorf("unsupported format %q (supported: .sarif .json .csv .md .xlsx)", ext)
	}
}

func looksLikeSARIF(data []byte) bool {
	return bytes.Contains(data, []byte(`"$schema"`)) && bytes.Contains(data, []byte("sarif")) ||
		bytes.Contains(data, []byte(`"runs"`)) && bytes.Contains(data, []byte(`"tool"`))
}

func cleanSeverity(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	switch s {
	case "", "NONE", "NOTE":
		return "INFO"
	case "WARNING":
		return "MEDIUM"
	case "ERROR":
		return "HIGH"
	}
	return s
}

func makeID(i int) string { return fmt.Sprintf("F%03d", i+1) }

// firstString returns the first present, non-empty string value among keys.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// asFindingSlice decodes a JSON document that is either {"findings":[...]} or a
// bare array of finding objects.
func asFindingSlice(data []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var arr []map[string]any
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}
	var obj struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, err
	}
	return obj.Findings, nil
}
