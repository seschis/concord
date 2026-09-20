package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

// scriptedModel returns one canned *ContentResponse per call, in order.
type scriptedModel struct {
	responses []*llms.ContentResponse
	calls     int
}

func (m *scriptedModel) GenerateContent(_ context.Context, _ []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	if m.calls >= len(m.responses) {
		return nil, errors.New("scriptedModel: no more responses")
	}
	r := m.responses[m.calls]
	m.calls++
	return r, nil
}

func (m *scriptedModel) Call(_ context.Context, _ string, _ ...llms.CallOption) (string, error) {
	return "", errors.New("not implemented")
}

func toolCallResponse(name, args string) *llms.ContentResponse {
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{
		ToolCalls: []llms.ToolCall{{
			ID:           "call-1",
			FunctionCall: &llms.FunctionCall{Name: name, Arguments: args},
		}},
		GenerationInfo: map[string]any{"InputTokens": 10, "OutputTokens": 5},
	}}}
}

func textResponse(text string) *llms.ContentResponse {
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{
		Content:        text,
		GenerationInfo: map[string]any{"InputTokens": 10, "OutputTokens": 5},
	}}}
}

func TestRunToolLoopReportsToolCallsAndResultOnStep(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("read_file", `{"path":"a.go"}`),
		textResponse("done"),
	}}
	exec := func(name, argsJSON string) string { return "file contents of a.go" }

	var steps []Step
	_, err := RunToolLoop(context.Background(), LoopOptions{
		Model: model, ModelName: "fake", System: "sys", User: "user",
		Tools: nil, Exec: exec, MaxIters: 4,
		OnStep: func(s Step) { steps = append(steps, s) },
	})
	if err != nil {
		t.Fatalf("RunToolLoop: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps (tool call + final), got %d: %+v", len(steps), steps)
	}

	toolStep := steps[0]
	if toolStep.Final {
		t.Errorf("first step should not be Final")
	}
	if len(toolStep.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call recorded, got %d", len(toolStep.ToolCalls))
	}
	got := toolStep.ToolCalls[0]
	want := ToolCall{Tool: "read_file", Args: `{"path":"a.go"}`, Result: "file contents of a.go"}
	if got != want {
		t.Errorf("tool call mismatch: got %+v, want %+v", got, want)
	}
	// Backward-compatible summary fields (used by the live progress display)
	// must still mirror the first tool call.
	if toolStep.Tool != "read_file" || toolStep.Args != `{"path":"a.go"}` || toolStep.ToolCount != 1 {
		t.Errorf("summary fields mismatch: %+v", toolStep)
	}

	finalStep := steps[1]
	if !finalStep.Final || finalStep.Text != "done" {
		t.Errorf("final step mismatch: %+v", finalStep)
	}
}

func TestRunToolLoopBudgetExhaustedStepIsFinalWithText(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("list_dir", `{"path":"."}`),
		textResponse("forced answer"),
	}}
	exec := func(name, argsJSON string) string { return "a/\nb/\n" }

	var steps []Step
	res, err := RunToolLoop(context.Background(), LoopOptions{
		Model: model, ModelName: "fake", System: "sys", User: "user",
		Tools: nil, Exec: exec, MaxIters: 1,
		OnStep: func(s Step) { steps = append(steps, s) },
	})
	if err != nil {
		t.Fatalf("RunToolLoop: %v", err)
	}
	if res.FinalText != "forced answer" {
		t.Errorf("want forced final answer, got %q", res.FinalText)
	}
	last := steps[len(steps)-1]
	if !last.Final || last.Text != "forced answer" {
		t.Errorf("last step should be Final with the forced answer's text: %+v", last)
	}
}

// fiveReadsModel scripts a loop of five 1000-char file reads (all below the
// 1600-char trim floor) followed by a final answer. By the final call the
// history holds ~5092 chars (~1273 estimated tokens): past 80% of a 1000-token
// window, a rounding error away from a 100000-token one.
func fiveReadsModel() *capturingModel {
	resps := make([]*llms.ContentResponse, 0, 6)
	for i := 1; i <= 5; i++ {
		resps = append(resps, toolCallResponse("read_file", fmt.Sprintf(`{"path":"f%d.txt"}`, i)))
	}
	resps = append(resps, textResponse("done"))
	return &capturingModel{responses: resps}
}

func thousandCharReadExec(string, string) string { return strings.Repeat("r", 1000) }

// TestLoopWindowedPerCallPruneInstance: the loop derives its own pruner
// instance carrying ContextWindow. The shared run-scoped pruner is
// window-blind, so only the loop's own window can pressure-trim the stale
// 1000-char results (below the trim floor).
func TestLoopWindowedPerCallPruneInstance(t *testing.T) {
	shared := BreadcrumbPruner{RecentWindow: 2, MinTrimChars: 1600, HeadChars: 600}
	model := fiveReadsModel()

	_, err := RunToolLoop(context.Background(), LoopOptions{
		Model: model, ModelName: "fake", System: "sys", User: "user",
		Exec: thousandCharReadExec, MaxIters: 8,
		Pruner: shared, ContextWindow: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	last := model.seen[len(model.seen)-1]
	res := toolResults(last)
	if !strings.Contains(res[0].Content, "elided to save context") {
		t.Errorf("loop must prune with its own windowed instance; oldest result arrived full")
	}
	if strings.Contains(res[len(res)-1].Content, "elided") {
		t.Errorf("most recent result should stay full, was trimmed")
	}
	if shared.Window != 0 {
		t.Errorf("the shared run-scoped pruner must not be mutated by the loop; Window=%d", shared.Window)
	}
}

// TestLoopWindowZeroKeepsWindowBlindPrune: with ContextWindow 0 the loop sends
// today's exact view — the shared pruner's floor applies, no window pressure.
func TestLoopWindowZeroKeepsWindowBlindPrune(t *testing.T) {
	shared := BreadcrumbPruner{RecentWindow: 2, MinTrimChars: 1600, HeadChars: 600}
	model := fiveReadsModel()

	_, err := RunToolLoop(context.Background(), LoopOptions{
		Model: model, ModelName: "fake", System: "sys", User: "user",
		Exec: thousandCharReadExec, MaxIters: 8,
		Pruner: shared, // ContextWindow left 0
	})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("r", 1000)
	for i, r := range toolResults(model.seen[len(model.seen)-1]) {
		if r.Content != body {
			t.Errorf("ContextWindow 0 must keep today's exact view; result %d altered", i)
		}
	}
}

// TestLoopPerLoopWindowIsolation: two loops with different windows share one
// run-scoped pruner, the way concurrent voter loops share the context
// instance. Each loop's trims must reflect only its own window.
func TestLoopPerLoopWindowIsolation(t *testing.T) {
	shared := BreadcrumbPruner{RecentWindow: 2, MinTrimChars: 1600, HeadChars: 600}
	small, big := fiveReadsModel(), fiveReadsModel()

	var (
		wg   sync.WaitGroup
		errs = make([]error, 2)
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = RunToolLoop(context.Background(), LoopOptions{
			Model: small, ModelName: "small", System: "sys", User: "user",
			Exec: thousandCharReadExec, MaxIters: 8,
			Pruner: shared, ContextWindow: 1000,
		})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = RunToolLoop(context.Background(), LoopOptions{
			Model: big, ModelName: "big", System: "sys", User: "user",
			Exec: thousandCharReadExec, MaxIters: 8,
			Pruner: shared, ContextWindow: 100000,
		})
	}()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("concurrent loops failed: %v %v", errs[0], errs[1])
	}

	body := strings.Repeat("r", 1000)
	smallLast := small.seen[len(small.seen)-1]
	if !strings.Contains(toolResults(smallLast)[0].Content, "elided to save context") {
		t.Errorf("small-window loop must pressure-trim its stale result")
	}
	bigLast := big.seen[len(big.seen)-1]
	for i, r := range toolResults(bigLast) {
		if r.Content != body {
			t.Errorf("big-window loop must keep stale result %d byte-identical", i)
		}
	}
	if shared.Window != 0 {
		t.Errorf("shared run-scoped pruner was mutated by the loops; Window=%d", shared.Window)
	}
}
