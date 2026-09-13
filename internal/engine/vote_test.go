package engine

import (
	"testing"

	"github.com/seschis/concord/internal/triage"
)

func rs(verdicts ...triage.Verdict) []triage.Result {
	out := make([]triage.Result, len(verdicts))
	for i, v := range verdicts {
		out[i] = triage.Result{FinalVerdict: v}
	}
	return out
}

func TestVote(t *testing.T) {
	cases := []struct {
		name          string
		in            []triage.Result
		wantVerdict   triage.Verdict
		wantAgreement string
	}{
		{"single", rs(triage.LikelyReal), triage.LikelyReal, "single"},
		{"all same category, most conservative wins",
			rs(triage.LikelyReal, triage.ConfirmedReal, triage.LikelyReal, triage.ConfirmedReal),
			triage.ConfirmedReal, "all"},
		{"three-one majority",
			rs(triage.NotExploitable, triage.NotExploitable, triage.Unlikely, triage.ConfirmedReal),
			triage.Unlikely, "majority"}, // not_real leads 3-1; most conservative not_real is UNLIKELY
		{"two-one-one majority",
			rs(triage.ConfirmedReal, triage.LikelyReal, triage.Unlikely, triage.NeedsMoreContext),
			triage.ConfirmedReal, "majority"}, // real leads 2, most conservative CONFIRMED_REAL
		{"two-two tie needs adjudication",
			rs(triage.ConfirmedReal, triage.LikelyReal, triage.Unlikely, triage.NotExploitable),
			triage.NeedsMoreContext, "none"},
		{"empty", rs(), triage.NeedsMoreContext, "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, a := Vote(c.in)
			if v != c.wantVerdict || a != c.wantAgreement {
				t.Fatalf("Vote = (%s, %s), want (%s, %s)", v, a, c.wantVerdict, c.wantAgreement)
			}
		})
	}
}

func jr(verdicts ...triage.Verdict) []triage.AdjudicationResult {
	out := make([]triage.AdjudicationResult, len(verdicts))
	for i, v := range verdicts {
		out[i] = triage.AdjudicationResult{FinalVerdict: v}
	}
	return out
}

func TestVoteJudges(t *testing.T) {
	cases := []struct {
		name          string
		in            []triage.AdjudicationResult
		wantVerdict   triage.Verdict
		wantAgreement string
	}{
		{"single judge", jr(triage.Unlikely), triage.Unlikely, "single"},
		{"judge majority breaks to conservative",
			jr(triage.NotExploitable, triage.Unlikely, triage.ConfirmedReal),
			triage.Unlikely, "majority"}, // not_real 2-1; most conservative not_real is UNLIKELY
		{"judge two-two tie",
			jr(triage.ConfirmedReal, triage.LikelyReal, triage.Unlikely, triage.NotExploitable),
			triage.NeedsMoreContext, "none"},
		{"judge all real",
			jr(triage.ConfirmedReal, triage.LikelyReal),
			triage.ConfirmedReal, "all"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, a := VoteJudges(c.in)
			if v != c.wantVerdict || a != c.wantAgreement {
				t.Fatalf("VoteJudges = (%s, %s), want (%s, %s)", v, a, c.wantVerdict, c.wantAgreement)
			}
		})
	}
}
