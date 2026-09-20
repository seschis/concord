package provider

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// TestModelSpecSet covers every registry key against ModelSpec's fields, plus
// the coercion and unknown-key errors.
func TestModelSpecSet(t *testing.T) {
	vals := map[string]string{
		"name": "qwen", "protocol": "openai", "endpoint": "http://127.0.0.1:8000/v1",
		"api_key": "sk-test", "model": "Qwen3.8-27B", "context_window": "262144",
		"price_in": "1.5", "price_out": "2.5", "bedrock": "true", "region": "us-east-1",
		"api_version": "2024-12-01-preview",
	}
	var m ModelSpec
	for k, v := range vals {
		if err := m.Set(k, v); err != nil {
			t.Fatalf("Set(%q, %q): %v", k, v, err)
		}
	}
	if m.Name != "qwen" || m.Protocol != ProtocolOpenAI || m.Endpoint != "http://127.0.0.1:8000/v1" ||
		m.APIKey != "sk-test" || m.Model != "Qwen3.8-27B" || m.ContextWindow != 262144 ||
		m.PriceIn == nil || *m.PriceIn != 1.5 || m.PriceOut == nil || *m.PriceOut != 2.5 ||
		!m.Bedrock || m.Region != "us-east-1" || m.APIVersion != "2024-12-01-preview" {
		t.Fatalf("Set applied values wrongly: %+v", m)
	}

	var m2 ModelSpec
	if err := m2.Set("bogus", "1"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown key should be an error naming it, got %v", err)
	}
	for _, kv := range []struct{ key, val string }{
		{"context_window", "big"},
		{"price_in", "big"},
		{"price_out", "big"},
		{"bedrock", "maybe"},
	} {
		var m3 ModelSpec
		if err := m3.Set(kv.key, kv.val); err == nil || !strings.Contains(err.Error(), kv.key) {
			t.Fatalf("Set(%q, %q) should be a coercion error naming the key, got %v", kv.key, kv.val, err)
		}
	}
}

// TestSpecFlagKeysPinnedToModelSpec pins the spec key registry against
// ModelSpec's exported fields AND modelTable's toml tags: every field has
// exactly one key and every key names a field, so adding a spec field cannot
// desync the --add-model parser, the unknown-key error, and the TOML table.
func TestSpecFlagKeysPinnedToModelSpec(t *testing.T) {
	got := append([]string(nil), specFlagKeys...)
	sort.Strings(got)
	want := sortedKeysOf(t, ModelSpec{}, "")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("spec key registry %v out of sync with ModelSpec fields %v", got, want)
	}
	want = sortedKeysOf(t, modelTable{}, "toml")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("spec key registry %v out of sync with modelTable toml tags %v", got, want)
	}
}

// sortedKeysOf returns the sorted snake_case key of every exported field of
// the value's type: the field name itself, or the named struct tag when set.
func sortedKeysOf(t *testing.T, v any, tag string) []string {
	t.Helper()
	tm := reflect.TypeOf(v)
	out := make([]string, 0, tm.NumField())
	for i := 0; i < tm.NumField(); i++ {
		f := tm.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if tag != "" {
			out = append(out, f.Tag.Get(tag))
		} else {
			out = append(out, pascalToKey(f.Name))
		}
	}
	sort.Strings(out)
	return out
}

// pascalToKey converts a PascalCase field name to its spec key (APIKey ->
// api_key), the convention the registry and the toml tags follow. An
// underscore separates an acronym from the word after it (APIKey ->
// api_key), not between acronym letters.
func pascalToKey(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				nextIsLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if !unicode.IsUpper(prev) || nextIsLower {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
