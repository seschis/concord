package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seschis/harmonia/internal/provider"
)

var fixedNow = func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }

func envOf(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

func TestDetectEnvModels(t *testing.T) {
	models, _, vars := detectEnvModels(envOf(map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant",
		"OPENAI_API_KEY":    "sk-oai",
		"GOOGLE_API_KEY":    "g-key",
	}))
	if len(models) != 3 {
		t.Fatalf("want 3 models, got %d", len(models))
	}
	if len(vars) != 3 {
		t.Fatalf("want 3 vars, got %d", len(vars))
	}
	byName := map[string]detectedModel{}
	for _, m := range models {
		byName[m.name] = m
	}
	if m := byName["claude"]; m.apiKey != "env:ANTHROPIC_API_KEY" || !m.presetMerge {
		t.Errorf("claude: %+v", m)
	}
	if m := byName["openai"]; m.protocol != "openai" {
		t.Errorf("openai: %+v", m)
	}
	if m := byName["gemini"]; m.protocol != "gemini" {
		t.Errorf("gemini: %+v", m)
	}
	// Nothing should carry a literal secret.
	for _, m := range models {
		if !strings.HasPrefix(m.apiKey, "env:") {
			t.Errorf("%s: apiKey should be env: ref, got %q", m.name, m.apiKey)
		}
	}
}

func TestDetectEnvModelsAzureNeedsEndpoint(t *testing.T) {
	models, notes, _ := detectEnvModels(envOf(map[string]string{
		"AZURE_OPENAI_API_KEY": "az-key",
	}))
	if len(models) != 0 {
		t.Fatalf("want 0 models without endpoint, got %d", len(models))
	}
	joined := strings.Join(notes, " ")
	if !strings.Contains(joined, "AZURE_OPENAI_ENDPOINT") {
		t.Errorf("want a note about the missing endpoint, got %q", joined)
	}
}

func TestDetectEnvModelsAzureFull(t *testing.T) {
	models, _, _ := detectEnvModels(envOf(map[string]string{
		"AZURE_OPENAI_API_KEY":     "az-key",
		"AZURE_OPENAI_ENDPOINT":    "https://x.openai.azure.com",
		"AZURE_OPENAI_API_VERSION": "2024-02-01",
	}))
	if len(models) != 1 {
		t.Fatalf("want 1 model, got %d", len(models))
	}
	m := models[0]
	if m.name != "azure" || m.protocol != "azure" || m.endpoint == "" || m.apiVersion != "2024-02-01" {
		t.Errorf("azure: %+v", m)
	}
}

func TestStripJSONC(t *testing.T) {
	cases := map[string]string{
		`{"a": 1} // trailing`:      `{"a": 1} `,
		"// lead\n{\"a\": 2}":       "\n{\"a\": 2}",
		`/* block */ {"a": 3}`:      ` {"a": 3}`,
		`{"s": "not // a comment"}`: `{"s": "not // a comment"}`,
		`{"s": "esc \\/* kept"}`:    `{"s": "esc \\/* kept"}`,
		`{"s": "quoted \" /* ok"}`:  `{"s": "quoted \" /* ok"}`,
		`{"a" /* c */ : 1}`:         `{"a"  : 1}`,
	}
	for in, want := range cases {
		if got := string(stripJSONC([]byte(in))); got != want {
			t.Errorf("stripJSONC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugName(t *testing.T) {
	cases := map[string]string{
		"aws-oh-vllm/unsloth:Qwen3.8": "aws-oh-vllm-unsloth-qwen3-8",
		"My_Provider":                 "my-provider",
		"---x---":                     "x",
		"UPPER.case":                  "upper-case",
	}
	for in, want := range cases {
		if got := slugName(in); got != want {
			t.Errorf("slugName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFinalizeNamesCollision(t *testing.T) {
	ms := []detectedModel{
		{name: "my-model"}, // kept
		{name: "my-model"}, // collision -> my-model-2
		{name: "strict"},   // reserved persona -> strict-custom
		{name: "claude"},   // preset name -> claude-custom
	}
	finalizeNames(ms)
	if ms[0].name != "my-model" || ms[1].name != "my-model-2" ||
		ms[2].name != "strict-custom" || ms[3].name != "claude-custom" {
		t.Errorf("got %q %q %q %q", ms[0].name, ms[1].name, ms[2].name, ms[3].name)
	}
}

func TestFinalizeNamesPresetMergeKept(t *testing.T) {
	ms := []detectedModel{{name: "openai", presetMerge: true}}
	finalizeNames(ms)
	if ms[0].name != "openai" {
		t.Errorf("preset merge name should stay openai, got %q", ms[0].name)
	}
}

func TestRenderTOMLNoSecrets(t *testing.T) {
	ms := []detectedModel{
		{name: "m1", protocol: "openai", endpoint: "http://x/v1", model: "model-a",
			contextWindow: 4096, apiKey: "sk-literal", hasSecret: true, source: "test"},
	}
	content, exports := renderTOML(ms, []string{"opencode: /tmp/x.jsonc"}, true, fixedNow)
	if strings.Contains(content, "sk-literal") {
		t.Errorf("noSecrets: content should not contain the literal key:\n%s", content)
	}
	if len(exports) == 0 || !strings.Contains(exports[0], "HARMONIA_KEY_M1") {
		t.Errorf("want export lines, got %v", exports)
	}
}

func TestRenderTOMLIncludesContextAndPrice(t *testing.T) {
	ms := []detectedModel{
		{name: "m1", protocol: "openai", model: "a", contextWindow: 8192,
			priceIn: 0.5, priceOut: 1.5, source: "s"},
	}
	content, _ := renderTOML(ms, nil, false, fixedNow)
	for _, want := range []string{"context_window = 8192", "price_in = 0.5", "price_out = 1.5"} {
		if !strings.Contains(content, want) {
			t.Errorf("content missing %q:\n%s", want, content)
		}
	}
}

// --- end-to-end runConfigure against a temp filesystem ---

func TestConfigureWritesFile(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	// An opencode config in the project dir with one supported provider.
	oc := `// project opencode config
{
  "provider": {
    "my-vllm": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8000/v1" },
      "models": { "local-model": { "limit": { "context": 32768 } } }
    }
  }
}`
	if err := os.WriteFile(filepath.Join(cwd, "opencode.json"), []byte(oc), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	o := configureOpts{
		cwd: cwd, homeDir: home,
		getEnv: envOf(map[string]string{"ANTHROPIC_API_KEY": "sk-ant"}),
		now:    fixedNow, out: &out,
	}
	if err := runConfigure(o); err != nil {
		t.Fatalf("runConfigure: %v", err)
	}
	// No local file in the cwd, so the default target is the machine-wide
	// config under the (injected) home dir.
	data, err := os.ReadFile(filepath.Join(home, ".config", "harmonia", "harmonia.toml"))
	if err != nil {
		t.Fatalf("machine-wide harmonia.toml not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "harmonia.toml")); !os.IsNotExist(err) {
		t.Error("default run should not create a local harmonia.toml")
	}
	s := string(data)
	for _, want := range []string{
		`name = "claude"`, `api_key = "env:ANTHROPIC_API_KEY"`,
		`name = "my-vllm-local-model"`, `endpoint = "http://127.0.0.1:8000/v1"`,
		`context_window = 32768`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated file missing %q:\n%s", want, s)
		}
	}
}

func TestConfigureRefusesOverwrite(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "harmonia.toml"), []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := configureOpts{
		cwd: cwd, homeDir: t.TempDir(),
		getEnv: envOf(map[string]string{"OPENAI_API_KEY": "sk"}),
		now:    fixedNow, out: &bytes.Buffer{},
	}
	err := runConfigure(o)
	if err == nil {
		t.Fatal("want an error when a local file exists and --force is not set")
	}
	if !strings.Contains(err.Error(), "--global") {
		t.Errorf("error should suggest --global, got: %v", err)
	}
}

func TestConfigureLocalFlag(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	o := configureOpts{
		cwd: cwd, homeDir: home, local: true,
		getEnv: envOf(map[string]string{"OPENAI_API_KEY": "sk"}),
		now:    fixedNow, out: &bytes.Buffer{},
	}
	if err := runConfigure(o); err != nil {
		t.Fatalf("runConfigure --local: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "harmonia.toml"))
	if err != nil {
		t.Fatalf("--local should write ./harmonia.toml: %v", err)
	}
	if !strings.Contains(string(data), `name = "openai"`) {
		t.Errorf("local file missing the openai entry:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "harmonia", "harmonia.toml")); !os.IsNotExist(err) {
		t.Error("--local should not write the machine-wide config")
	}
}

func TestConfigureGlobalWithLocalPresent(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "harmonia.toml"), []byte("# local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := configureOpts{
		cwd: cwd, homeDir: home, global: true,
		getEnv: envOf(map[string]string{"OPENAI_API_KEY": "sk"}),
		now:    fixedNow, out: &bytes.Buffer{},
	}
	if err := runConfigure(o); err != nil {
		t.Fatalf("runConfigure --global: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "harmonia", "harmonia.toml"))
	if err != nil {
		t.Fatalf("--global should write the machine-wide config: %v", err)
	}
	if !strings.Contains(string(data), `name = "openai"`) {
		t.Errorf("machine-wide file missing the openai entry:\n%s", data)
	}
	if _, err := os.ReadFile(filepath.Join(cwd, "harmonia.toml")); err != nil {
		t.Errorf("--global must not touch the local file: %v", err)
	}
}

func TestConfigureForceOverwrites(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "harmonia.toml"), []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := configureOpts{
		cwd: cwd, homeDir: t.TempDir(), force: true,
		getEnv: envOf(map[string]string{"OPENAI_API_KEY": "sk"}),
		now:    fixedNow, out: &bytes.Buffer{},
	}
	if err := runConfigure(o); err != nil {
		t.Fatalf("runConfigure --force: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(cwd, "harmonia.toml"))
	if strings.Contains(string(data), "# existing") {
		t.Error("--force should have overwritten the file")
	}
}

func TestConfigurePrintOnly(t *testing.T) {
	cwd := t.TempDir()
	var out bytes.Buffer
	o := configureOpts{
		cwd: cwd, homeDir: t.TempDir(), printOnly: true,
		getEnv: envOf(map[string]string{"OPENAI_API_KEY": "sk"}),
		now:    fixedNow, out: &out,
	}
	if err := runConfigure(o); err != nil {
		t.Fatalf("runConfigure --print: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "harmonia.toml")); !os.IsNotExist(err) {
		t.Error("--print should not write the file")
	}
	if !strings.Contains(out.String(), `name = "openai"`) {
		t.Errorf("--print should show the content, got:\n%s", out.String())
	}
}

func TestConfigureNoSecretsSummary(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	oc := `{
  "provider": {
    "hosted": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "https://api.example.com/v1", "apiKey": "sk-test-123" },
      "models": { "gpt-x": { "contextWindow": 131072 } }
    }
  }
}`
	if err := os.WriteFile(filepath.Join(cwd, "opencode.json"), []byte(oc), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := t.TempDir()
	var out bytes.Buffer
	o := configureOpts{
		cwd: cwd, homeDir: homeDir, noSecrets: true,
		getEnv: envOf(nil), now: fixedNow, out: &out,
	}
	if err := runConfigure(o); err != nil {
		t.Fatalf("runConfigure: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, `export HARMONIA_KEY_HOSTED_GPT_X="sk-test-123"`) {
		t.Errorf("want a valid (no-dash) export line, got:\n%s", s)
	}
	if strings.Contains(s, "do not commit it") {
		t.Errorf("no gitignore warning expected under --no-secrets:\n%s", s)
	}
	data, _ := os.ReadFile(filepath.Join(homeDir, ".config", "harmonia", "harmonia.toml"))
	if strings.Contains(string(data), "sk-test-123") {
		t.Error("file must not contain the literal key under --no-secrets")
	}
}

func TestConfigureNoModels(t *testing.T) {
	cwd := t.TempDir()
	var out bytes.Buffer
	o := configureOpts{cwd: cwd, homeDir: t.TempDir(), getEnv: envOf(nil), now: fixedNow, out: &out}
	if err := runConfigure(o); err != nil {
		t.Fatalf("runConfigure with no sources should not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "harmonia.toml")); !os.IsNotExist(err) {
		t.Error("no models: should not write a file")
	}
	if !strings.Contains(out.String(), "No LLM provider configuration found") {
		t.Errorf("want guidance, got:\n%s", out.String())
	}
}

func TestDetectOpencodeModelsSkipsUnsupported(t *testing.T) {
	home := t.TempDir()
	oc := `{
  "provider": {
    "mystery": { "npm": "@ai-sdk/something-else", "models": { "m1": {} } },
    "my-vllm": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8000/v1" },
      "models": { "local-model": { "contextWindow": 32768 } }
    }
  }
}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(oc), 0o644); err != nil {
		t.Fatal(err)
	}
	models, notes, src := detectOpencodeModels(home, dir, envOf(nil))
	if src == "" {
		t.Fatal("expected a source path")
	}
	if len(models) != 1 {
		t.Fatalf("want 1 model (mystery skipped), got %d", len(models))
	}
	m := models[0]
	if m.protocol != "openai" || m.contextWindow != 32768 || m.windowAssumed {
		t.Errorf("model: %+v", m)
	}
	if !strings.Contains(strings.Join(notes, " "), "mystery") {
		t.Errorf("want a skip note for the unsupported provider, got %v", notes)
	}
}

func TestDetectPiModels(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsJSON := `{
  "providers": {
    "local": {
      "baseUrl": "http://127.0.0.1:8000/v1",
      "models": [
        { "id": "pi-model", "contextWindow": 16384, "cost": { "input": 0.3, "output": 0.9 } }
      ]
    }
  }
}`
	authJSON := `{"local": { "type": "api_key", "key": "sk-pi" }}`
	if err := os.WriteFile(filepath.Join(agent, "models.json"), []byte(modelsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "auth.json"), []byte(authJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	models, _, src := detectPiModels(home, envOf(nil))
	if src == "" {
		t.Fatal("expected a source path")
	}
	if len(models) != 1 {
		t.Fatalf("want 1 model, got %d", len(models))
	}
	m := models[0]
	if m.protocol != "openai" || m.endpoint != "http://127.0.0.1:8000/v1" || m.model != "pi-model" {
		t.Errorf("model: %+v", m)
	}
	if m.apiKey != "sk-pi" || !m.hasSecret || m.contextWindow != 16384 {
		t.Errorf("model: %+v", m)
	}
	if m.priceIn != 0.3 || m.priceOut != 0.9 {
		t.Errorf("price: %+v", m)
	}
}

func TestDetectPiModelsEnvReference(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsJSON := `{"providers": {"prov": {"baseUrl": "http://x/v1", "models": [{"id": "m"}]}}}`
	authJSON := `{"prov": { "type": "api_key", "env": { "MY_PROVIDER_KEY": "" } }}`
	if err := os.WriteFile(filepath.Join(agent, "models.json"), []byte(modelsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "auth.json"), []byte(authJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	models, _, _ := detectPiModels(home, envOf(nil))
	if len(models) != 1 {
		t.Fatalf("want 1 model, got %d", len(models))
	}
	if models[0].apiKey != "env:MY_PROVIDER_KEY" || models[0].hasSecret {
		t.Errorf("want an env: reference, got %+v", models[0])
	}
}

// TestConfigureOutputPassesValidation proves the generated file round-trips
// through the exact load/merge/validate path a real run uses, so configure
// can never emit a harmonia.toml the tool would then reject.
func TestConfigureOutputPassesValidation(t *testing.T) {
	cwd := t.TempDir()
	oc := `{
  "provider": {
    "my-vllm": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://127.0.0.1:8000/v1" },
      "models": { "local-model": { "limit": { "context": 32768 } } }
    },
    "shane-local": {
      "npm": "@ai-sdk/anthropic",
      "options": { "baseURL": "http://127.0.0.1:8001", "apiKey": "sk-local" },
      "models": { "mlx-model": { "contextWindow": 102400 } }
    }
  }
}`
	if err := os.WriteFile(filepath.Join(cwd, "opencode.json"), []byte(oc), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	o := configureOpts{
		cwd: cwd, homeDir: home,
		getEnv: envOf(map[string]string{"ANTHROPIC_API_KEY": "sk-ant"}),
		now:    fixedNow, out: &bytes.Buffer{},
	}
	if err := runConfigure(o); err != nil {
		t.Fatalf("runConfigure: %v", err)
	}

	specs, err := provider.LoadTOML(filepath.Join(home, ".config", "harmonia", "harmonia.toml"))
	if err != nil {
		t.Fatalf("LoadTOML on generated file: %v", err)
	}
	presets := []provider.ModelSpec{provider.PresetClaude, provider.PresetGemini, provider.PresetOpenAI, provider.PresetAzure}
	presetNames := map[string]bool{}
	for _, p := range presets {
		presetNames[p.Name] = true
	}
	merged := provider.MergeSpecs(presets, specs)
	if err := provider.ValidateSpecs(merged, presetNames); err != nil {
		t.Fatalf("ValidateSpecs on generated file: %v", err)
	}
	// The claude entry must have picked up the preset model id via merge.
	var claudeOK, vllmOK, mlxOK bool
	for _, s := range merged {
		switch s.Name {
		case "claude":
			claudeOK = s.Model != "" && s.ContextWindow >= 4096
		case "my-vllm-local-model":
			vllmOK = s.Endpoint == "http://127.0.0.1:8000/v1"
		case "shane-local-mlx-model":
			mlxOK = s.Protocol == provider.ProtocolAnthropic && s.APIKey == "sk-local"
		}
	}
	if !claudeOK || !vllmOK || !mlxOK {
		t.Errorf("merged specs incomplete: claude=%v vllm=%v mlx=%v", claudeOK, vllmOK, mlxOK)
	}
}

func TestFirstExisting(t *testing.T) {
	dir := t.TempDir()
	hit := filepath.Join(dir, "exists.json")
	if err := os.WriteFile(hit, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := firstExisting(filepath.Join(dir, "nope.json"), hit)
	if got != hit {
		t.Errorf("firstExisting = %q, want %q", got, hit)
	}
}
