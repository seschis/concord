package ingest

import (
	"encoding/json"
	"fmt"

	"github.com/seschis/concord/internal/finding"
)

// Minimal SARIF 2.1.0 subset, enough to extract findings from CodeQL, Semgrep,
// Checkov, and GitHub Advanced Security output.
type sarifDoc struct {
	Runs []struct {
		Tool struct {
			Driver struct {
				Name  string `json:"name"`
				Rules []struct {
					ID               string `json:"id"`
					Name             string `json:"name"`
					ShortDescription struct {
						Text string `json:"text"`
					} `json:"shortDescription"`
					Properties struct {
						Tags     []string `json:"tags"`
						Severity string   `json:"security-severity"`
					} `json:"properties"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Results []sarifResult `json:"results"`
	} `json:"runs"`
}

type sarifResult struct {
	RuleID  string `json:"ruleId"`
	Level   string `json:"level"`
	Message struct {
		Text string `json:"text"`
	} `json:"message"`
	Locations []struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
			Region struct {
				StartLine int `json:"startLine"`
				Snippet   struct {
					Text string `json:"text"`
				} `json:"snippet"`
			} `json:"region"`
		} `json:"physicalLocation"`
	} `json:"locations"`
}

func parseSARIF(data []byte) ([]finding.Finding, error) {
	var doc sarifDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse sarif: %w", err)
	}

	var out []finding.Finding
	idx := 0
	for _, run := range doc.Runs {
		tool := run.Tool.Driver.Name

		// Index rule metadata by id for enrichment.
		type ruleMeta struct{ vulnType, cwe, severity string }
		rules := map[string]ruleMeta{}
		for _, r := range run.Tool.Driver.Rules {
			vt := r.Name
			if vt == "" {
				vt = r.ShortDescription.Text
			}
			if vt == "" {
				vt = r.ID
			}
			cwe := ""
			for _, tag := range r.Properties.Tags {
				if len(tag) >= 3 && (tag[:3] == "CWE" || tag[:3] == "cwe") {
					cwe = tag
					break
				}
			}
			rules[r.ID] = ruleMeta{vulnType: vt, cwe: cwe, severity: r.Properties.Severity}
		}

		for _, res := range run.Results {
			file, line, snippet := "", 0, ""
			if len(res.Locations) > 0 {
				pl := res.Locations[0].PhysicalLocation
				file = pl.ArtifactLocation.URI
				line = pl.Region.StartLine
				snippet = pl.Region.Snippet.Text
			}
			meta := rules[res.RuleID]
			vt := meta.vulnType
			if vt == "" {
				vt = res.RuleID
			}
			out = append(out, finding.Finding{
				ID:          makeID(idx),
				File:        file,
				Line:        line,
				VulnType:    vt,
				Severity:    cleanSeverity(res.Level),
				CWE:         meta.cwe,
				Description: res.Message.Text,
				Code:        snippet,
				SourceTool:  tool,
			})
			idx++
		}
	}
	return out, nil
}
