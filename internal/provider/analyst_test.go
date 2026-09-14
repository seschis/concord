package provider

import (
	"strings"
	"testing"

	"github.com/seschis/concord/internal/triage"
)

// NewAnalyst must inherit the base provider's model and pricing while taking a
// distinct label and a persona-lens system prompt (so it votes as its own voter).
func TestNewAnalyst(t *testing.T) {
	base, err := NewClaude("", "claude-fake", 100)
	if err != nil {
		t.Fatalf("NewClaude should build without a key: %v", err)
	}

	a := NewAnalyst(base, "strict", "a skeptical, evidence-first lens")

	if a.Name() != "strict" {
		t.Fatalf("want label strict, got %q", a.Name())
	}
	if a.Model() != "claude-fake" {
		t.Fatalf("analyst should inherit the base model, got %q", a.Model())
	}
	if a.maxTokens != base.maxTokens {
		t.Fatalf("analyst should inherit the base maxTokens, got %d", a.maxTokens)
	}

	// The analyst's Analyze system prompt must be the persona lens, not the bare
	// default — and it must keep the base triage contract so output is comparable.
	if a.system == "" {
		t.Fatalf("analyst must carry a system-prompt override")
	}
	if a.system == base.system {
		t.Fatalf("analyst system prompt should differ from the base")
	}
	if !strings.Contains(a.system, "skeptical, evidence-first lens") {
		t.Fatalf("analyst system prompt must include the lens, got %q", a.system)
	}
	if !strings.Contains(a.system, "part1_reachability") {
		t.Fatalf("analyst system prompt must retain the base JSON contract")
	}
}

// A plain model provider (no lens) keeps the default triage prompt.
func TestPlainProviderUsesDefaultSystemPrompt(t *testing.T) {
	base, err := NewClaude("", "claude-fake", 100)
	if err != nil {
		t.Fatalf("NewClaude: %v", err)
	}
	if base.system != "" {
		t.Fatalf("a plain provider must not carry a system override")
	}
	if base.systemPrompt() != triage.SystemPrompt {
		t.Fatalf("plain provider should fall back to the default triage prompt")
	}
}
