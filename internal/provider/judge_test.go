package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/tmc/langchaingo/llms"

	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/progress"
	"github.com/seschis/concord/internal/triage"
)

// judgeScriptedModel is a scripted llms.Model that records the
// []llms.MessageContent it receives, so tests can assert on prompt construction.
type judgeScriptedModel struct {
	responses []*llms.ContentResponse
	msgs      []llms.MessageContent
	calls     int
}

func (m *judgeScriptedModel) GenerateContent(_ context.Context, msgs []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	m.msgs = msgs
	if m.calls >= len(m.responses) {
		return nil, errors.New("judgeScriptedModel: no more responses")
	}
	r := m.responses[m.calls]
	m.calls++
	return r, nil
}

func (m *judgeScriptedModel) Call(_ context.Context, _ string, _ ...llms.CallOption) (string, error) {
	return "", errors.New("not implemented")
}

func adjudicationResponse(text string) *llms.ContentResponse {
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{
		Content:        text,
		GenerationInfo: map[string]any{"InputTokens": 10, "OutputTokens": 5},
	}}}
}

const adjudicationJSON = `{"final_verdict":"UNLIKELY","final_reasoning":"no taint","key_deciding_factor":"sink missing"}`

func TestNewJudge(t *testing.T) {
	lp := &LLMProvider{name: "claude", model: "claude-fake"}
	j := NewJudge("strict", lp, "MY_SYSTEM_PROMPT")
	if j.Name() != "strict" {
		t.Errorf("Name() = %q, want %q", j.Name(), "strict")
	}
	if j.Model() != "claude-fake" {
		t.Errorf("Model() = %q, want %q", j.Model(), "claude-fake")
	}
}

func TestJudgeAdjudicateDelegatesToProvider(t *testing.T) {
	model := &judgeScriptedModel{responses: []*llms.ContentResponse{
		adjudicationResponse(adjudicationJSON),
	}}
	lp := &LLMProvider{
		llm:       model,
		name:      "claude",
		model:     "claude-fake",
		maxTokens: 100,
		cost:      func(string, int, int, int, int) float64 { return 0.5 },
	}
	j := NewJudge("strict", lp, "MY_SYSTEM_PROMPT")

	sink := &recordingSink{}
	ctx := progress.WithSink(context.Background(), sink)
	res, err := j.Adjudicate(ctx, finding.Finding{ID: "F1"},
		[]triage.Result{{Provider: "claude", FinalVerdict: triage.ConfirmedReal}}, "low")
	if err != nil {
		t.Fatalf("Adjudicate: %v", err)
	}
	if res.FinalVerdict != triage.Unlikely {
		t.Errorf("FinalVerdict = %q, want %q", res.FinalVerdict, triage.Unlikely)
	}
	if res.Reasoning != "no taint" {
		t.Errorf("Reasoning = %q, want %q", res.Reasoning, "no taint")
	}
	if res.CostUSD != 0.5 {
		t.Errorf("CostUSD = %v, want 0.5", res.CostUSD)
	}

	// The judge's own system prompt must be sent, not a default.
	var sawJudgeSystem bool
	for _, mc := range model.msgs {
		if mc.Role != llms.ChatMessageTypeSystem {
			continue
		}
		for _, part := range mc.Parts {
			if tc, ok := part.(llms.TextContent); ok && tc.Text == "MY_SYSTEM_PROMPT" {
				sawJudgeSystem = true
			}
		}
	}
	if !sawJudgeSystem {
		t.Errorf("system message did not carry the judge's system prompt %q", "MY_SYSTEM_PROMPT")
	}

	// Progress must be labeled with the judge's name, not the model backend's.
	strictEvents := 0
	for _, ev := range sink.events {
		if ev.Provider == "strict" && ev.Role == progress.RoleAdjudicator {
			strictEvents++
		}
	}
	if strictEvents < 2 {
		t.Errorf("want >=2 events with Provider=strict Role=adjudicator (Action + ModelDone), got %d: %+v", strictEvents, sink.events)
	}
}

func TestLLMProviderAdjudicateBackCompat(t *testing.T) {
	model := &judgeScriptedModel{responses: []*llms.ContentResponse{
		adjudicationResponse(adjudicationJSON),
	}}
	lp := &LLMProvider{
		llm:       model,
		name:      "claude",
		model:     "claude-fake",
		maxTokens: 100,
		cost:      func(string, int, int, int, int) float64 { return 0.5 },
	}

	sink := &recordingSink{}
	ctx := progress.WithSink(context.Background(), sink)
	res, err := lp.Adjudicate(ctx, finding.Finding{ID: "F1"},
		[]triage.Result{{Provider: "claude", FinalVerdict: triage.ConfirmedReal}}, "low")
	if err != nil {
		t.Fatalf("Adjudicate: %v", err)
	}
	if res.FinalVerdict != triage.Unlikely {
		t.Errorf("FinalVerdict = %q, want %q", res.FinalVerdict, triage.Unlikely)
	}

	var sawProviderEvent bool
	for _, ev := range sink.events {
		if ev.Provider == "claude" && ev.Role == progress.RoleAdjudicator {
			sawProviderEvent = true
		}
	}
	if !sawProviderEvent {
		t.Errorf("no progress events with Provider=claude Role=adjudicator: %+v", sink.events)
	}
}
