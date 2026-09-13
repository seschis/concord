package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFileWriterRecordAppendsJSONL(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)

	w.Record("voter", "claude", "12345", Step{Iter: 1, Text: "starting", InTokens: 10, OutTokens: 5})
	w.Record("voter", "claude", "12345", Step{
		Iter:      2,
		ToolCalls: []ToolCall{{Tool: "read_file", Args: `{"path":"a.go"}`, Result: "package a"}},
		InTokens:  20, OutTokens: 8,
	})

	data, err := os.ReadFile(filepath.Join(dir, "12345_voter_claude.jsonl"))
	if err != nil {
		t.Fatalf("read transcript file: %v", err)
	}
	lines := splitLines(string(data))
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), string(data))
	}
	var first Step
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal first line: %v", err)
	}
	if first.Text != "starting" || first.Iter != 1 {
		t.Errorf("first step mismatch: %+v", first)
	}
	var second Step
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("unmarshal second line: %v", err)
	}
	if len(second.ToolCalls) != 1 || second.ToolCalls[0].Tool != "read_file" {
		t.Errorf("second step mismatch: %+v", second)
	}
}

func TestFileWriterSeparatesByRoleAndProvider(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)

	w.Record("voter", "claude", "1", Step{Iter: 1})
	w.Record("explorer", "claude", "1", Step{Iter: 1})
	w.Record("voter", "gemini", "1", Step{Iter: 1})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 separate transcript files, got %d", len(entries))
	}
}

func TestFileWriterConcurrentRecordIsSafe(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)

	var wg sync.WaitGroup
	const n = 50
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w.Record("voter", "claude", "concurrent", Step{Iter: i})
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "concurrent_voter_claude.jsonl"))
	if err != nil {
		t.Fatalf("read transcript file: %v", err)
	}
	lines := splitLines(string(data))
	if len(lines) != n {
		t.Fatalf("want %d lines, got %d", n, len(lines))
	}
	for _, l := range lines {
		var s Step
		if err := json.Unmarshal([]byte(l), &s); err != nil {
			t.Errorf("corrupted line from concurrent writes: %q (%v)", l, err)
		}
	}
}

func TestSanitizeFilenameSafe(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	w.Record("voter", "claude", "../../etc/passwd", Step{Iter: 1})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 file, got %d", len(entries))
	}
	if strings.ContainsAny(entries[0].Name(), "/\\") {
		t.Errorf("sanitized name still contains a path separator: %q", entries[0].Name())
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
