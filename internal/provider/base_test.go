package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/tmc/langchaingo/llms"

	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/triage"
)

// recordingModel is a scripted llms.Model that records the CallOptions of
// every GenerateContent call, so a test can assert the exact output budget a
// path requested from the model.
type recordingModel struct {
	responses []*llms.ContentResponse
	opts      []llms.CallOptions
	calls     int
}

func (m *recordingModel) GenerateContent(_ context.Context, _ []llms.MessageContent, opts ...llms.CallOption) (*llms.ContentResponse, error) {
	var o llms.CallOptions
	for _, opt := range opts {
		opt(&o)
	}
	m.opts = append(m.opts, o)
	if m.calls >= len(m.responses) {
		return nil, errors.New("recordingModel: no more responses")
	}
	r := m.responses[m.calls]
	m.calls++
	return r, nil
}

func (m *recordingModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", errors.New("not implemented")
}

// observedMaxTokens returns the MaxTokens value of every recorded call.
func (m *recordingModel) observedMaxTokens() []int {
	out := make([]int, len(m.opts))
	for i, o := range m.opts {
		out[i] = o.MaxTokens
	}
	return out
}

func modelTextResponse(text string) *llms.ContentResponse {
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{
		Content:        text,
		GenerationInfo: map[string]any{"InputTokens": 10, "OutputTokens": 5},
	}}}
}

// providerWith builds a provider on a scripted model with the given window and
// global output budget, the shape NewFromSpec produces.
func providerWith(model llms.Model, window, maxTokens int) *LLMProvider {
	return &LLMProvider{
		llm: model, name: "claude", model: "claude-fake",
		maxTokens: maxTokens, contextWindow: window,
		cost: func(string, int, int, int, int) float64 { return 0 },
	}
}

// TestSingleShotClampsMaxTokensToHalfWindow: with an 8192-token window the
// 16000-token global budget must arrive at the model as 4096.
func TestSingleShotClampsMaxTokensToHalfWindow(t *testing.T) {
	model := &recordingModel{responses: []*llms.ContentResponse{modelTextResponse("verdict")}}
	p := providerWith(model, 8192, 16000)

	_, err := p.Analyze(context.Background(), finding.Finding{ID: "F1"}, AnalyzeInput{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := model.observedMaxTokens(); len(got) != 1 || got[0] != 4096 {
		t.Errorf("single-shot path must request WithMaxTokens 4096 (min(16000, 8192/2)), observed %v", got)
	}
}

// TestAdjudicateClampsMaxTokensToHalfWindow: the same clamp must hold on the
// adjudication path.
func TestAdjudicateClampsMaxTokensToHalfWindow(t *testing.T) {
	model := &recordingModel{responses: []*llms.ContentResponse{modelTextResponse("verdict")}}
	p := providerWith(model, 8192, 16000)

	_, err := p.AdjudicateAs(context.Background(), finding.Finding{ID: "F1"},
		[]triage.Result{}, "strict", "system", "low")
	if err != nil {
		t.Fatalf("AdjudicateAs: %v", err)
	}
	if got := model.observedMaxTokens(); len(got) != 1 || got[0] != 4096 {
		t.Errorf("adjudicate path must request WithMaxTokens 4096 (min(16000, 8192/2)), observed %v", got)
	}
}

// TestAgenticLoopClampsMaxTokensToHalfWindow: a small 4096-token window caps
// the agentic loop's output budget at 2048.
func TestAgenticLoopClampsMaxTokensToHalfWindow(t *testing.T) {
	model := &recordingModel{responses: []*llms.ContentResponse{modelTextResponse("verdict")}}
	p := providerWith(model, 4096, 16000)
	srcRoot := t.TempDir()

	_, err := p.Analyze(context.Background(), finding.Finding{ID: "F1"},
		AnalyzeInput{Tools: &ToolEnv{SrcRoot: srcRoot}})
	if err != nil {
		t.Fatalf("Analyze (agentic): %v", err)
	}
	if got := model.observedMaxTokens(); len(got) != 1 || got[0] != 2048 {
		t.Errorf("agentic loop must request WithMaxTokens 2048 (min(16000, 4096/2)), observed %v", got)
	}
}

// TestGatherClampsExplorerCapToHalfWindow: the explorer's fixed 4000 cap is
// clamped by half the window when that is smaller.
func TestGatherClampsExplorerCapToHalfWindow(t *testing.T) {
	cases := []struct {
		window int
		want   int
	}{
		{8192, 4000},   // min(4000, 4096) keeps the 4000 cap
		{4096, 2048},   // min(4000, 2048) caps at the half window
		{128000, 4000}, // preset default: the 4000 cap is unchanged
		{0, 4000},      // unknown window: no clamp
	}
	for _, tc := range cases {
		model := &recordingModel{responses: []*llms.ContentResponse{modelTextResponse("brief")}}
		p := providerWith(model, tc.window, 16000)
		srcRoot := t.TempDir()

		_, _, err := p.Gather(context.Background(), finding.Finding{ID: "F1"}, srcRoot, nil, "low")
		if err != nil {
			t.Fatalf("Gather (window %d): %v", tc.window, err)
		}
		if got := model.observedMaxTokens(); len(got) != 1 || got[0] != tc.want {
			t.Errorf("explorer with window %d must request WithMaxTokens %d, observed %v", tc.window, tc.want, got)
		}
	}
}

// TestNoOpDefaultWindowIsByteIdentical: the preset default window (128000)
// must leave the 16000-token budget untouched, so presets-only runs keep
// today's exact requests.
func TestNoOpDefaultWindowIsByteIdentical(t *testing.T) {
	single := &recordingModel{responses: []*llms.ContentResponse{modelTextResponse("verdict")}}
	pSingle := providerWith(single, 128000, 16000)
	if _, err := pSingle.Analyze(context.Background(), finding.Finding{ID: "F1"}, AnalyzeInput{}); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := single.observedMaxTokens(); len(got) != 1 || got[0] != 16000 {
		t.Errorf("window 128000 must not clamp the 16000 budget, observed %v", got)
	}

	adj := &recordingModel{responses: []*llms.ContentResponse{modelTextResponse("verdict")}}
	pAdj := providerWith(adj, 128000, 16000)
	if _, err := pAdj.AdjudicateAs(context.Background(), finding.Finding{ID: "F1"},
		[]triage.Result{}, "claude", "system", "low"); err != nil {
		t.Fatalf("AdjudicateAs: %v", err)
	}
	if got := adj.observedMaxTokens(); len(got) != 1 || got[0] != 16000 {
		t.Errorf("window 128000 must not clamp the adjudicate budget, observed %v", got)
	}

	loop := &recordingModel{responses: []*llms.ContentResponse{modelTextResponse("verdict")}}
	pLoop := providerWith(loop, 128000, 16000)
	srcRoot := t.TempDir()
	if _, err := pLoop.Analyze(context.Background(), finding.Finding{ID: "F1"},
		AnalyzeInput{Tools: &ToolEnv{SrcRoot: srcRoot}}); err != nil {
		t.Fatalf("Analyze (agentic): %v", err)
	}
	if got := loop.observedMaxTokens(); len(got) != 1 || got[0] != 16000 {
		t.Errorf("window 128000 must not clamp the agentic budget, observed %v", got)
	}
}

// TestWindowClampRespectsGlobalBudgetBelowHalfWindow: when the global budget
// is already below half the window, the clamp must not raise it.
func TestWindowClampRespectsGlobalBudgetBelowHalfWindow(t *testing.T) {
	model := &recordingModel{responses: []*llms.ContentResponse{modelTextResponse("verdict")}}
	p := providerWith(model, 128000, 4000)
	if _, err := p.Analyze(context.Background(), finding.Finding{ID: "F1"}, AnalyzeInput{}); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := model.observedMaxTokens(); len(got) != 1 || got[0] != 4000 {
		t.Errorf("clamp must never raise the budget above --max-tokens, observed %v", got)
	}
}
