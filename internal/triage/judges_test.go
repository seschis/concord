package triage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFocusForBuiltInPersonas(t *testing.T) {
	for _, name := range []string{"adjudicator", "strict", "business", "codeflow"} {
		focus, ok := FocusFor(name)
		if !ok {
			t.Fatalf("FocusFor(%q) should be a built-in persona", name)
		}
		if strings.TrimSpace(focus) == "" {
			t.Fatalf("FocusFor(%q) returned empty focus", name)
		}
	}
	if _, ok := FocusFor("does-not-exist"); ok {
		t.Fatalf("FocusFor should reject an unknown persona name")
	}
}

func TestBuildJudgeSystemPromptAlwaysCarriesContract(t *testing.T) {
	for name, focus := range builtInPersonas {
		prompt := BuildJudgeSystemPrompt(focus)
		for _, want := range []string{`"final_verdict"`, `"key_deciding_factor"`, "ONLY a valid JSON object"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("persona %q prompt missing contract element %q", name, want)
			}
		}
	}
}

func TestDefaultJudgePromptMatchesLegacyConstant(t *testing.T) {
	focus, ok := FocusFor("adjudicator")
	if !ok {
		t.Fatalf("adjudicator persona must exist")
	}
	if got := BuildJudgeSystemPrompt(focus); got != AdjudicationSystemPrompt {
		t.Fatalf("default judge prompt drifted from AdjudicationSystemPrompt:\n-- got --\n%s\n-- want --\n%s", got, AdjudicationSystemPrompt)
	}
}

func TestAdjudicationResultJudgeFields(t *testing.T) {
	with := AdjudicationResult{Judge: "strict", Model: "claude-opus-5", FinalVerdict: Unlikely}
	b, err := json.Marshal(with)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["judge"] != "strict" || m["model"] != "claude-opus-5" {
		t.Fatalf("Judge/Model should serialize, got %s", b)
	}

	without := AdjudicationResult{FinalVerdict: Unlikely}
	b2, _ := json.Marshal(without)
	if strings.Contains(string(b2), `"judge"`) || strings.Contains(string(b2), `"model"`) {
		t.Fatalf("Judge/Model should be omitted when empty, got %s", b2)
	}
}
