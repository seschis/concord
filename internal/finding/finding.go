// Package finding defines the normalized finding shape that every ingest
// format is mapped into and that every provider triages.
package finding

// Finding is a scanner result normalized across all supported input formats.
type Finding struct {
	ID          string         `json:"id"`
	File        string         `json:"file"`
	Line        int            `json:"line,omitempty"`
	VulnType    string         `json:"vuln_type"`
	Severity    string         `json:"severity"`
	CWE         string         `json:"cwe,omitempty"`
	Description string         `json:"description"`
	Code        string         `json:"code,omitempty"`
	Fix         string         `json:"fix,omitempty"`
	SourceTool  string         `json:"source_tool,omitempty"`
	Raw         map[string]any `json:"-"`
}
