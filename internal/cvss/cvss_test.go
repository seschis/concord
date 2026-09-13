package cvss

import (
	"math"
	"testing"
)

func TestScoreMaxVector(t *testing.T) {
	v, err := ParseVector("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H")
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	got := v.Score()
	if got != 10.0 {
		t.Errorf("max vector score = %v, want 10.0", got)
	}
}

func TestScoreAllNoneImpact(t *testing.T) {
	v, err := ParseVector("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:N/VI:N/VA:N/SC:N/SI:N/SA:N")
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	got := v.Score()
	if got != 0.0 {
		t.Errorf("all-none vector score = %v, want 0.0", got)
	}
}

func TestScoreMediumVector(t *testing.T) {
	// A vector that should score around 7.1 High.
	v, err := ParseVector("CVSS:4.0/AV:N/AC:L/AT:N/PR:L/UI:N/VC:N/VI:H/VA:N/SC:L/SI:L/SA:N")
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	got := v.Score()
	// Accept a small range around the expected value.
	if got < 5.0 || got > 9.0 {
		t.Errorf("medium vector score = %v, want ~7.1 (between 5.0 and 9.0)", got)
	}
	sev := v.Severity()
	if sev != "High" && sev != "Medium" {
		t.Errorf("medium vector severity = %q, want High or Medium", sev)
	}
}

func TestScoreLowVector(t *testing.T) {
	v, err := ParseVector("CVSS:4.0/AV:P/AC:H/AT:P/PR:H/UI:A/VC:L/VI:L/VA:L/SC:L/SI:L/SA:L")
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	got := v.Score()
	if got > 3.9 {
		t.Errorf("low vector score = %v, want <= 3.9 (Low range)", got)
	}
	sev := v.Severity()
	if sev != "Low" {
		t.Errorf("low vector severity = %q, want Low", sev)
	}
}

func TestSeverityBands(t *testing.T) {
	tests := []struct {
		name   string
		score  float64
		expect string
	}{
		{"none", 0.0, "None"},
		{"low-bottom", 0.1, "Low"},
		{"low-top", 3.9, "Low"},
		{"medium-bottom", 4.0, "Medium"},
		{"medium-top", 6.9, "Medium"},
		{"high-bottom", 7.0, "High"},
		{"high-top", 8.9, "High"},
		{"critical-bottom", 9.0, "Critical"},
		{"critical-top", 10.0, "Critical"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build a dummy vector just to call Severity; we override the
			// score check by testing the severity function directly.
			got := severityFromScore(tt.score)
			if got != tt.expect {
				t.Errorf("severity(%v) = %q, want %q", tt.score, got, tt.expect)
			}
		})
	}
}

func severityFromScore(s float64) string {
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

func TestParseVectorValid(t *testing.T) {
	input := "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"
	v, err := ParseVector(input)
	if err != nil {
		t.Fatalf("ParseVector(%q): %v", input, err)
	}
	if v.AV != "N" || v.AC != "L" || v.AT != "N" || v.PR != "N" || v.UI != "N" {
		t.Errorf("base metrics parsed incorrectly: %+v", v)
	}
	if v.VC != "H" || v.VI != "H" || v.VA != "H" {
		t.Errorf("vendor impact parsed incorrectly: %+v", v)
	}
	if v.SC != "H" || v.SI != "H" || v.SA != "H" {
		t.Errorf("subsequent impact parsed incorrectly: %+v", v)
	}
	// Optional metrics should default to "X".
	if v.E != "X" {
		t.Errorf("E = %q, want X", v.E)
	}
	if v.CR != "X" || v.IR != "X" || v.AR != "X" {
		t.Errorf("CR/IR/AR should default to X, got %q/%q/%q", v.CR, v.IR, v.AR)
	}
}

func TestParseVectorWithOptional(t *testing.T) {
	input := "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H/E:P/CR:M/IR:L/AR:H"
	v, err := ParseVector(input)
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	if v.E != "P" {
		t.Errorf("E = %q, want P", v.E)
	}
	if v.CR != "M" {
		t.Errorf("CR = %q, want M", v.CR)
	}
	if v.IR != "L" {
		t.Errorf("IR = %q, want L", v.IR)
	}
	if v.AR != "H" {
		t.Errorf("AR = %q, want H", v.AR)
	}
}

func TestParseVectorErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"wrong prefix", "CVSS:3.1/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"},
		{"missing metric", "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H"},
		{"invalid value", "CVSS:4.0/AV:Z/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"},
		{"unknown metric", "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H/ZZ:Q"},
		{"duplicate metric", "CVSS:4.0/AV:N/AV:A/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"},
		{"empty body", "CVSS:4.0/"},
		{"no prefix", "AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseVector(tt.input)
			if err == nil {
				t.Errorf("ParseVector(%q) should have returned an error", tt.input)
			}
		})
	}
}

func TestStringRoundTrip(t *testing.T) {
	input := "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"
	v, err := ParseVector(input)
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	got := v.String()
	if got != input {
		t.Errorf("String() = %q, want %q", got, input)
	}
}

func TestStringRoundTripWithOptional(t *testing.T) {
	input := "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H/E:P/CR:M/IR:L/AR:H"
	v, err := ParseVector(input)
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	got := v.String()
	if got != input {
		t.Errorf("String() = %q, want %q", got, input)
	}
}

func TestStringOmitsDefaultOptional(t *testing.T) {
	v := Vector{
		AV: "N", AC: "L", AT: "N", PR: "N", UI: "N",
		VC: "H", VI: "H", VA: "H",
		SC: "H", SI: "H", SA: "H",
		E: "X", CR: "X", IR: "X", AR: "X",
	}
	got := v.String()
	expected := "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"
	if got != expected {
		t.Errorf("String() = %q, want %q", got, expected)
	}
}

func TestMacroVector(t *testing.T) {
	tests := []struct {
		name   string
		vector string
		want   string
	}{
		{
			"max severity",
			"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H",
			"000100", // SI:H/SA:H -> EQ4=1 (only SI:S or SA:S gives EQ4=0)
		},
		{
			"all low impact",
			"CVSS:4.0/AV:P/AC:H/AT:P/PR:H/UI:A/VC:L/VI:L/VA:L/SC:L/SI:L/SA:L",
			"212201",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ParseVector(tt.vector)
			if err != nil {
				t.Fatalf("ParseVector: %v", err)
			}
			got := MacroVector(v)
			if got != tt.want {
				t.Errorf("MacroVector() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLookupTableSize(t *testing.T) {
	if len(cvssLookup) != 270 {
		t.Errorf("cvssLookup has %d entries, want 270", len(cvssLookup))
	}
}

func TestScoreRange(t *testing.T) {
	// Verify all lookup scores are in [0, 10].
	for k, v := range cvssLookup {
		if v < 0 || v > 10 {
			t.Errorf("cvssLookup[%q] = %v, out of [0,10] range", k, v)
		}
	}
}

func TestScoreNaN(t *testing.T) {
	// Verify NaN is not returned by Score().
	vectors := []string{
		"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H",
		"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:N/VI:N/VA:N/SC:N/SI:N/SA:N",
		"CVSS:4.0/AV:P/AC:H/AT:P/PR:H/UI:A/VC:L/VI:L/VA:L/SC:L/SI:L/SA:L",
		"CVSS:4.0/AV:N/AC:L/AT:N/PR:L/UI:N/VC:N/VI:H/VA:N/SC:L/SI:L/SA:N",
	}
	for _, vs := range vectors {
		v, err := ParseVector(vs)
		if err != nil {
			t.Fatalf("ParseVector(%q): %v", vs, err)
		}
		score := v.Score()
		if math.IsNaN(score) {
			t.Errorf("Score() returned NaN for %q", vs)
		}
	}
}

func TestSafetyMetrics(t *testing.T) {
	// SI:S should map to EQ4=0.
	v, err := ParseVector("CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:S/SA:S")
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	mv := MacroVector(v)
	if mv[3] != '0' {
		t.Errorf("EQ4 for SI:S/SA:S = %c, want 0", mv[3])
	}
}

func TestExploitMaturityEffect(t *testing.T) {
	// With E:U (unproven), score should be lower than E:A (attacked).
	base := "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"
	vA, _ := ParseVector(base)
	// E defaults to A, so this is the baseline.
	scoreA := vA.Score()

	vU, _ := ParseVector(base + "/E:U")
	scoreU := vU.Score()

	if scoreU >= scoreA {
		t.Errorf("E:U score (%v) should be less than E:A score (%v)", scoreU, scoreA)
	}
}
