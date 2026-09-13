// Package engine orchestrates a finding through a context strategy, the
// multi-model vote, and adjudication. It imports provider, report, triage, and
// finding; nothing imports it, so there is no cycle.
package engine

import "github.com/seschis/concord/internal/triage"

// conservativeOrder ranks verdicts from most to least "real". The most
// conservative agreed verdict is the lowest-ranked one.
var conservativeOrder = map[triage.Verdict]int{
	triage.ConfirmedReal:    0,
	triage.LikelyReal:       1,
	triage.Unlikely:         2,
	triage.NotExploitable:   3,
	triage.NeedsMoreContext: 4,
}

func category(v triage.Verdict) string {
	switch v {
	case triage.ConfirmedReal, triage.LikelyReal:
		return "real"
	case triage.Unlikely, triage.NotExploitable:
		return "not_real"
	default:
		return "unknown"
	}
}

// Vote returns the final verdict and the agreement level across model results.
// Agreement is "single" for one voter, "all" when every voter shares a category,
// "majority" when one category strictly leads with two or more votes, and "none"
// on a tie (which the caller resolves by adjudication).
func Vote(results []triage.Result) (triage.Verdict, string) {
	if len(results) == 0 {
		return triage.NeedsMoreContext, "none"
	}
	if len(results) == 1 {
		return results[0].FinalVerdict, "single"
	}

	byCat := map[string][]triage.Verdict{}
	for _, r := range results {
		c := category(r.FinalVerdict)
		byCat[c] = append(byCat[c], r.FinalVerdict)
	}

	maxN, ties, bestCat := 0, 0, ""
	for c, vs := range byCat {
		switch {
		case len(vs) > maxN:
			maxN, ties, bestCat = len(vs), 1, c
		case len(vs) == maxN:
			ties++
		}
	}

	if maxN >= 2 && ties == 1 {
		agreement := "majority"
		if len(byCat) == 1 {
			agreement = "all"
		}
		return mostConservative(byCat[bestCat]), agreement
	}
	return triage.NeedsMoreContext, "none"
}

func mostConservative(vs []triage.Verdict) triage.Verdict {
	best := vs[0]
	for _, v := range vs[1:] {
		if conservativeOrder[v] < conservativeOrder[best] {
			best = v
		}
	}
	return best
}
