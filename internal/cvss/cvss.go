// Package cvss implements the CVSS 4.0 scoring algorithm.
//
// The scoring logic is a port of the FIRST reference JavaScript implementation
// (BSD-2-Clause). The metric guidance embedded in the triage prompt
// (internal/triage/prompts.go) condenses the scoring rubrics transcribed in
// docs/cvss-v4-scoring-rubrics.txt, so a model can score as it triages.
//
// This package has zero internal dependencies.
package cvss

import (
	"fmt"
	"math"
	"strings"
)

// Vector holds CVSS 4.0 metric values. Only the 11 base metrics are required.
// Optional metrics (E, CR/IR/AR) default to "X" (not defined).
type Vector struct {
	AV, AC, AT, PR, UI string
	VC, VI, VA         string
	SC, SI, SA         string
	E                  string
	CR, IR, AR         string
}

// validMetrics defines the allowed values for each metric.
var validMetrics = map[string][]string{
	"AV": {"N", "A", "L", "P"},
	"AC": {"L", "H"},
	"AT": {"N", "P"},
	"PR": {"N", "L", "H"},
	"UI": {"N", "P", "A"},
	"VC": {"H", "L", "N"},
	"VI": {"H", "L", "N"},
	"VA": {"H", "L", "N"},
	"SC": {"H", "L", "N"},
	"SI": {"S", "H", "L", "N"},
	"SA": {"S", "H", "L", "N"},
	"E":  {"X", "A", "P", "U"},
	"CR": {"X", "H", "M", "L"},
	"IR": {"X", "H", "M", "L"},
	"AR": {"X", "H", "M", "L"},
}

// baseMetrics lists the 11 metrics that must always be present.
var baseMetrics = []string{"AV", "AC", "AT", "PR", "UI", "VC", "VI", "VA", "SC", "SI", "SA"}

// ParseVector parses a CVSS 4.0 vector string into a Vector.
// The string must start with "CVSS:4.0/" followed by metric key:value pairs
// separated by "/". All 11 base metrics are required. Optional metrics
// default to "X".
func ParseVector(s string) (Vector, error) {
	if !strings.HasPrefix(s, "CVSS:4.0/") {
		return Vector{}, fmt.Errorf("cvss: vector must start with CVSS:4.0/, got %q", s)
	}

	body := s[len("CVSS:4.0/"):]
	if body == "" {
		return Vector{}, fmt.Errorf("cvss: empty vector body")
	}

	v := Vector{E: "X", CR: "X", IR: "X", AR: "X"}
	seen := make(map[string]bool)

	parts := strings.Split(body, "/")
	for _, part := range parts {
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			return Vector{}, fmt.Errorf("cvss: invalid metric %q", part)
		}
		key, val := kv[0], kv[1]

		allowed, ok := validMetrics[key]
		if !ok {
			return Vector{}, fmt.Errorf("cvss: unknown metric %q", key)
		}
		if !contains(allowed, val) {
			return Vector{}, fmt.Errorf("cvss: invalid value %q for metric %s", val, key)
		}
		if seen[key] {
			return Vector{}, fmt.Errorf("cvss: duplicate metric %s", key)
		}
		seen[key] = true

		switch key {
		case "AV":
			v.AV = val
		case "AC":
			v.AC = val
		case "AT":
			v.AT = val
		case "PR":
			v.PR = val
		case "UI":
			v.UI = val
		case "VC":
			v.VC = val
		case "VI":
			v.VI = val
		case "VA":
			v.VA = val
		case "SC":
			v.SC = val
		case "SI":
			v.SI = val
		case "SA":
			v.SA = val
		case "E":
			v.E = val
		case "CR":
			v.CR = val
		case "IR":
			v.IR = val
		case "AR":
			v.AR = val
		}
	}

	for _, m := range baseMetrics {
		if !seen[m] {
			return Vector{}, fmt.Errorf("cvss: missing required metric %s", m)
		}
	}

	return v, nil
}

// String formats the Vector as a CVSS 4.0 vector string.
// Base metrics are always included. Optional metrics with value "X" are omitted.
func (v Vector) String() string {
	var b strings.Builder
	b.WriteString("CVSS:4.0")

	// Base metrics (always included).
	b.WriteString("/AV:" + v.AV)
	b.WriteString("/AC:" + v.AC)
	b.WriteString("/AT:" + v.AT)
	b.WriteString("/PR:" + v.PR)
	b.WriteString("/UI:" + v.UI)
	b.WriteString("/VC:" + v.VC)
	b.WriteString("/VI:" + v.VI)
	b.WriteString("/VA:" + v.VA)
	b.WriteString("/SC:" + v.SC)
	b.WriteString("/SI:" + v.SI)
	b.WriteString("/SA:" + v.SA)

	// Optional metrics, only if set.
	if v.E != "" && v.E != "X" {
		b.WriteString("/E:" + v.E)
	}
	if v.CR != "" && v.CR != "X" {
		b.WriteString("/CR:" + v.CR)
	}
	if v.IR != "" && v.IR != "X" {
		b.WriteString("/IR:" + v.IR)
	}
	if v.AR != "" && v.AR != "X" {
		b.WriteString("/AR:" + v.AR)
	}

	return b.String()
}

// Score computes the CVSS 4.0 numeric score for this vector.
func (v Vector) Score() float64 {
	// Short-circuit: if all impact metrics are "N", score is 0.
	if v.VC == "N" && v.VI == "N" && v.VA == "N" &&
		v.SC == "N" && v.SI == "N" && v.SA == "N" {
		return 0.0
	}

	mv := MacroVector(v)
	baseScore, ok := cvssLookup[mv]
	if !ok {
		return 0.0
	}

	// Apply defaults for "X" values (the m() function from the JS).
	e := defaultMetric(v.E, "A")
	cr := defaultMetric(v.CR, "H")
	ir := defaultMetric(v.IR, "H")
	ar := defaultMetric(v.AR, "H")

	// Parse EQ levels from the MacroVector.
	eq1 := int(mv[0] - '0')
	eq2 := int(mv[1] - '0')
	eq3 := int(mv[2] - '0')
	eq4 := int(mv[3] - '0')
	eq5 := int(mv[4] - '0')
	eq6 := int(mv[5] - '0')

	// For each EQ, find the score of the next-lower MacroVector.
	// EQ3 and EQ6 are linked with special transitions.
	eqNextScores := [5]float64{}

	// EQ1 next
	eqNextScores[0] = lookupNext(mv, 0, eq1+1)
	// EQ2 next
	eqNextScores[1] = lookupNext(mv, 1, eq2+1)
	// EQ3+EQ6 next: try both transitions, take the higher score.
	eqNextScores[2] = nextEQ3EQ6(mv, eq3, eq6)
	// EQ4 next
	eqNextScores[3] = lookupNext(mv, 3, eq4+1)
	// EQ5 next
	eqNextScores[4] = lookupNext(mv, 4, eq5+1)

	// Build the effective metric values for severity distance computation.
	mvals := map[string]string{
		"AV": v.AV, "PR": v.PR, "UI": v.UI,
		"AC": v.AC, "AT": v.AT,
		"VC": v.VC, "VI": v.VI, "VA": v.VA,
		"CR": cr, "IR": ir, "AR": ar,
		"SC": v.SC, "SI": v.SI, "SA": v.SA,
		"E": e,
	}

	// For each EQ, compute severity distances from the max vector.
	// We need to find a combination of maxComposed entries (one per EQ group)
	// where all severity distances >= 0.

	// Generate candidate max vectors by composing across EQ groups.
	eq1Maxes := maxComposed.EQ1[eq1]
	eq2Maxes := maxComposed.EQ2[eq2]
	eq3eq6Maxes := maxComposed.EQ3[eq3][eq6]
	eq4Maxes := maxComposed.EQ4[eq4]
	eq5Maxes := maxComposed.EQ5[eq5]

	// The severity distance per EQ group, computed from the max vector that
	// has all non-negative individual distances.
	hamming := [5]float64{math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN()}

	// Try all combinations of max vectors.
	for _, m1 := range eq1Maxes {
		for _, m2 := range eq2Maxes {
			for _, m36 := range eq3eq6Maxes {
				for _, m4 := range eq4Maxes {
					for _, m5 := range eq5Maxes {
						candidate := m1 + m2 + m36 + m4 + m5
						maxMetrics := extractMetrics(candidate)

						// Check all individual distances are >= 0.
						allNonNeg := true
						for metric, val := range mvals {
							levels, ok := metricLevels[metric]
							if !ok {
								continue
							}
							maxLevel, ok1 := levels[maxMetrics[metric]]
							curLevel, ok2 := levels[val]
							if !ok1 || !ok2 {
								continue
							}
							if curLevel-maxLevel < 0 {
								allNonNeg = false
								break
							}
						}

						if !allNonNeg {
							continue
						}

						// Compute hamming distances per EQ group.
						h1 := dist(maxMetrics, mvals, "AV") + dist(maxMetrics, mvals, "PR") + dist(maxMetrics, mvals, "UI")
						h2 := dist(maxMetrics, mvals, "AC") + dist(maxMetrics, mvals, "AT")
						h3eq6 := dist(maxMetrics, mvals, "VC") + dist(maxMetrics, mvals, "VI") + dist(maxMetrics, mvals, "VA") +
							dist(maxMetrics, mvals, "CR") + dist(maxMetrics, mvals, "IR") + dist(maxMetrics, mvals, "AR")
						h4 := dist(maxMetrics, mvals, "SC") + dist(maxMetrics, mvals, "SI") + dist(maxMetrics, mvals, "SA")
						h5 := float64(0)

						hamming = [5]float64{h1, h2, h3eq6, h4, h5}
						goto found
					}
				}
			}
		}
	}
found:

	// Normalize and interpolate.
	maxSev := [5]float64{
		float64(maxSeverity.EQ1[eq1]),
		float64(maxSeverity.EQ2[eq2]),
		float64(maxSeverity.EQ3EQ6[eq3][eq6]),
		float64(maxSeverity.EQ4[eq4]),
		float64(maxSeverity.EQ5[eq5]),
	}

	var totalAdjust float64
	var count float64

	for i := 0; i < 5; i++ {
		nextScore := eqNextScores[i]
		if math.IsNaN(nextScore) {
			continue
		}
		availDist := baseScore - nextScore

		var normalized float64
		if maxSev[i] > 0 {
			pct := hamming[i] / (maxSev[i] * 0.1)
			if pct > 1 {
				pct = 1
			}
			normalized = pct * availDist
		}

		totalAdjust += normalized
		count++
	}

	var meanAdjust float64
	if count > 0 {
		meanAdjust = totalAdjust / count
	}

	result := baseScore - meanAdjust
	if result < 0 {
		result = 0
	}
	if result > 10 {
		result = 10
	}

	return math.Round(result*10) / 10
}

// Severity returns the qualitative severity rating for this vector.
func (v Vector) Severity() string {
	s := v.Score()
	switch {
	case s == 0.0:
		return "None"
	case s <= 3.9:
		return "Low"
	case s <= 6.9:
		return "Medium"
	case s <= 8.9:
		return "High"
	default:
		return "Critical"
	}
}

// MacroVector computes the 6-digit MacroVector string for the given Vector.
// Each digit represents an equivalence class level.
func MacroVector(v Vector) string {
	// Apply defaults for "X" values.
	e := defaultMetric(v.E, "A")
	cr := defaultMetric(v.CR, "H")
	ir := defaultMetric(v.IR, "H")
	ar := defaultMetric(v.AR, "H")

	var mv [6]byte

	// EQ1: AV/PR/UI combinations.
	switch {
	case v.AV == "N" && v.PR == "N" && v.UI == "N":
		mv[0] = '0'
	case (v.AV == "N" || v.PR == "N" || v.UI == "N") &&
		!(v.AV == "N" && v.PR == "N" && v.UI == "N") &&
		v.AV != "P":
		mv[0] = '1'
	default:
		mv[0] = '2'
	}

	// EQ2: AC/AT.
	if v.AC == "L" && v.AT == "N" {
		mv[1] = '0'
	} else {
		mv[1] = '1'
	}

	// EQ3: VC/VI/VA.
	switch {
	case v.VC == "H" && v.VI == "H":
		mv[2] = '0'
	case v.VC == "H" || v.VI == "H" || v.VA == "H":
		mv[2] = '1'
	default:
		mv[2] = '2'
	}

	// EQ4: SC/SI/SA (MSI/MSA "S" maps to Safety).
	switch {
	case v.SI == "S" || v.SA == "S":
		mv[3] = '0'
	case v.SC == "H" || v.SI == "H" || v.SA == "H":
		mv[3] = '1'
	default:
		mv[3] = '2'
	}

	// EQ5: E (exploit maturity).
	switch e {
	case "A":
		mv[4] = '0'
	case "P":
		mv[4] = '1'
	case "U":
		mv[4] = '2'
	}

	// EQ6: CR*VC, IR*VI, AR*VA combinations.
	crVC := cr == "H" && v.VC == "H"
	irVI := ir == "H" && v.VI == "H"
	arVA := ar == "H" && v.VA == "H"
	if crVC || irVI || arVA {
		mv[5] = '0'
	} else {
		mv[5] = '1'
	}

	return string(mv[:])
}

// defaultMetric returns val unless it is "X" or empty, in which case it
// returns the default. This mirrors the m() helper in the JS reference.
func defaultMetric(val, def string) string {
	if val == "X" || val == "" {
		return def
	}
	return val
}

// lookupNext returns the score for the MacroVector with position pos replaced
// by newDigit. Returns NaN if the resulting key is not in the lookup table.
func lookupNext(mv string, pos int, newDigit int) float64 {
	if newDigit > 9 {
		return math.NaN()
	}
	next := []byte(mv)
	next[pos] = byte('0' + newDigit)
	score, ok := cvssLookup[string(next)]
	if !ok {
		return math.NaN()
	}
	return score
}

// nextEQ3EQ6 computes the next-lower MacroVector score for the linked EQ3/EQ6
// pair. Transitions: 00->01,00->10(higher), 01->11, 10->11, 11->21, 21->32(DNE).
func nextEQ3EQ6(mv string, eq3, eq6 int) float64 {
	switch {
	case eq3 == 0 && eq6 == 0:
		// Try both 01 and 10, pick the higher score.
		s01 := lookupEQ3EQ6(mv, 0, 1)
		s10 := lookupEQ3EQ6(mv, 1, 0)
		switch {
		case math.IsNaN(s01) && math.IsNaN(s10):
			return math.NaN()
		case math.IsNaN(s01):
			return s10
		case math.IsNaN(s10):
			return s01
		default:
			return math.Max(s01, s10)
		}
	case eq3 == 0 && eq6 == 1:
		return lookupEQ3EQ6(mv, 1, 1)
	case eq3 == 1 && eq6 == 0:
		return lookupEQ3EQ6(mv, 1, 1)
	case eq3 == 1 && eq6 == 1:
		return lookupEQ3EQ6(mv, 2, 1)
	case eq3 == 2 && eq6 == 1:
		// 32 does not exist in the lookup table.
		return math.NaN()
	default:
		return math.NaN()
	}
}

// lookupEQ3EQ6 replaces positions 2 and 5 in the MacroVector and looks up
// the score.
func lookupEQ3EQ6(mv string, eq3, eq6 int) float64 {
	next := []byte(mv)
	next[2] = byte('0' + eq3)
	next[5] = byte('0' + eq6)
	score, ok := cvssLookup[string(next)]
	if !ok {
		return math.NaN()
	}
	return score
}

// extractMetrics parses a composed max-vector fragment string like
// "AV:N/PR:N/UI:N/AC:L/AT:N/..." into a metric->value map.
func extractMetrics(s string) map[string]string {
	m := make(map[string]string)
	parts := strings.Split(s, "/")
	for _, p := range parts {
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, ":", 2)
		if len(kv) == 2 {
			m[kv[0]] = kv[1]
		}
	}
	return m
}

// dist returns the severity distance for a single metric between the max
// vector and the current vector.
func dist(maxMetrics, curMetrics map[string]string, metric string) float64 {
	levels, ok := metricLevels[metric]
	if !ok {
		return 0
	}
	maxVal, ok1 := maxMetrics[metric]
	curVal, ok2 := curMetrics[metric]
	if !ok1 || !ok2 {
		return 0
	}
	maxLev, ok1 := levels[maxVal]
	curLev, ok2 := levels[curVal]
	if !ok1 || !ok2 {
		return 0
	}
	return curLev - maxLev
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
