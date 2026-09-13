package provider

import (
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestThinkingOptsMapsEffort(t *testing.T) {
	cases := map[string]llms.ThinkingMode{
		"low":     llms.ThinkingModeLow,
		"medium":  llms.ThinkingModeMedium,
		"high":    llms.ThinkingModeHigh,
		"":        llms.ThinkingModeLow, // default
		"unknown": llms.ThinkingModeLow, // default
	}
	for effort, wantMode := range cases {
		var co llms.CallOptions
		for _, opt := range thinkingOpts(effort) {
			opt(&co)
		}
		cfg := llms.GetThinkingConfig(&co)
		if cfg == nil || cfg.Mode != wantMode {
			t.Fatalf("effort %q: got mode %v, want %v", effort, cfg, wantMode)
		}
		// Thinking requires temperature=1 (Anthropic constraint).
		if co.Temperature != 1 {
			t.Fatalf("effort %q: temperature = %v, want 1", effort, co.Temperature)
		}
	}
}
