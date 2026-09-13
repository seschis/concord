package main

import (
	"testing"

	"github.com/seschis/concord/internal/provider"
)

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
	claude, err := provider.NewClaude("", "claude-fake", 100)
	if err != nil {
		t.Fatalf("NewClaude should build without a key: %v", err)
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
	claude, err := provider.NewClaude("", "claude-fake", 100)
	if err != nil {
		t.Fatalf("NewClaude: %v", err)
	}
	// A non-empty (fake) token lets the constructor build offline; no model call
	// is made, so this stays a pure wiring test.
	codex, err := provider.NewCodex("sk-test-fake", "gpt-fake", 100)
	if err != nil {
		t.Fatalf("NewCodex: %v", err)
	}
	voters := []provider.Provider{claude, codex}

	// "nope" is not a built-in persona and has no file -> skipped. "strict" uses
	// the default model; "business" is overridden to codex.
	judges, names := buildJudges(voters, &config{
		judges:      []string{"nope", "strict", "business"},
		judgeModels: []string{"business=codex"},
	})
	if len(judges) != 2 {
		t.Fatalf("want 2 judges (unknown skipped), got %d: %v", len(judges), names)
	}
	if names[0] != "strict" || names[1] != "business" {
		t.Fatalf("names = %v, want [strict business]", names)
	}
	if judges[0].Model() != "claude-fake" {
		t.Fatalf("strict should use the default model, got %q", judges[0].Model())
	}
	if judges[1].Model() != "gpt-fake" {
		t.Fatalf("business should be overridden to codex (gpt-fake), got %q", judges[1].Model())
	}
}
