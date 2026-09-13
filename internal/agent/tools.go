// Package agent implements a provider-agnostic tool-calling loop and the
// sandboxed file tools it drives. It depends only on langchaingo's llms package,
// so provider can import it without an import cycle.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

const (
	maxToolOutput = 20000 // chars returned to the model per tool call
	maxReadLines  = 400
	maxListItems  = 300
	maxSearchHits = 50
)

// skipDirs are never listed or searched.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "build": true,
	"dist": true, "out": true, "vendor": true, ".venv": true, "venv": true,
	"__pycache__": true, ".gradle": true, ".idea": true, ".terraform": true,
}

// PrimaryLabel is the reserved label for the finding's own repository, always
// the first root of a ToolBox.
const PrimaryLabel = "src"

// Root is a labeled directory the tools may read. The label is what the model
// passes in the tools' "root" argument and what appears in the manifest.
type Root struct {
	Label string
	Path  string
}

// ConventionGuideFile, when present at the top level of a context root, is
// inlined into the manifest automatically so users can drop an architecture map
// next to the code without passing a flag.
const ConventionGuideFile = "TRIAGE_CONTEXT.md"

const maxGuideChars = 16000 // cap injected guide/convention text per source

// ToolBox exposes read-only file tools sandboxed to one or more labeled roots.
// roots[0] is the primary (the finding's repo, label PrimaryLabel); any others
// are supporting architecture-context directories.
type ToolBox struct {
	roots   []Root
	byLabel map[string]string // label -> absolute path
	guide   string            // explicit human-authored map, injected into the manifest
}

// WithGuide attaches an explicit architecture guide (already-read file contents)
// that Manifest surfaces ahead of the auto-discovered material. Returns t for
// chaining.
func (t *ToolBox) WithGuide(text string) *ToolBox {
	t.guide = strings.TrimSpace(text)
	return t
}

// NewToolBox validates that primary is a directory and returns a sandboxed
// toolbox. Any context roots are added as additional, independently sandboxed
// roots the model can opt into via the tools' "root" argument. Context labels
// are made unique and never collide with the primary.
func NewToolBox(primary string, context ...Root) (*ToolBox, error) {
	primAbs, err := absDir(primary)
	if err != nil {
		return nil, fmt.Errorf("srcroot is not a directory: %s", primary)
	}
	tb := &ToolBox{
		roots:   []Root{{Label: PrimaryLabel, Path: primAbs}},
		byLabel: map[string]string{PrimaryLabel: primAbs},
	}
	for _, c := range context {
		abs, err := absDir(c.Path)
		if err != nil {
			return nil, fmt.Errorf("context dir is not a directory: %s", c.Path)
		}
		label := uniqueLabel(tb.byLabel, sanitizeLabel(c.Label))
		tb.roots = append(tb.roots, Root{Label: label, Path: abs})
		tb.byLabel[label] = abs
	}
	return tb, nil
}

func absDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return abs, nil
}

func sanitizeLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	s = strings.Trim(s, "-")
	if s == "" || s == PrimaryLabel {
		return "ctx"
	}
	return s
}

func uniqueLabel(taken map[string]string, base string) string {
	if _, ok := taken[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, ok := taken[cand]; !ok {
			return cand
		}
	}
}

// hasContext reports whether any supporting context roots are configured.
func (t *ToolBox) hasContext() bool { return len(t.roots) > 1 }

// Definitions returns the tool schemas to advertise to the model. When context
// roots are configured, each tool gains an optional "root" argument selecting
// which root to read; otherwise the schema is identical to the single-root case.
func (t *ToolBox) Definitions() []llms.Tool {
	readProps := map[string]any{
		"path":       map[string]any{"type": "string", "description": "File path relative to the root."},
		"start_line": map[string]any{"type": "integer", "description": "1-based first line (optional)."},
		"end_line":   map[string]any{"type": "integer", "description": "1-based last line (optional)."},
	}
	listProps := map[string]any{
		"path": map[string]any{"type": "string", "description": "Directory path relative to the root. Use '.' for the root."},
	}
	searchProps := map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Text or regex to search for."},
		"path":    map[string]any{"type": "string", "description": "Optional subdirectory to scope the search."},
	}
	if t.hasContext() {
		rp := t.rootParam()
		readProps["root"] = rp
		listProps["root"] = rp
		searchProps["root"] = rp
	}
	return []llms.Tool{
		fnTool("read_file", "Read a UTF-8 text file relative to a root. Optionally restrict to a line range.",
			map[string]any{"type": "object", "properties": readProps, "required": []string{"path"}}),
		fnTool("list_dir", "List files and subdirectories directly under a path relative to a root.",
			map[string]any{"type": "object", "properties": listProps, "required": []string{"path"}}),
		fnTool("search", "Search file contents for a string or regex, returning file:line matches.",
			map[string]any{"type": "object", "properties": searchProps, "required": []string{"pattern"}}),
	}
}

// rootParam is the shared JSON-schema fragment for the "root" argument.
func (t *ToolBox) rootParam() map[string]any {
	labels := make([]string, len(t.roots))
	for i, r := range t.roots {
		labels[i] = r.Label
	}
	return map[string]any{
		"type": "string",
		"enum": labels,
		"description": fmt.Sprintf(
			"Which root to read. Default %q (the finding's own repository). Other roots are supporting architecture context: %s.",
			PrimaryLabel, strings.Join(labels[1:], ", ")),
	}
}

func fnTool(name, desc string, params map[string]any) llms.Tool {
	return llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        name,
			Description: desc,
			Parameters:  params,
		},
	}
}

// Exec dispatches a tool call by name with its JSON arguments and returns text
// suitable for a tool result. Errors are returned as "error: ..." strings rather
// than failing the loop.
func (t *ToolBox) Exec(name, argsJSON string) string {
	var args map[string]any
	if argsJSON != "" {
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}
	get := func(k string) string {
		if s, ok := args[k].(string); ok {
			return s
		}
		return ""
	}
	getInt := func(k string) int {
		if f, ok := args[k].(float64); ok {
			return int(f)
		}
		return 0
	}
	root := get("root")
	switch name {
	case "read_file":
		return t.readFile(root, get("path"), getInt("start_line"), getInt("end_line"))
	case "list_dir":
		return t.listDir(root, get("path"))
	case "search":
		return t.search(root, get("pattern"), get("path"))
	default:
		return "error: unknown tool " + name
	}
}

// rootPath resolves a root label to its absolute path, defaulting to the primary
// when the label is empty. The bool is false for an unknown label.
func (t *ToolBox) rootPath(label string) (string, bool) {
	if label == "" {
		label = PrimaryLabel
	}
	p, ok := t.byLabel[label]
	return p, ok
}

// safeJoin resolves rel under the given root, refusing anything that escapes it.
func (t *ToolBox) safeJoin(root, rel string) (string, bool) {
	if rel == "" {
		rel = "."
	}
	p := filepath.Clean(filepath.Join(root, strings.TrimPrefix(rel, "/")))
	if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return p, true
	}
	return "", false
}

func (t *ToolBox) readFile(rootLabel, rel string, start, end int) string {
	root, ok := t.rootPath(rootLabel)
	if !ok {
		return "error: unknown root: " + rootLabel
	}
	p, ok := t.safeJoin(root, rel)
	if !ok {
		return "error: path outside sandbox: " + rel
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "error: " + err.Error()
	}
	lines := strings.Split(string(data), "\n")
	lo := 0
	if start > 0 {
		lo = start - 1
	}
	if lo > len(lines) {
		lo = len(lines)
	}
	hi := len(lines)
	if end > 0 && end < hi {
		hi = end
	}
	if hi-lo > maxReadLines {
		hi = lo + maxReadLines
	}
	var b strings.Builder
	for i := lo; i < hi; i++ {
		fmt.Fprintf(&b, "%5d: %s\n", i+1, lines[i])
	}
	return truncate(b.String())
}

func (t *ToolBox) listDir(rootLabel, rel string) string {
	root, ok := t.rootPath(rootLabel)
	if !ok {
		return "error: unknown root: " + rootLabel
	}
	p, ok := t.safeJoin(root, rel)
	if !ok {
		return "error: path outside sandbox: " + rel
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return "error: " + err.Error()
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	var out []string
	for _, e := range entries {
		if len(out) >= maxListItems {
			out = append(out, "...[truncated]")
			break
		}
		if e.IsDir() && skipDirs[e.Name()] {
			continue
		}
		if e.IsDir() {
			out = append(out, e.Name()+"/")
		} else {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		return "(empty)"
	}
	return strings.Join(out, "\n")
}

func (t *ToolBox) search(rootLabel, pattern, rel string) string {
	root, ok := t.rootPath(rootLabel)
	if !ok {
		return "error: unknown root: " + rootLabel
	}
	scope := root
	if rel != "" {
		p, ok := t.safeJoin(root, rel)
		if !ok {
			return "error: path outside sandbox: " + rel
		}
		scope = p
	}
	// Prefer ripgrep, fall back to grep.
	for _, cmd := range [][]string{
		{"rg", "-n", "--no-heading", "-i", "-m", fmt.Sprint(maxSearchHits), pattern, scope},
		{"grep", "-rniI", "-m", fmt.Sprint(maxSearchHits), pattern, scope},
	} {
		bin, err := exec.LookPath(cmd[0])
		if err != nil {
			continue
		}
		out, _ := exec.Command(bin, cmd[1:]...).Output()
		text := strings.TrimSpace(string(out))
		text = strings.ReplaceAll(text, root+string(os.PathSeparator), "")
		lines := strings.Split(text, "\n")
		if len(lines) > maxSearchHits {
			lines = lines[:maxSearchHits]
		}
		if text == "" {
			return "(no matches)"
		}
		return truncate(strings.Join(lines, "\n"))
	}
	return "error: no search tool (rg/grep) available"
}

func truncate(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n...[truncated]"
	}
	return s
}

const manifestEntriesPerRoot = 80

// Manifest returns the supporting-context preamble the model sees: an optional
// human-authored guide (explicit and/or a ConventionGuideFile discovered at a
// context root), then an inventory of the context roots and their top-level
// entries. Returns "" when there is neither a guide nor any context root.
func (t *ToolBox) Manifest() string {
	guides := t.collectGuides()
	if !t.hasContext() && len(guides) == 0 {
		return ""
	}
	var b strings.Builder
	if t.hasContext() {
		b.WriteString("Supporting architecture-context roots are available (read-only, and may be out of date). ")
		b.WriteString("Consult them via read_file/list_dir/search with the \"root\" argument when a finding's exploitability depends on cross-service topology, trust boundaries, network policy, or upstream controls. Prefer code and config over prose; treat docs and diagrams as hints to confirm against code.\n")
	}
	for _, g := range guides {
		rel := ""
		if g.rootLabel != "" {
			rel = fmt.Sprintf(" Paths in this guide are relative to root %q; read them with root=%q.", g.rootLabel, g.rootLabel)
		}
		fmt.Fprintf(&b, "\n--- architecture guide (%s, human-authored map, may be out of date).%s ---\n%s\n", g.source, rel, g.text)
	}
	for _, r := range t.roots[1:] {
		fmt.Fprintf(&b, "\nroot %q:\n", r.Label)
		b.WriteString(indentLines(t.topLevel(r.Path), "  "))
	}
	return b.String()
}

// guideEntry is one inlined guide. rootLabel is set for a ConventionGuideFile
// (its relative paths resolve under that root); it is empty for an explicit
// --context-guide, whose paths are not tied to a single root.
type guideEntry struct {
	source    string
	rootLabel string
	text      string
}

// collectGuides gathers the explicit guide (if set) plus any ConventionGuideFile
// found at the top level of a context root, capping each to maxGuideChars.
func (t *ToolBox) collectGuides() []guideEntry {
	var out []guideEntry
	if t.guide != "" {
		out = append(out, guideEntry{source: "--context-guide", text: capText(t.guide, maxGuideChars)})
	}
	for _, r := range t.roots[1:] {
		p := filepath.Join(r.Path, ConventionGuideFile)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		out = append(out, guideEntry{
			rootLabel: r.Label,
			source:    fmt.Sprintf("%s/%s", r.Label, ConventionGuideFile),
			text:      capText(text, maxGuideChars),
		})
	}
	return out
}

func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...[guide truncated]"
}

// topLevel lists the immediate entries of dir (dirs first), capped, skipping the
// usual build/VCS noise.
func (t *ToolBox) topLevel(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "(unreadable)"
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	var out []string
	for _, e := range entries {
		if e.IsDir() && skipDirs[e.Name()] {
			continue
		}
		if len(out) >= manifestEntriesPerRoot {
			out = append(out, "...[truncated]")
			break
		}
		if e.IsDir() {
			out = append(out, e.Name()+"/")
		} else {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		return "(empty)"
	}
	return strings.Join(out, "\n")
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n") + "\n"
}
