package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

// buildHistory returns a system+user prefix followed by n (assistant read_file,
// tool result) turns. Each result is `size` bytes so trimming can be exercised.
func buildHistory(n, size int) []llms.MessageContent {
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "find the bug"),
	}
	body := strings.Repeat("x", size)
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		path := "file" + id + ".java"
		asst := llms.MessageContent{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{
			llms.ToolCall{ID: id, Type: "function", FunctionCall: &llms.FunctionCall{
				Name: "read_file", Arguments: `{"path":"` + path + `"}`,
			}},
		}}
		tool := llms.MessageContent{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{
			llms.ToolCallResponse{ToolCallID: id, Name: "read_file", Content: body},
		}}
		msgs = append(msgs, asst, tool)
	}
	return msgs
}

func toolResults(msgs []llms.MessageContent) []llms.ToolCallResponse {
	var out []llms.ToolCallResponse
	for _, m := range msgs {
		if m.Role != llms.ChatMessageTypeTool {
			continue
		}
		for _, p := range m.Parts {
			if r, ok := p.(llms.ToolCallResponse); ok {
				out = append(out, r)
			}
		}
	}
	return out
}

func TestNoPrunePassesThrough(t *testing.T) {
	msgs := buildHistory(10, 4000)
	out := NoPrune{}.Prune(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("NoPrune changed message count: %d -> %d", len(msgs), len(out))
	}
	for i := range msgs {
		if len(out[i].Parts) != len(msgs[i].Parts) {
			t.Fatalf("NoPrune altered message %d", i)
		}
	}
}

func TestBreadcrumbKeepsCountAndRecentWindow(t *testing.T) {
	p := NewBreadcrumbPruner() // RecentWindow 6, MinTrim 1600, Head 600
	msgs := buildHistory(10, 4000)
	out := p.Prune(msgs)

	// Message count must be identical so every tool_use keeps its tool_result.
	if len(out) != len(msgs) {
		t.Fatalf("pruning changed message count: %d -> %d", len(msgs), len(out))
	}
	// System and user turns untouched.
	if out[0].Role != llms.ChatMessageTypeSystem || out[1].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("prefix roles changed")
	}

	res := toolResults(out)
	if len(res) != 10 {
		t.Fatalf("expected 10 tool results, got %d", len(res))
	}
	// Oldest 4 trimmed, most recent 6 kept in full.
	for i, r := range res {
		trimmed := strings.Contains(r.Content, "elided to save context")
		if i < 4 && !trimmed {
			t.Errorf("old result %d should be trimmed", i)
		}
		if i >= 4 && trimmed {
			t.Errorf("recent result %d should be full", i)
		}
	}
}

func TestBreadcrumbNamesThePathAndKeepsHead(t *testing.T) {
	p := NewBreadcrumbPruner()
	msgs := buildHistory(10, 4000)
	out := p.Prune(msgs)
	first := toolResults(out)[0]
	if !strings.Contains(first.Content, `"filea.java"`) {
		t.Errorf("breadcrumb should name the file to re-read; got: %q", first.Content)
	}
	if len(first.Content) >= 4000 {
		t.Errorf("trimmed result should be much smaller than the 4000-byte original, got %d", len(first.Content))
	}
	// The pairing key must survive so the API sees a matched tool_result.
	if first.ToolCallID != "a" || first.Name != "read_file" {
		t.Errorf("trimming must preserve ToolCallID/Name, got %q/%q", first.ToolCallID, first.Name)
	}
}

func TestBreadcrumbLeavesSmallResultsAlone(t *testing.T) {
	p := NewBreadcrumbPruner()
	msgs := buildHistory(10, 200) // under MinTrimChars
	out := p.Prune(msgs)
	for i, r := range toolResults(out) {
		if strings.Contains(r.Content, "elided") {
			t.Errorf("small result %d should not be trimmed", i)
		}
	}
}

func TestBreadcrumbDeterministicForCacheStability(t *testing.T) {
	// A message that is already old must trim to the same bytes on a later,
	// longer history, so the sent prefix stays byte-stable and cacheable.
	p := NewBreadcrumbPruner()
	short := p.Prune(buildHistory(8, 4000))
	long := p.Prune(buildHistory(12, 4000))
	// In both, the first tool result is old; its trimmed content must match.
	if toolResults(short)[0].Content != toolResults(long)[0].Content {
		t.Errorf("trimming of an old result is not stable across turns")
	}
}

// capturingModel records the messages it is handed on each call, then replays a
// canned response, so a test can inspect exactly what the loop transmitted.
type capturingModel struct {
	responses []*llms.ContentResponse
	seen      [][]llms.MessageContent
	calls     int
}

func (m *capturingModel) GenerateContent(_ context.Context, msgs []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	m.seen = append(m.seen, msgs)
	r := m.responses[m.calls]
	m.calls++
	return r, nil
}

func (m *capturingModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", nil
}

// TestLoopSendsPrunedView guards the wiring: the loop must transmit the pruner's
// output, not the raw history. With a one-turn window, by the final call the
// first file read is old and must arrive trimmed.
func TestLoopSendsPrunedView(t *testing.T) {
	model := &capturingModel{responses: []*llms.ContentResponse{
		toolCallResponse("read_file", `{"path":"first.go"}`),
		toolCallResponse("read_file", `{"path":"second.go"}`),
		textResponse("done"),
	}}
	big := strings.Repeat("y", 5000)
	_, err := RunToolLoop(context.Background(), LoopOptions{
		Model: model, ModelName: "fake", System: "sys", User: "user",
		Exec: func(string, string) string { return big }, MaxIters: 8,
		Pruner: BreadcrumbPruner{RecentWindow: 1, MinTrimChars: 1600, HeadChars: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	last := model.seen[len(model.seen)-1]
	res := toolResults(last)
	if len(res) < 2 {
		t.Fatalf("expected at least 2 tool results in final call, got %d", len(res))
	}
	if !strings.Contains(res[0].Content, "elided to save context") {
		t.Errorf("loop did not send the pruned view; first result was full: %q", res[0].Content[:40])
	}
	if strings.Contains(res[len(res)-1].Content, "elided") {
		t.Errorf("most recent result should be full, was trimmed")
	}
}

func TestPrunerFromContextDefaultsToNoPrune(t *testing.T) {
	if _, ok := PrunerFrom(context.Background()).(NoPrune); !ok {
		t.Fatalf("unset context should yield NoPrune")
	}
	ctx := WithPruner(context.Background(), NewBreadcrumbPruner())
	if PrunerFrom(ctx).Name() != "breadcrumb" {
		t.Fatalf("WithPruner not honored")
	}
}
