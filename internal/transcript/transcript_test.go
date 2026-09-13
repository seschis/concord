package transcript

import (
	"context"
	"testing"
)

type recordingWriter struct{ steps []Step }

func (r *recordingWriter) Record(role, provider, findingID string, s Step) {
	r.steps = append(r.steps, s)
}

func TestFromReturnsNopWhenUnset(t *testing.T) {
	w := From(context.Background())
	if _, ok := w.(Nop); !ok {
		t.Fatalf("want Nop, got %T", w)
	}
	// Must be safe to call without panicking.
	w.Record("voter", "claude", "1", Step{Iter: 1})
}

func TestWithWriterRoundTrips(t *testing.T) {
	rec := &recordingWriter{}
	ctx := WithWriter(context.Background(), rec)

	w := From(ctx)
	w.Record("voter", "claude", "1", Step{Iter: 1, Text: "done", Final: true})

	if len(rec.steps) != 1 || rec.steps[0].Text != "done" {
		t.Fatalf("writer installed on ctx did not receive the record: %+v", rec.steps)
	}
}
