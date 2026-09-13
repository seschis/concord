package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "svc", "sub"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "node_modules"), 0o755))
	must(os.WriteFile(filepath.Join(root, "README.md"), []byte("line1\nline2\nline3\nline4\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "svc", "app.go"), []byte("package svc\n// TODO secret handling\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "node_modules", "junk.js"), []byte("nope"), 0o644))
	return root
}

func TestListDirSkipsVendorDirs(t *testing.T) {
	tb, err := NewToolBox(setupRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	out := tb.listDir("", ".")
	if !strings.Contains(out, "svc/") || !strings.Contains(out, "README.md") {
		t.Fatalf("expected svc/ and README.md, got:\n%s", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("node_modules should be skipped, got:\n%s", out)
	}
}

func TestReadFileLineRange(t *testing.T) {
	tb, _ := NewToolBox(setupRepo(t))
	out := tb.readFile("", "README.md", 2, 3)
	if !strings.Contains(out, "2: line2") || !strings.Contains(out, "3: line3") {
		t.Fatalf("expected lines 2-3, got:\n%s", out)
	}
	if strings.Contains(out, "line1") || strings.Contains(out, "line4") {
		t.Fatalf("range not respected, got:\n%s", out)
	}
}

func TestSandboxRejectsTraversal(t *testing.T) {
	tb, _ := NewToolBox(setupRepo(t))
	for _, rel := range []string{"../../etc/passwd", "/etc/passwd", "svc/../../.."} {
		if got := tb.readFile("", rel, 0, 0); !strings.HasPrefix(got, "error:") {
			t.Fatalf("expected sandbox error for %q, got:\n%s", rel, got)
		}
	}
}

func TestExecDispatch(t *testing.T) {
	tb, _ := NewToolBox(setupRepo(t))
	out := tb.Exec("read_file", `{"path":"svc/app.go"}`)
	if !strings.Contains(out, "package svc") {
		t.Fatalf("Exec read_file failed, got:\n%s", out)
	}
	if got := tb.Exec("bogus", `{}`); !strings.HasPrefix(got, "error: unknown tool") {
		t.Fatalf("expected unknown tool error, got: %s", got)
	}
}

// setupContext builds a second directory standing in for architecture context.
func setupContext(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gateway", "policy.yaml"), []byte("routes:\n  - path: /mcp\n    auth: bearer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMultiRootReadAndSearch(t *testing.T) {
	tb, err := NewToolBox(setupRepo(t), Root{Label: "arch", Path: setupContext(t)})
	if err != nil {
		t.Fatal(err)
	}
	// Default root is the primary repo.
	if out := tb.Exec("read_file", `{"path":"svc/app.go"}`); !strings.Contains(out, "package svc") {
		t.Fatalf("default root read failed:\n%s", out)
	}
	// root="arch" reads the context directory.
	if out := tb.Exec("read_file", `{"root":"arch","path":"gateway/policy.yaml"}`); !strings.Contains(out, "auth: bearer") {
		t.Fatalf("context root read failed:\n%s", out)
	}
	// A path that exists in the context root must NOT be reachable from the primary.
	if out := tb.Exec("read_file", `{"path":"gateway/policy.yaml"}`); !strings.HasPrefix(out, "error:") {
		t.Fatalf("primary root should not see context files, got:\n%s", out)
	}
	// Unknown root is rejected.
	if out := tb.Exec("list_dir", `{"root":"nope","path":"."}`); !strings.HasPrefix(out, "error: unknown root") {
		t.Fatalf("expected unknown root error, got:\n%s", out)
	}
	// Sandbox holds per-root: traversal out of the context root is refused.
	if out := tb.Exec("read_file", `{"root":"arch","path":"../../etc/passwd"}`); !strings.HasPrefix(out, "error:") {
		t.Fatalf("context root sandbox breach, got:\n%s", out)
	}
	// Search scoped to the context root finds context content.
	if out := tb.Exec("search", `{"root":"arch","pattern":"bearer"}`); !strings.Contains(out, "policy.yaml") {
		t.Fatalf("context root search failed:\n%s", out)
	}
}

func TestManifestAndDefinitions(t *testing.T) {
	// Single root: no manifest, no root param.
	solo, _ := NewToolBox(setupRepo(t))
	if solo.Manifest() != "" {
		t.Fatalf("single-root manifest should be empty, got:\n%s", solo.Manifest())
	}
	if _, ok := solo.Definitions()[0].Function.Parameters.(map[string]any)["properties"].(map[string]any)["root"]; ok {
		t.Fatalf("single-root read_file should not advertise a root param")
	}

	// Multi root: manifest names the context label + its top-level entry, and the
	// root param is advertised.
	tb, _ := NewToolBox(setupRepo(t), Root{Label: "arch", Path: setupContext(t)})
	man := tb.Manifest()
	if !strings.Contains(man, `"arch"`) || !strings.Contains(man, "gateway/") {
		t.Fatalf("manifest missing context label or entries:\n%s", man)
	}
	props := tb.Definitions()[0].Function.Parameters.(map[string]any)["properties"].(map[string]any)
	if _, ok := props["root"]; !ok {
		t.Fatalf("multi-root read_file should advertise a root param")
	}
}

func TestManifestGuides(t *testing.T) {
	ctxRoot := setupContext(t)
	// Drop a convention guide file at the context root.
	if err := os.WriteFile(filepath.Join(ctxRoot, ConventionGuideFile),
		[]byte("# Map\nGateway lives at gateway/, bypassed in-cluster.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tb, _ := NewToolBox(setupRepo(t), Root{Label: "arch", Path: ctxRoot})
	tb.WithGuide("Explicit map: services under services/aiml-*.")

	man := tb.Manifest()
	if !strings.Contains(man, "Explicit map: services under services/aiml-*.") {
		t.Fatalf("explicit guide missing from manifest:\n%s", man)
	}
	if !strings.Contains(man, "--context-guide") {
		t.Fatalf("explicit guide source label missing:\n%s", man)
	}
	if !strings.Contains(man, "Gateway lives at gateway/") {
		t.Fatalf("convention guide file not inlined:\n%s", man)
	}
	if !strings.Contains(man, "arch/"+ConventionGuideFile) {
		t.Fatalf("convention guide source label missing:\n%s", man)
	}
	// The convention guide must tell the model its relative paths resolve under
	// the "arch" root, so it uses root="arch" when following them.
	if !strings.Contains(man, `relative to root "arch"`) || !strings.Contains(man, `root="arch"`) {
		t.Fatalf("convention guide missing root annotation:\n%s", man)
	}
	if !strings.Contains(man, "gateway/") { // root listing still present
		t.Fatalf("root listing missing:\n%s", man)
	}
}

func TestGuideOnlyNoContextRoots(t *testing.T) {
	// A guide with no context roots still surfaces (describes the primary repo's
	// place in the architecture), but no root param is advertised.
	tb, _ := NewToolBox(setupRepo(t))
	tb.WithGuide("Primary service sits behind the api-gateway service.")
	if !strings.Contains(tb.Manifest(), "behind the api-gateway service") {
		t.Fatalf("guide-only manifest missing guide:\n%s", tb.Manifest())
	}
	if _, ok := tb.Definitions()[0].Function.Parameters.(map[string]any)["properties"].(map[string]any)["root"]; ok {
		t.Fatalf("no context roots, so no root param should be advertised")
	}
}

func TestContextLabelSanitizeAndUnique(t *testing.T) {
	repo := setupRepo(t)
	tb, err := NewToolBox(repo,
		Root{Label: "src", Path: setupContext(t)},         // collides with primary reserved label
		Root{Label: "my services", Path: setupContext(t)}, // space -> sanitized
		Root{Label: "my services", Path: setupContext(t)}, // duplicate -> -2 suffix
	)
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, len(tb.roots))
	for i, r := range tb.roots {
		labels[i] = r.Label
	}
	// primary stays "src"; the colliding context root is remapped; spaces removed;
	// duplicate disambiguated.
	if labels[0] != PrimaryLabel {
		t.Fatalf("primary label = %q, want %q", labels[0], PrimaryLabel)
	}
	seen := map[string]bool{}
	for _, l := range labels {
		if seen[l] {
			t.Fatalf("duplicate label %q in %v", l, labels)
		}
		seen[l] = true
		if strings.Contains(l, " ") {
			t.Fatalf("label %q not sanitized", l)
		}
	}
}
