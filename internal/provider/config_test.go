package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTOML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "concord.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const vllmTOML = `
[[models]]
name = "qwen"
protocol = "openai"
endpoint = "http://127.0.0.1:8000/v1"
model = "Qwen3.8-27B"
context_window = 262144
`

func TestLoadTOML(t *testing.T) {
	t.Run("valid vLLM entry", func(t *testing.T) {
		specs, err := LoadTOML(writeTOML(t, vllmTOML))
		if err != nil {
			t.Fatalf("LoadTOML: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("want 1 spec, got %d", len(specs))
		}
		s := specs[0]
		if s.Name != "qwen" || s.Protocol != ProtocolOpenAI ||
			s.Endpoint != "http://127.0.0.1:8000/v1" || s.Model != "Qwen3.8-27B" ||
			s.ContextWindow != 262144 {
			t.Fatalf("spec = %+v", s)
		}
	})

	t.Run("all optional fields", func(t *testing.T) {
		path := writeTOML(t, `
[[models]]
name = "full"
protocol = "anthropic"
endpoint = "http://127.0.0.1:9999"
model = "local-model"
context_window = 8192
api_key = "env:MY_KEY"
price_in = 1.5
price_out = 2.5
bedrock = true
region = "us-east-1"

[[models]]
name = "az"
protocol = "azure"
model = "gpt-5.5"
context_window = 128000
api_version = "2025-03-01"
`)
		specs, err := LoadTOML(path)
		if err != nil {
			t.Fatalf("LoadTOML: %v", err)
		}
		if len(specs) != 2 {
			t.Fatalf("want 2 specs, got %d", len(specs))
		}
		s := specs[0]
		if s.Protocol != ProtocolAnthropic || s.APIKey != "env:MY_KEY" ||
			s.PriceIn == nil || *s.PriceIn != 1.5 || s.PriceOut == nil || *s.PriceOut != 2.5 ||
			!s.Bedrock || s.Region != "us-east-1" {
			t.Fatalf("spec[0] = %+v", s)
		}
		if specs[1].APIVersion != "2025-03-01" {
			t.Fatalf("spec[1].APIVersion = %q, want 2025-03-01", specs[1].APIVersion)
		}
	})

	t.Run("unknown key is an error", func(t *testing.T) {
		path := writeTOML(t, vllmTOML+"\nbogus = 1\n")
		if _, err := LoadTOML(path); err == nil || !strings.Contains(err.Error(), "bogus") {
			t.Fatalf("want an error naming the unknown key, got %v", err)
		}
	})

	t.Run("unknown key inside a models table is an error", func(t *testing.T) {
		path := writeTOML(t, vllmTOML+"\n[[models]]\nname = \"x\"\nprotocol = \"openai\"\nmodel = \"m\"\ncontext_window = 8192\ncontext = 1\n")
		if _, err := LoadTOML(path); err == nil || !strings.Contains(err.Error(), "context") {
			t.Fatalf("want an error naming the unknown key, got %v", err)
		}
	})

	t.Run("duplicate name is an error", func(t *testing.T) {
		path := writeTOML(t, vllmTOML+vllmTOML)
		if _, err := LoadTOML(path); err == nil || !strings.Contains(err.Error(), "qwen") {
			t.Fatalf("want a duplicate-name error naming the model, got %v", err)
		}
	})

	t.Run("no models table", func(t *testing.T) {
		specs, err := LoadTOML(writeTOML(t, "# just a comment\n"))
		if err != nil {
			t.Fatalf("LoadTOML: %v", err)
		}
		if len(specs) != 0 {
			t.Fatalf("want 0 specs, got %d", len(specs))
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := LoadTOML(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
			t.Fatal("want an error for a missing file")
		}
	})
}

// LoadTOML decodes only; the field-level rules (price pair, required context
// window, window floor) apply to the merged spec, so these go through
// ValidateSpecs after the load.
func TestLoadTOMLValidation(t *testing.T) {
	t.Run("one-sided price", func(t *testing.T) {
		specs, err := LoadTOML(writeTOML(t, vllmTOML+"price_in = 1.0\n"))
		if err != nil {
			t.Fatalf("LoadTOML: %v (decoding a one-sided price is legal; validation rejects it)", err)
		}
		if err := ValidateSpecs(specs, nil); err == nil || !strings.Contains(err.Error(), "price") {
			t.Fatalf("want a price-pair error, got %v", err)
		}
	})

	t.Run("custom spec missing context_window", func(t *testing.T) {
		specs, err := LoadTOML(writeTOML(t, `
[[models]]
name = "qwen"
protocol = "openai"
endpoint = "http://127.0.0.1:8000/v1"
model = "Qwen3.8-27B"
`))
		if err != nil {
			t.Fatalf("LoadTOML: %v", err)
		}
		if err := ValidateSpecs(specs, nil); err == nil || !strings.Contains(err.Error(), "context_window") {
			t.Fatalf("want a context_window error, got %v", err)
		}
	})

	t.Run("window 2048 is below the floor", func(t *testing.T) {
		specs, err := LoadTOML(writeTOML(t, `
[[models]]
name = "qwen"
protocol = "openai"
endpoint = "http://127.0.0.1:8000/v1"
model = "Qwen3.8-27B"
context_window = 2048
`))
		if err != nil {
			t.Fatalf("LoadTOML: %v", err)
		}
		if err := ValidateSpecs(specs, nil); err == nil || !strings.Contains(err.Error(), "4096") {
			t.Fatalf("want a window-floor error, got %v", err)
		}
	})

	t.Run("entry without a name", func(t *testing.T) {
		specs, err := LoadTOML(writeTOML(t, `
[[models]]
protocol = "openai"
model = "m"
context_window = 8192
`))
		if err != nil {
			t.Fatalf("LoadTOML: %v", err)
		}
		if err := ValidateSpecs(specs, nil); err == nil || !strings.Contains(err.Error(), "name") {
			t.Fatalf("want a name error, got %v", err)
		}
	})
}

func TestValidateSpecs(t *testing.T) {
	valid := func() ModelSpec {
		return ModelSpec{Name: "qwen", Protocol: ProtocolOpenAI, Model: "Qwen3.8-27B", ContextWindow: 262144}
	}

	t.Run("valid spec passes", func(t *testing.T) {
		if err := ValidateSpecs([]ModelSpec{valid()}, nil); err != nil {
			t.Fatalf("ValidateSpecs: %v", err)
		}
	})

	t.Run("reserved name is rejected with the set named", func(t *testing.T) {
		for _, name := range []string{"adjudicator", "strict", "business", "codeflow"} {
			s := valid()
			s.Name = name
			err := ValidateSpecs([]ModelSpec{s}, nil)
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("name %q: want a reserved-name error naming the set, got %v", name, err)
			}
			for _, other := range []string{"adjudicator", "strict", "business", "codeflow"} {
				if !strings.Contains(err.Error(), other) {
					t.Fatalf("name %q: error should name the whole reserved set, got %v", name, err)
				}
			}
		}
	})

	t.Run("name grammar", func(t *testing.T) {
		for _, name := range []string{"", "Strict", "bad_name", "with space", "UPPER-1"} {
			s := valid()
			s.Name = name
			if err := ValidateSpecs([]ModelSpec{s}, nil); err == nil {
				t.Fatalf("name %q: want an error", name)
			}
		}
	})

	t.Run("unknown protocol", func(t *testing.T) {
		s := valid()
		s.Protocol = "mystery"
		if err := ValidateSpecs([]ModelSpec{s}, nil); err == nil || !strings.Contains(err.Error(), "protocol") {
			t.Fatalf("want a protocol error, got %v", err)
		}
	})

	t.Run("empty protocol", func(t *testing.T) {
		s := valid()
		s.Protocol = ""
		if err := ValidateSpecs([]ModelSpec{s}, nil); err == nil {
			t.Fatal("want a protocol error for an empty protocol")
		}
	})

	t.Run("gemini with a custom endpoint is rejected", func(t *testing.T) {
		s := valid()
		s.Protocol = ProtocolGemini
		s.Endpoint = "http://127.0.0.1:8999"
		err := ValidateSpecs([]ModelSpec{s}, nil)
		if err == nil || !strings.Contains(err.Error(), "gemini") || !strings.Contains(err.Error(), "endpoint") {
			t.Fatalf("want a gemini+endpoint rejection, got %v", err)
		}
	})

	t.Run("empty model id", func(t *testing.T) {
		s := valid()
		s.Model = ""
		if err := ValidateSpecs([]ModelSpec{s}, nil); err == nil || !strings.Contains(err.Error(), "model") {
			t.Fatalf("want a model-id error, got %v", err)
		}
	})

	t.Run("one-sided price", func(t *testing.T) {
		p := 1.0
		s := valid()
		s.PriceIn = &p
		if err := ValidateSpecs([]ModelSpec{s}, nil); err == nil || !strings.Contains(err.Error(), "price") {
			t.Fatalf("want a price-pair error, got %v", err)
		}
	})

	t.Run("price pair passes", func(t *testing.T) {
		one, two := 1.0, 2.0
		s := valid()
		s.PriceIn, s.PriceOut = &one, &two
		if err := ValidateSpecs([]ModelSpec{s}, nil); err != nil {
			t.Fatalf("a complete price pair should pass, got %v", err)
		}
	})

	t.Run("window 2048 is rejected", func(t *testing.T) {
		s := valid()
		s.ContextWindow = 2048
		if err := ValidateSpecs([]ModelSpec{s}, nil); err == nil || !strings.Contains(err.Error(), "4096") {
			t.Fatalf("want a window-floor error, got %v", err)
		}
	})

	t.Run("preset window override below the floor is rejected", func(t *testing.T) {
		s := valid()
		s.Name = "claude"
		s.ContextWindow = 4095
		presets := map[string]bool{"claude": true}
		if err := ValidateSpecs([]ModelSpec{s}, presets); err == nil {
			t.Fatal("want a window-floor error even for a preset-name override")
		}
	})
}

// TestMergeSpecs pins the per-field flag > file > preset rule and the result
// ordering: presets first, then custom names in declaration order, then
// flag-only names.
func TestMergeSpecs(t *testing.T) {
	t.Run("file redefines only the claude model", func(t *testing.T) {
		file := []ModelSpec{{Name: "claude", Model: "claude-sonnet-4-5"}}
		merged := MergeSpecs([]ModelSpec{PresetClaude}, file)
		if len(merged) != 1 {
			t.Fatalf("want 1 merged spec, got %d", len(merged))
		}
		got := merged[0]
		if got.Model != "claude-sonnet-4-5" {
			t.Errorf("Model = %q, want the file value", got.Model)
		}
		if got.ContextWindow != 128000 {
			t.Errorf("ContextWindow = %d, want the preset 128000 preserved", got.ContextWindow)
		}
		if got.Protocol != ProtocolAnthropic || got.APIKey != "" || got.Endpoint != "" {
			t.Errorf("unset fields should keep the preset values: %+v", got)
		}
	})

	t.Run("flag redefines the model of a file spec", func(t *testing.T) {
		file := []ModelSpec{{
			Name: "qwen", Protocol: ProtocolOpenAI,
			Endpoint: "http://127.0.0.1:8000/v1", APIKey: "file-key",
			Model: "m1", ContextWindow: 8192,
		}}
		flag := []ModelSpec{{Name: "qwen", Model: "m2"}}
		merged := MergeSpecs(file, flag)
		if len(merged) != 1 {
			t.Fatalf("want 1 merged spec, got %d", len(merged))
		}
		got := merged[0]
		if got.Model != "m2" {
			t.Errorf("Model = %q, want the flag value m2", got.Model)
		}
		if got.Endpoint != "http://127.0.0.1:8000/v1" || got.APIKey != "file-key" {
			t.Errorf("file endpoint/key should be preserved: %+v", got)
		}
		if got.ContextWindow != 8192 || got.Protocol != ProtocolOpenAI {
			t.Errorf("file protocol/window should be preserved: %+v", got)
		}
	})

	t.Run("flag over file over preset matrix", func(t *testing.T) {
		fileIn, fileOut := 1.0, 2.0
		file := []ModelSpec{{Name: "claude", Model: "file-model", ContextWindow: 16384, PriceIn: &fileIn, PriceOut: &fileOut}}
		flag := []ModelSpec{{Name: "claude", Model: "flag-model", ContextWindow: 32768}}
		merged := MergeSpecs([]ModelSpec{PresetClaude}, file, flag)
		got := merged[0]
		if got.Model != "flag-model" {
			t.Errorf("Model = %q, want flag-model (flag > file > preset)", got.Model)
		}
		if got.ContextWindow != 32768 {
			t.Errorf("ContextWindow = %d, want 32768 (flag > file)", got.ContextWindow)
		}
		if got.PriceIn == nil || got.PriceOut == nil {
			t.Errorf("price pair should be preserved from the file layer: %+v", got)
		}
		if got.Protocol != ProtocolAnthropic {
			t.Errorf("Protocol = %q, want the preset protocol preserved", got.Protocol)
		}
	})

	t.Run("price pair split across layers", func(t *testing.T) {
		fileIn := 1.0
		flagOut := 2.0
		file := []ModelSpec{{Name: "qwen", Protocol: ProtocolOpenAI, Model: "m", ContextWindow: 8192, PriceIn: &fileIn}}
		flag := []ModelSpec{{Name: "qwen", PriceOut: &flagOut}}
		merged := MergeSpecs(file, flag)
		if merged[0].PriceIn == nil || merged[0].PriceOut == nil {
			t.Fatalf("both price halves should survive the merge: %+v", merged[0])
		}
	})

	t.Run("ordering: presets, then file, then flag-only", func(t *testing.T) {
		presets := []ModelSpec{PresetClaude, PresetGemini, PresetOpenAI, PresetAzure}
		file := []ModelSpec{{Name: "spec1"}, {Name: "spec2"}}
		flag := []ModelSpec{{Name: "spec1", Model: "x"}, {Name: "zeta"}}
		merged := MergeSpecs(presets, file, flag)
		var order []string
		for _, s := range merged {
			order = append(order, s.Name)
		}
		want := "claude gemini openai azure spec1 spec2 zeta"
		if got := strings.Join(order, " "); got != want {
			t.Fatalf("order = %q, want %q", got, want)
		}
	})

	t.Run("a repeated name within one layer is last-wins", func(t *testing.T) {
		flag := []ModelSpec{{Name: "qwen", Model: "m1"}, {Name: "qwen", Model: "m2"}}
		merged := MergeSpecs(flag)
		if len(merged) != 1 || merged[0].Model != "m2" {
			t.Fatalf("want last-wins [m2], got %+v", merged)
		}
	})

	t.Run("a nameless spec passes through for validation", func(t *testing.T) {
		merged := MergeSpecs([]ModelSpec{{Protocol: ProtocolOpenAI, Model: "m"}})
		if len(merged) != 1 || merged[0].Name != "" {
			t.Fatalf("want the nameless spec preserved, got %+v", merged)
		}
		if err := ValidateSpecs(merged, nil); err == nil {
			t.Fatal("validation should reject a nameless spec")
		}
	})
}
