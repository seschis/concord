package agent

import (
	"context"
	"errors"
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
