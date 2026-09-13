package triage

import (
	"context"
	"testing"
)

func TestGuideContextRoundTrip(t *testing.T) {
	if got := GuideFrom(context.Background()); got != "" {
		t.Fatalf("expected empty guide on bare context, got %q", got)
	}
	ctx := WithGuide(context.Background(), "gateway at cag/")
	if got := GuideFrom(ctx); got != "gateway at cag/" {
		t.Fatalf("guide round-trip failed, got %q", got)
	}
}
