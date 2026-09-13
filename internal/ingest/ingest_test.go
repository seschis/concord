package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

// TestLoadCSVWithBOM covers a common scanner-UI / Excel export shape: a UTF-8
// BOM, CRLF line endings, and fully quoted fields. The BOM must be stripped or
// the CSV reader fails with "bare \" in non-quoted-field" on the first field.
func TestLoadCSVWithBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.csv")
	body := "\xEF\xBB\xBF" +
		"\"ID\",\"Severity\",\"File path\",\"Category\",\"Line\"\r\n" +
		"\"3502519\",\"HIGH\",\"svc/a.go\",\"Missing authorization\",\"42\"\r\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	if fs[0].ID != "3502519" || fs[0].Severity != "HIGH" || fs[0].File != "svc/a.go" || fs[0].Line != 42 {
		t.Fatalf("BOM/CRLF CSV mismapped: %+v", fs[0])
	}
}

func TestParseCSV(t *testing.T) {
	data := []byte("file,type,severity,cwe,line,description\n" +
		"app/db.go,SQL Injection,critical,CWE-89,42,raw query\n" +
		"web/x.js,XSS,warning,CWE-79,7,unescaped output\n")
	fs, err := parseCSV(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("want 2 findings, got %d", len(fs))
	}
	if fs[0].VulnType != "SQL Injection" || fs[0].Severity != "CRITICAL" || fs[0].Line != 42 || fs[0].CWE != "CWE-89" {
		t.Fatalf("row 0 mismapped: %+v", fs[0])
	}
	if fs[1].Severity != "MEDIUM" { // "warning" -> MEDIUM
		t.Fatalf("severity normalization failed: %q", fs[1].Severity)
	}
}

func TestParseMarkdownBlocks(t *testing.T) {
	md := "" +
		"### FINDING 1 ###\n" +
		"- File: app/auth.go\n" +
		"- Vulnerability type: Hardcoded Secret\n" +
		"- Severity: HIGH\n" +
		"- CWE: CWE-798\n" +
		"- Line: 10\n" +
		"- Description: API key committed to source\n" +
		"```go\nconst key = \"abc\"\n```\n" +
		"### FINDING 2 ###\n" +
		"- File: web/x.js\n" +
		"- Type: XSS\n" +
		"- Severity: MEDIUM\n"
	fs, err := parseMarkdown([]byte(md))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("want 2 findings, got %d", len(fs))
	}
	if fs[0].File != "app/auth.go" || fs[0].VulnType != "Hardcoded Secret" || fs[0].Line != 10 {
		t.Fatalf("block 0 mismapped: %+v", fs[0])
	}
	if fs[0].Code == "" {
		t.Fatalf("expected code fence captured, got empty")
	}
	if fs[1].VulnType != "XSS" {
		t.Fatalf("block 1 type mismapped: %q", fs[1].VulnType)
	}
}

func TestParseMarkdownTable(t *testing.T) {
	md := "" +
		"| id | file | type | severity |\n" +
		"|----|------|------|----------|\n" +
		"| A1 | a.go | SQLi | high |\n"
	fs, err := parseMarkdown([]byte(md))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].ID != "A1" || fs[0].VulnType != "SQLi" || fs[0].Severity != "HIGH" {
		t.Fatalf("table parse failed: %+v", fs)
	}
}

func TestParseXLSX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.xlsx")
	fh := excelize.NewFile()
	sheet := fh.GetSheetName(0)
	_ = fh.SetSheetRow(sheet, "A1", &[]any{"file", "type", "severity", "line"})
	_ = fh.SetSheetRow(sheet, "A2", &[]any{"svc/a.go", "Path Traversal", "high", 15})
	if err := fh.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	fs, err := parseXLSX(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].VulnType != "Path Traversal" || fs[0].Severity != "HIGH" || fs[0].Line != 15 {
		t.Fatalf("xlsx parse failed: %+v", fs)
	}
}
