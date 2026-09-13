package agent

import (
	"testing"

	"github.com/tmc/langchaingo/llms"
)

// TestCollapseMultiChoice mirrors Anthropic's shape: a thinking block with empty
// content, a separate tool_use choice, then the text choice. Usage is identical
// across choices and must be read once, not summed.
func TestCollapseMultiChoice(t *testing.T) {
	gi := map[string]any{"InputTokens": 100, "OutputTokens": 50}
	resp := &llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{Content: "", GenerationInfo: gi}, // thinking block
			{ToolCalls: []llms.ToolCall{{ID: "t1", FunctionCall: &llms.FunctionCall{Name: "read_file"}}}, GenerationInfo: gi},
			{Content: "final answer", GenerationInfo: gi}, // text block
		},
	}
	text, tools, u := Collapse(resp)
	if text != "final answer" {
		t.Fatalf("text = %q, want %q", text, "final answer")
	}
	if len(tools) != 1 || tools[0].FunctionCall.Name != "read_file" {
		t.Fatalf("tool calls not aggregated: %+v", tools)
	}
	if u.In != 100 || u.Out != 50 {
		t.Fatalf("usage should be read once, got in=%d out=%d (want 100/50)", u.In, u.Out)
	}
}

// TestCollapseCacheTokens verifies the cache-token keys the anthropic-sdk-go
// adapter emits are read into Usage.
func TestCollapseCacheTokens(t *testing.T) {
	gi := map[string]any{
		"InputTokens": 10, "OutputTokens": 5,
		"CacheCreationInputTokens": 200, "CacheReadInputTokens": 4096,
	}
	resp := &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: "ok", GenerationInfo: gi}}}
	_, _, u := Collapse(resp)
	if u.CacheWrite != 200 || u.CacheRead != 4096 {
		t.Fatalf("cache tokens = write %d read %d, want 200/4096", u.CacheWrite, u.CacheRead)
	}
}
