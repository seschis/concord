package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// FileWriter appends each Record as one JSON line to
// <dir>/<findingID>_<role>_<provider>.jsonl. A transcript failure must never
// break triage, so every error here is swallowed.
type FileWriter struct {
	dir string
	mu  sync.Mutex // serializes the open-append-close cycle across goroutines
}

// NewFileWriter returns a Writer that appends JSONL transcripts under dir,
// creating it on first write.
func NewFileWriter(dir string) *FileWriter {
	return &FileWriter{dir: dir}
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string {
	if s == "" {
		return "unknown"
	}
	return unsafeChars.ReplaceAllString(s, "_")
}

// Record implements Writer.
func (w *FileWriter) Record(role, provider, findingID string, s Step) {
	line, err := json.Marshal(s)
	if err != nil {
		return
	}
	line = append(line, '\n')

	name := fmt.Sprintf("%s_%s_%s.jsonl", sanitize(findingID), sanitize(role), sanitize(provider))
	path := filepath.Join(w.dir, name)

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
}
