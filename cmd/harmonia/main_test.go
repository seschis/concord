package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seschis/harmonia/internal/provider"
	"github.com/seschis/harmonia/internal/report"
)

// clearCredentialEnvs blanks every credential environment variable the spec
// factory or the resolvability predicate consults, so tests are independent
// of the host machine's credentials. t.Setenv restores each at test end.
func clearCredentialEnvs(t *testing.T) {
	t.Helper()
	for _, env := range []string{
		"OPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "GOOGLE_API_KEY",
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_PROFILE",
		"AWS_REGION", "AWS_DEFAULT_REGION",
		"CLAUDE_CODE_USE_BEDROCK", "AWS_BEARER_TOKEN_BEDROCK",
	} {
		t.Setenv(env, "")
	}
}

// claudeSpec is a keyless Claude preset spec: construction is offline and the
// anthropic SDK resolves credentials lazily at first use, so no credential is
// needed to build.
func claudeSpec(model string) provider.ModelSpec {
	return provider.ModelSpec{Name: "claude", Protocol: provider.ProtocolAnthropic, Model: model, ContextWindow: 128000}
}

func openaiSpec(name, key, model string) provider.ModelSpec {
	return provider.ModelSpec{Name: name, Protocol: provider.ProtocolOpenAI, APIKey: key, Model: model, ContextWindow: 128000}
}

func TestSplitSpec(t *testing.T) {
	cases := []struct{ in, name, value string }{
		{"strict", "strict", ""},
		{"strict=claude", "strict", "claude"},
		{"biz=/tmp/x.md", "biz", "/tmp/x.md"},
		{" padded = claude ", "padded", "claude"},
	}
	for _, c := range cases {
		name, value := splitSpec(c.in)
		if name != c.name || value != c.value {
			t.Fatalf("splitSpec(%q) = (%q,%q), want (%q,%q)", c.in, name, value, c.name, c.value)
		}
	}
}

func TestBuildJudgesDefaultAndPersonas(t *testing.T) {
	claude, err := provider.NewFromSpec(claudeSpec("claude-fake"), 100)
	if err != nil {
		t.Fatalf("NewFromSpec(claude) should build without a key (lazy SDK credential): %v", err)
	}
	voters := []provider.Provider{claude}

	// Default (no --judge): a single "adjudicator" judge on the default model.
	judges, names := buildJudges(voters, &config{})
	if len(judges) != 1 || names[0] != "adjudicator" {
		t.Fatalf("default panel = %d judges %v, want 1 [adjudicator]", len(judges), names)
	}
	if judges[0].Name() != "adjudicator" || judges[0].Model() != "claude-fake" {
		t.Fatalf("default judge mismatch: name=%q model=%q", judges[0].Name(), judges[0].Model())
	}

	// Built-in personas, all defaulting to the preferred (single) model.
	judges, names = buildJudges(voters, &config{judges: []string{"strict", "business", "codeflow"}})
	if len(judges) != 3 {
		t.Fatalf("want 3 judges, got %d", len(judges))
	}
	for i, w := range []string{"strict", "business", "codeflow"} {
		if names[i] != w || judges[i].Name() != w {
			t.Fatalf("judge[%d] = %q, want %q", i, names[i], w)
		}
		if judges[i].Model() != "claude-fake" {
			t.Fatalf("judge %q should default to the preferred model, got %q", w, judges[i].Model())
		}
	}
}

func TestBuildJudgesSkipsUnknownAndAppliesModelOverride(t *testing.T) {
	claude, err := provider.NewFromSpec(claudeSpec("claude-fake"), 100)
	if err != nil {
		t.Fatalf("NewFromSpec(claude): %v", err)
	}
	// A non-empty (fake) token lets the constructor build offline; no model
	// call is made, so this stays a pure wiring test.
	openai, err := provider.NewFromSpec(openaiSpec("openai", "sk-test-fake", "gpt-fake"), 100)
	if err != nil {
		t.Fatalf("NewFromSpec(openai): %v", err)
	}
	voters := []provider.Provider{claude, openai}

	// "nope" is not a built-in persona and has no file -> skipped. "strict" uses
	// the default model; "business" is overridden to the openai voter;
	// "codeflow" is overridden to the openai voter via the deprecated codex
	// alias.
	judges, names := buildJudges(voters, &config{
		judges:      []string{"nope", "strict", "business", "codeflow"},
		judgeModels: []string{"business=openai", "codeflow=codex"},
	})
	if len(judges) != 3 {
		t.Fatalf("want 3 judges (unknown skipped), got %d: %v", len(judges), names)
	}
	if names[0] != "strict" || names[1] != "business" || names[2] != "codeflow" {
		t.Fatalf("names = %v, want [strict business codeflow]", names)
	}
	if judges[0].Model() != "claude-fake" {
		t.Fatalf("strict should use the default model, got %q", judges[0].Model())
	}
	if judges[1].Model() != "gpt-fake" {
		t.Fatalf("business should be overridden to the openai voter (gpt-fake), got %q", judges[1].Model())
	}
	if judges[2].Model() != "gpt-fake" {
		t.Fatalf("the codex alias should resolve to the openai voter (gpt-fake), got %q", judges[2].Model())
	}
}

// TestJudgeDisplayNames verifies the banner/TUI rendering of the judge panel:
// each judge shows as "persona (voter name)", where the model id maps back to
// the voter that serves it.
func TestJudgeDisplayNames(t *testing.T) {
	claude, err := provider.NewFromSpec(claudeSpec("claude-fake"), 100)
	if err != nil {
		t.Fatalf("NewFromSpec(claude): %v", err)
	}
	openai, err := provider.NewFromSpec(openaiSpec("openai", "sk-test-fake", "gpt-fake"), 100)
	if err != nil {
		t.Fatalf("NewFromSpec(openai): %v", err)
	}
	voters := []provider.Provider{claude, openai}

	// Default panel: a single adjudicator on the preferred voter.
	judges, _ := buildJudges(voters, &config{})
	if got, want := judgeDisplayNames(judges, voters), []string{"adjudicator (claude)"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("judgeDisplayNames (default) = %v, want %v", got, want)
	}

	// Mixed panel: one judge on the default model, one overridden to the
	// openai voter.
	judges, _ = buildJudges(voters, &config{
		judges:      []string{"strict", "business"},
		judgeModels: []string{"business=openai"},
	})
	got := judgeDisplayNames(judges, voters)
	want := []string{"strict (claude)", "business (openai)"}
	if len(got) != len(want) {
		t.Fatalf("judgeDisplayNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("judgeDisplayNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildAnalysts(t *testing.T) {
	claude, err := provider.NewFromSpec(claudeSpec("claude-fake"), 100)
	if err != nil {
		t.Fatalf("NewFromSpec(claude) should build without a key (lazy SDK credential): %v", err)
	}
	voters := []provider.Provider{claude}

	// No --analyst -> empty panel (default unchanged).
	if a, n := buildAnalysts(voters, &config{}); len(a) != 0 || len(n) != 0 {
		t.Fatalf("default should yield no analysts, got %d analysts %v", len(a), n)
	}

	// Built-in lenses, all on the preferred (single) model.
	a, n := buildAnalysts(voters, &config{analysts: []string{"strict", "business", "codeflow"}})
	if len(a) != 3 || len(n) != 3 {
		t.Fatalf("want 3 analysts, got %d (%v)", len(a), n)
	}
	for i, w := range []string{"strict", "business", "codeflow"} {
		if n[i] != w || a[i].Name() != w {
			t.Fatalf("analyst[%d] = %q, want %q", i, a[i].Name(), w)
		}
		if a[i].Model() != "claude-fake" {
			t.Fatalf("analyst %q should run on the preferred model, got %q", w, a[i].Model())
		}
	}
}

func TestBuildAnalystsSkipsUnknownAndReadsCustomFile(t *testing.T) {
	claude, err := provider.NewFromSpec(claudeSpec("claude-fake"), 100)
	if err != nil {
		t.Fatalf("NewFromSpec(claude): %v", err)
	}
	voters := []provider.Provider{claude}

	dir := t.TempDir()
	custom := filepath.Join(dir, "paranoid.md")
	if err := os.WriteFile(custom, []byte("a paranoid lens"), 0o644); err != nil {
		t.Fatal(err)
	}

	// "nope" (unknown) and "adjudicator" (a judge-only persona, not an analyst
	// lens) are skipped; "strict" and "paranoid=/path" are kept.
	a, n := buildAnalysts(voters, &config{analysts: []string{"nope", "adjudicator", "strict", "paranoid=" + custom}})
	if len(a) != 2 || len(n) != 2 {
		t.Fatalf("want 2 analysts (unknown + judge-only skipped), got %d: %v", len(a), n)
	}
	if n[0] != "strict" || n[1] != "paranoid" {
		t.Fatalf("names = %v, want [strict paranoid]", n)
	}
	for _, p := range a {
		if p.Model() != "claude-fake" {
			t.Fatalf("analyst should run on the preferred model, got %q", p.Model())
		}
	}
}

// TestKeylessClaudePresetUnresolvable documents the U3 behavior change: a
// keyless Claude no longer votes a per-finding error stub. Without any
// ANTHROPIC_* credential the preset is unresolvable and gets a skip reason
// instead; with one it resolves.
func TestKeylessClaudePresetUnresolvable(t *testing.T) {
	clearCredentialEnvs(t)
	spec := provider.PresetClaude
	if ok, reason := spec.Resolvable(); ok || reason == "" {
		t.Fatalf("keyless claude with no ANTHROPIC_* env should be unresolvable, got ok=%v reason=%q", ok, reason)
	}
	t.Setenv("ANTHROPIC_API_KEY", "k")
	if ok, _ := spec.Resolvable(); !ok {
		t.Fatal("claude with ANTHROPIC_API_KEY should resolve")
	}
}

func TestParseAddModel(t *testing.T) {
	spec, err := parseAddModel("qwen,protocol=openai,endpoint=http://127.0.0.1:8000/v1,model=Qwen3.8-27B,context_window=262144")
	if err != nil {
		t.Fatalf("parseAddModel: %v", err)
	}
	if spec.Name != "qwen" || spec.Protocol != provider.ProtocolOpenAI ||
		spec.Endpoint != "http://127.0.0.1:8000/v1" || spec.Model != "Qwen3.8-27B" ||
		spec.ContextWindow != 262144 {
		t.Fatalf("parsed spec = %+v", spec)
	}

	if _, err := parseAddModel("qwen,bogus=1"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown key should be an error naming it, got %v", err)
	}

	// A bare token after the name is not a key=value pair.
	if _, err := parseAddModel("qwen,notakeyvalue"); err == nil {
		t.Fatal("a bare token should be an error")
	}

	// A non-integer window is a parse error.
	if _, err := parseAddModel("qwen,context_window=big"); err == nil {
		t.Fatal("a non-integer context_window should be an error")
	}

	// A spec carrying a literal api_key must not leak the key in any parse
	// error: the echoed spec is redacted (CWE-532).
	for _, specIn := range []string{
		"qwen,api_key=sk-secret-value,bogus=1",
		"qwen,api_key=sk-secret-value,notakeyvalue",
		"qwen,api_key=sk-secret-value,context_window=big",
		"qwen,api_key=sk-secret-value,price_in=big",
	} {
		_, err := parseAddModel(specIn)
		if err == nil {
			t.Fatalf("parseAddModel(%q) should fail", specIn)
		}
		if strings.Contains(err.Error(), "sk-secret-value") {
			t.Fatalf("parse error must not echo the api_key value: %v", err)
		}
	}

	// A reserved name parses fine; validation rejects it naming the set.
	spec, err = parseAddModel("strict,protocol=openai,model=gpt")
	if err != nil {
		t.Fatalf("parseAddModel: %v", err)
	}
	err = provider.ValidateSpecs([]provider.ModelSpec{spec}, nil)
	if err == nil || !strings.Contains(err.Error(), "strict") || !strings.Contains(err.Error(), "adjudicator") ||
		!strings.Contains(err.Error(), "business") || !strings.Contains(err.Error(), "codeflow") {
		t.Fatalf("reserved name should be rejected naming the reserved set, got %v", err)
	}
}

func TestDiscoverConfigFile(t *testing.T) {
	writeFile := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// An empty cwd so the discovery steps are deterministic.
	t.Chdir(t.TempDir())

	explicit := filepath.Join(t.TempDir(), "explicit.toml")
	writeFile(t, explicit, "# explicit\n")
	writeFile(t, "harmonia.toml", "# cwd\n")

	// --config wins over cwd.
	got, err := discoverConfigFile(&config{configFile: explicit}, "findings.sarif")
	if err != nil || got != explicit {
		t.Fatalf("--config should win over cwd, got %q (%v)", got, err)
	}

	// A missing --config file is an error.
	if _, err := discoverConfigFile(&config{configFile: filepath.Join(t.TempDir(), "nope.toml")}, "findings.sarif"); err == nil {
		t.Fatal("want an error for a missing --config file")
	}

	// cwd file found.
	got, err = discoverConfigFile(&config{}, "findings.sarif")
	if err != nil || got != "harmonia.toml" {
		t.Fatalf("cwd harmonia.toml should be found, got %q (%v)", got, err)
	}

	// Input-dir file found when cwd has none.
	os.Remove("harmonia.toml")
	inDir := t.TempDir()
	writeFile(t, filepath.Join(inDir, "harmonia.toml"), "# input dir\n")
	got, err = discoverConfigFile(&config{}, filepath.Join(inDir, "findings.sarif"))
	if err != nil || got != filepath.Join(inDir, "harmonia.toml") {
		t.Fatalf("input-dir harmonia.toml should be found, got %q (%v)", got, err)
	}

	// No file anywhere -> presets only.
	empty := t.TempDir()
	got, err = discoverConfigFile(&config{}, filepath.Join(empty, "findings.sarif"))
	if err != nil || got != "" {
		t.Fatalf("no config file should yield %q (%v)", got, err)
	}
}

const vllmSpecTOML = `
[[models]]
name = "qwen"
protocol = "openai"
endpoint = "http://127.0.0.1:8000/v1"
model = "Qwen3.8-27B"
context_window = 262144
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "harmonia.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveSpecsSkipReporting is the no-credentials scenario: the keyless
// vLLM spec resolves via a placeholder token and becomes the only voter, while
// every preset is skipped (with a printed per-spec reason).
func TestResolveSpecsSkipReporting(t *testing.T) {
	clearCredentialEnvs(t)
	path := writeConfig(t, vllmSpecTOML)

	voters, err := resolveSpecs(&config{maxTokens: 100}, path)
	if err != nil {
		t.Fatalf("the keyless vLLM spec should resolve via a placeholder token: %v", err)
	}
	names := report.DisplayNames(modelInfos(voters))
	if len(voters) != 1 || len(names) != 1 || names[0] != "qwen (Qwen3.8-27B)" {
		t.Fatalf("want only qwen as voter, got %v", names)
	}
}

// TestResolveSpecsZeroResolvable: with no credentials and no config file every
// preset is skipped and the run aborts with an error listing every reason.
func TestResolveSpecsZeroResolvable(t *testing.T) {
	clearCredentialEnvs(t)

	voters, err := resolveSpecs(&config{maxTokens: 100}, "")
	if err == nil {
		t.Fatal("want an error when no spec resolves")
	}
	if len(voters) != 0 {
		t.Fatalf("want no voters, got %d", len(voters))
	}
	// The error lists every skip reason, one per preset.
	for _, name := range []string{"claude", "gemini", "openai", "azure"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("zero-models error should name %s with a reason: %v", name, err)
		}
	}
	for _, name := range []string{"claude:", "gemini:", "openai:", "azure:"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should list the skip reason for %s, got %q", name, err.Error())
		}
	}
}

// TestResolveSpecsOrdering: presets first (claude, gemini, openai, azure) then
// TOML specs in declaration order, and the preferred model (judge default) is
// the first resolvable voter.
func TestResolveSpecsOrdering(t *testing.T) {
	clearCredentialEnvs(t)
	t.Setenv("ANTHROPIC_API_KEY", "claude-key")
	t.Setenv("GOOGLE_API_KEY", "gemini-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://res.openai.azure.com")

	path := writeConfig(t, `
[[models]]
name = "spec1"
protocol = "openai"
endpoint = "http://127.0.0.1:8001/v1"
model = "m1"
context_window = 16384

[[models]]
name = "spec2"
protocol = "openai"
endpoint = "http://127.0.0.1:8002/v1"
model = "m2"
context_window = 16384
`)
	voters, err := resolveSpecs(&config{maxTokens: 100}, path)
	if err != nil {
		t.Fatalf("resolveSpecs: %v", err)
	}
	names := report.DisplayNames(modelInfos(voters))
	want := []string{"claude (", "gemini (", "openai (", "azure (", "spec1 (m1)", "spec2 (m2)"}
	if len(names) != len(want) {
		t.Fatalf("want %d voters %v, got %v", len(want), want, names)
	}
	for i, w := range want {
		if !strings.HasPrefix(names[i], w) {
			t.Fatalf("voter[%d] = %q, want prefix %q", i, names[i], w)
		}
	}

	// Preferred = voters[0] = the judge default.
	judges, _ := buildJudges(voters, &config{})
	if judges[0].Model() != "claude-opus-5" {
		t.Fatalf("judge default should be the preferred voter (claude), got %q", judges[0].Model())
	}
}

// TestResolveSpecsFactoryErrorSkip: a spec the Resolvable predicate accepts
// but the factory rejects (azure: key resolvable, no endpoint anywhere) is
// skipped with the factory reason — the run proceeds with the remaining
// voters, and aborts listing the reason when it was the only resolvable one.
func TestResolveSpecsFactoryErrorSkip(t *testing.T) {
	clearCredentialEnvs(t)
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")

	// Azure alone: Resolvable passes on the key, NewFromSpec needs an
	// endpoint too and errors, so the only voter is skipped and the run
	// aborts listing the factory reason.
	voters, err := resolveSpecs(&config{maxTokens: 100}, "")
	if err == nil {
		t.Fatalf("want an error when the only resolvable spec fails at construction, got %d voters", len(voters))
	}
	if !strings.Contains(err.Error(), "azure requires an api key and endpoint") {
		t.Fatalf("the zero-models error should carry the azure factory reason, got %q", err.Error())
	}
	if len(voters) != 0 {
		t.Fatalf("want no voters, got %d", len(voters))
	}

	// With a second resolvable voter the run proceeds and only azure is
	// skipped.
	t.Setenv("ANTHROPIC_API_KEY", "claude-key")
	voters, err = resolveSpecs(&config{maxTokens: 100}, "")
	if err != nil {
		t.Fatalf("a second resolvable voter should let the run proceed: %v", err)
	}
	names := report.DisplayNames(modelInfos(voters))
	if len(voters) != 1 || !strings.HasPrefix(names[0], "claude (") {
		t.Fatalf("want only the claude voter (azure skipped at construction), got %v", names)
	}
}

// TestResolveSpecsSkipFlags: --no-model removes a custom spec by name,
// --no-codex removes the openai preset AND any spec named codex, --no-openai
// removes only the preset.
func TestResolveSpecsSkipFlags(t *testing.T) {
	clearCredentialEnvs(t)
	t.Setenv("OPENAI_API_KEY", "openai-key")

	path := writeConfig(t, `
[[models]]
name = "qwen"
protocol = "openai"
endpoint = "http://127.0.0.1:8001/v1"
model = "qwen-m"
context_window = 16384

[[models]]
name = "codex"
protocol = "openai"
endpoint = "http://127.0.0.1:8002/v1"
model = "codex-m"
context_window = 16384
`)
	namesOf := func(t *testing.T, cfg *config) []string {
		t.Helper()
		voters, err := resolveSpecs(cfg, path)
		if err != nil {
			t.Fatalf("resolveSpecs: %v", err)
		}
		return report.DisplayNames(modelInfos(voters))
	}
	hasName := func(names []string, prefix string) bool {
		for _, n := range names {
			if strings.HasPrefix(n, prefix) {
				return true
			}
		}
		return false
	}

	// Baseline: the openai preset plus both custom specs.
	names := namesOf(t, &config{maxTokens: 100})
	if !hasName(names, "openai (") || !hasName(names, "qwen (") || !hasName(names, "codex (") {
		t.Fatalf("baseline should have openai, qwen, codex voters, got %v", names)
	}

	// --no-model qwen removes the custom spec only.
	names = namesOf(t, &config{maxTokens: 100, noModels: []string{"qwen"}})
	if hasName(names, "qwen (") || !hasName(names, "openai (") || !hasName(names, "codex (") {
		t.Fatalf("--no-model qwen should remove only qwen, got %v", names)
	}

	// --no-codex removes the openai preset AND the spec named codex.
	names = namesOf(t, &config{maxTokens: 100, noCodex: true})
	if !hasName(names, "qwen (") || hasName(names, "openai (") || hasName(names, "codex (") {
		t.Fatalf("--no-codex should remove the openai preset and the codex spec, got %v", names)
	}

	// --no-openai removes the preset but keeps a spec named codex.
	names = namesOf(t, &config{maxTokens: 100, noOpenAI: true})
	if hasName(names, "openai (") || !hasName(names, "qwen (") || !hasName(names, "codex (") {
		t.Fatalf("--no-openai should remove only the openai preset, got %v", names)
	}
}

// TestResolveSpecsAddModel is the one-shot flag path: --add-model adds a
// custom voter and redefines a preset (flag > preset) without any file.
func TestResolveSpecsAddModel(t *testing.T) {
	clearCredentialEnvs(t)
	t.Setenv("ANTHROPIC_API_KEY", "claude-key")

	cfg := &config{
		maxTokens: 100,
		addModels: []string{
			"qwen,protocol=openai,endpoint=http://127.0.0.1:8000/v1,model=Qwen3.8-27B,context_window=262144",
			"claude,model=claude-sonnet-4-5",
		},
	}
	voters, err := resolveSpecs(cfg, "")
	if err != nil {
		t.Fatalf("resolveSpecs: %v", err)
	}
	names := report.DisplayNames(modelInfos(voters))
	if len(voters) != 2 {
		t.Fatalf("want claude + qwen voters, got %v", names)
	}
	if names[0] != "claude (claude-sonnet-4-5)" {
		t.Fatalf("the flag should redefine the claude preset model (flag > preset), got %v", names)
	}
	if names[1] != "qwen (Qwen3.8-27B)" {
		t.Fatalf("the one-shot spec should join as a voter, got %v", names)
	}
}

// TestCodexAliasesHiddenAndHonored: the deprecated codex flags are hidden from
// --help but still set the openai preset values, and --help shows the new
// canonical flags.
func TestCodexAliasesHiddenAndHonored(t *testing.T) {
	cfg := &config{}
	root := newRootCmd(cfg)
	var help bytes.Buffer
	root.SetOut(&help)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--help: %v", err)
	}
	hs := help.String()
	for _, flag := range []string{
		"--config", "--add-model", "--no-model",
		"--openai-model", "--openai-api-key", "--openai-endpoint", "--claude-endpoint", "--no-openai",
	} {
		if !strings.Contains(hs, flag) {
			t.Errorf("--help should show %s", flag)
		}
	}
	for _, flag := range []string{"--codex-model", "--codex-api-key", "--no-codex"} {
		if strings.Contains(hs, flag) {
			t.Errorf("--help should hide deprecated %s", flag)
		}
	}

	alias := &config{}
	if err := newRootCmd(alias).ParseFlags([]string{"--codex-model", "gpt-5.2", "--codex-api-key", "sk-test", "--no-codex"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if alias.openaiModel != "gpt-5.2" {
		t.Errorf("--codex-model should set the openai model, got %q", alias.openaiModel)
	}
	if alias.openaiKey != "sk-test" {
		t.Errorf("--codex-api-key should set the openai key, got %q", alias.openaiKey)
	}
	if !alias.noCodex {
		t.Error("--no-codex should set the skip flag")
	}
}
