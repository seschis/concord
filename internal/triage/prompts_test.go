package triage

import (
	"strings"
	"testing"
)

func TestBuildAnalystSystemPrompt(t *testing.T) {
	got := BuildAnalystSystemPrompt("You are a skeptical, evidence-first reviewer.")

	// It must start with the verbatim base triage prompt so the analyst still
	// performs the full three-part analysis.
	if !strings.HasPrefix(got, SystemPrompt) {
		t.Fatalf("analyst prompt must start with the base triage prompt")
	}
	// It must carry the lens framing and the persona focus.
	if !strings.Contains(got, "LENS:") {
		t.Fatalf("analyst prompt must carry the lens header, got %q", got)
	}
	if !strings.Contains(got, "skeptical, evidence-first reviewer") {
		t.Fatalf("analyst prompt must include the persona focus, got %q", got)
	}
	// The base prompt's JSON output contract must survive so analyst output is
	// still parseable and comparable across the panel.
	if !strings.Contains(got, "part1_reachability") {
		t.Fatalf("analyst prompt must retain the base JSON output contract")
	}
}

// Two different lenses on the same base prompt must differ, which is what makes
// a same-model analyst panel produce genuinely distinct analyses.
func TestBuildAnalystSystemPromptLensDiffer(t *testing.T) {
	a := BuildAnalystSystemPrompt("Lens A.")
	b := BuildAnalystSystemPrompt("Lens B.")
	if a == b {
		t.Fatalf("different lenses must yield different analyst prompts")
	}
}
