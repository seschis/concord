package tui

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	prog "github.com/seschis/concord/internal/progress"
)

// key builds a tea.KeyMsg for a single rune, matching how Bubble Tea delivers
// ordinary keypresses (msg.String() == the rune).
func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// TestQuitKeysGracefulSave verifies that a double-press of q cancels the run and
// marks it as a save-and-quit, while a single q only arms (and is disarmed by
// any other key).
func TestQuitKeysGracefulSave(t *testing.T) {
	cancelled := false
	m := NewModel(Header{}, func() { cancelled = true }, time.Now())

	// First q arms but does not cancel.
	m.Update(key('q'))
	if !m.quitArmed || m.interrupting || cancelled {
		t.Fatalf("after 1st q: armed=%v interrupting=%v cancelled=%v, want armed only", m.quitArmed, m.interrupting, cancelled)
	}

	// An unrelated key disarms.
	m.Update(key('x'))
	if m.quitArmed {
		t.Fatalf("unrelated key did not disarm q")
	}

	// Re-arm, then confirm with a second q: cancels and requests a saved report.
	m.Update(key('q'))
	m.Update(key('q'))
	if !m.interrupting || !m.saveReports || !cancelled || m.quitArmed {
		t.Fatalf("after q q: interrupting=%v saveReports=%v cancelled=%v armed=%v, want graceful save", m.interrupting, m.saveReports, cancelled, m.quitArmed)
	}
}

// TestQuitCtrlCAborts verifies ctrl+c cancels without requesting a report and
// that a second ctrl+c force-quits.
func TestQuitCtrlCAborts(t *testing.T) {
	cancelled := false
	m := NewModel(Header{}, func() { cancelled = true }, time.Now())

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.interrupting || m.saveReports || !cancelled {
		t.Fatalf("after ctrl+c: interrupting=%v saveReports=%v cancelled=%v, want abort", m.interrupting, m.saveReports, cancelled)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.finished || !m.forced || cmd == nil {
		t.Fatalf("second ctrl+c did not force-quit: finished=%v forced=%v cmd=%v", m.finished, m.forced, cmd)
	}
}

// TestQuitDoneCompletesGracefully verifies that after a graceful q q the engine
// signalling completion (doneMsg) finishes the model and requests a quit.
func TestQuitDoneCompletesGracefully(t *testing.T) {
	m := NewModel(Header{}, func() {}, time.Now())
	m.Update(key('q'))
	m.Update(key('q'))
	_, cmd := m.Update(doneMsg{err: context.Canceled})
	if !m.finished || !m.saveReports || cmd == nil {
		t.Fatalf("doneMsg after q q: finished=%v saveReports=%v cmd=%v", m.finished, m.saveReports, cmd)
	}
}

// TestApplyRunReducer drives the pure reducer through a representative shared-
// strategy sequence and checks the derived state: rows in first-seen order,
// verdicts, live cost accrual, and progress counting.
func TestApplyRunReducer(t *testing.T) {
	m := NewModel(Header{Models: []string{"claude", "gemini"}}, nil, time.Now())

	feed := []prog.Event{
		{Kind: prog.RunStart, Total: 2},
		{Kind: prog.FindingStart, Total: 2, FindingIdx: 0, FindingID: "F1", VulnType: "SQLi", Severity: "HIGH"},
		{Kind: prog.Action, Provider: "claude", Role: prog.RoleExplorer, Action: "reading a.go", CostUSD: 0.001},
		{Kind: prog.Action, Provider: "claude", Role: prog.RoleExplorer, Action: "search \"x\"", CostUSD: 0.002},
		{Kind: prog.ModelDone, Provider: "claude", Role: prog.RoleExplorer, Verdict: "context ready"},
		{Kind: prog.Action, Provider: "claude", Role: prog.RoleVoter, Action: "analyzing"},
		{Kind: prog.ModelDone, Provider: "claude", Role: prog.RoleVoter, Verdict: "REAL", CostUSD: 0.004},
		{Kind: prog.Action, Provider: "gemini", Role: prog.RoleVoter, Action: "analyzing"},
		{Kind: prog.ModelDone, Provider: "gemini", Role: prog.RoleVoter, Verdict: "NOT_EXPLOITABLE", CostUSD: 0.003},
		{Kind: prog.FindingDone, FindingIdx: 0, FindingID: "F1", Verdict: "CONFIRMED_REAL", Class: "TRUE_POSITIVE", Agreement: "majority"},
	}
	for _, e := range feed {
		m.apply(e)
	}

	// The finished finding is recorded with its classification and counted.
	if len(m.completed) != 1 {
		t.Fatalf("completed = %d, want 1", len(m.completed))
	}
	if got := m.completed[0]; got.id != "F1" || got.class != "TRUE_POSITIVE" || got.typ != "SQLi" {
		t.Fatalf("completed row wrong: %+v", got)
	}
	if m.tally["TRUE_POSITIVE"] != 1 {
		t.Fatalf("tally TRUE_POSITIVE = %d, want 1", m.tally["TRUE_POSITIVE"])
	}
	if v := m.View(); !strings.Contains(v, "TRUE_POSITIVE") || !strings.Contains(v, "TP 1") {
		t.Fatalf("View missing completed verdict/tally:\n%s", v)
	}

	if m.total != 2 {
		t.Fatalf("total = %d, want 2", m.total)
	}
	if m.curID != "F1" || m.curType != "SQLi" || m.curSeverity != "HIGH" {
		t.Fatalf("current finding wrong: %+v", struct{ ID, T, S string }{m.curID, m.curType, m.curSeverity})
	}
	if m.doneCount != 1 {
		t.Fatalf("doneCount = %d, want 1", m.doneCount)
	}
	// Rows: explorer (claude), voter claude, voter gemini — first-seen order.
	if len(m.rows) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(m.rows), m.rows)
	}
	if m.rows[0].label != "explorer" || !m.rows[0].done || m.rows[0].verdict != "context ready" {
		t.Fatalf("explorer row wrong: %+v", *m.rows[0])
	}
	if m.rows[1].label != "claude" || m.rows[1].verdict != "REAL" {
		t.Fatalf("claude voter row wrong: %+v", *m.rows[1])
	}
	if m.rows[2].label != "gemini" || m.rows[2].verdict != "NOT_EXPLOITABLE" {
		t.Fatalf("gemini voter row wrong: %+v", *m.rows[2])
	}
	// Live total = 0.001+0.002 (explorer actions) + 0.004 (claude) + 0.003 (gemini).
	if want := 0.010; math.Abs(m.totalCost-want) > 1e-9 {
		t.Fatalf("totalCost = %.6f, want %.6f", m.totalCost, want)
	}
	// Per-row cost: explorer accrued 0.003 via its actions.
	if math.Abs(m.rows[0].cost-0.003) > 1e-9 {
		t.Fatalf("explorer cost = %.6f, want 0.003", m.rows[0].cost)
	}
}

// TestViewRenders exercises the paint path (no TTY): View must not panic and
// must surface the finding, an in-flight action, and the live cost.
func TestViewRenders(t *testing.T) {
	m := NewModel(Header{InputFile: "/tmp/findings.csv", Models: []string{"claude"}, Strategy: "shared", Effort: "low"}, nil, time.Now())
	m.apply(prog.Event{Kind: prog.RunStart, Total: 3})
	m.apply(prog.Event{Kind: prog.FindingStart, Total: 3, FindingIdx: 1, FindingID: "F7", VulnType: "Command injection", Severity: "HIGH"})
	m.apply(prog.Event{Kind: prog.Action, Provider: "claude", Role: prog.RoleVoter, Action: "reading Foo.java", CostUSD: 0.0021})

	out := m.View()
	// Header shows current finding [2/3]; the progress counter shows completed 0/3.
	for _, want := range []string{"F7", "Command injection", "reading Foo.java", "$0.0021", "[2/3]", "0/3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("View() missing %q\n---\n%s", want, out)
		}
	}
}

// TestApplyJudgePanelRows verifies each adjudicator-panel judge gets its own row
// labeled by its provider name, in first-seen order.
func TestApplyJudgePanelRows(t *testing.T) {
	m := NewModel(Header{}, nil, time.Now())
	m.apply(prog.Event{Kind: prog.FindingStart, FindingID: "F1"})
	m.apply(prog.Event{Kind: prog.ModelDone, Provider: "strict", Role: prog.RoleAdjudicator, Verdict: "UNLIKELY"})
	m.apply(prog.Event{Kind: prog.ModelDone, Provider: "business", Role: prog.RoleAdjudicator, Verdict: "LIKELY_REAL"})
	if len(m.rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(m.rows), m.rows)
	}
	if m.rows[0].label != "strict" || m.rows[0].verdict != "UNLIKELY" {
		t.Fatalf("strict row wrong: %+v", *m.rows[0])
	}
	if m.rows[1].label != "business" || m.rows[1].verdict != "LIKELY_REAL" {
		t.Fatalf("business row wrong: %+v", *m.rows[1])
	}

	m2 := NewModel(Header{}, nil, time.Now())
	m2.apply(prog.Event{Kind: prog.ModelDone, Provider: "adjudicator", Role: prog.RoleAdjudicator, Verdict: "UNLIKELY"})
	if len(m2.rows) != 1 {
		t.Fatalf("default rows = %d, want 1: %+v", len(m2.rows), m2.rows)
	}
	if m2.rows[0].label != "adjudicator" {
		t.Fatalf("default row label = %q, want adjudicator", m2.rows[0].label)
	}
}

// TestFindingStartResetsRows verifies a new finding clears the prior finding's
// per-model rows so stale state never bleeds across findings.
func TestFindingStartResetsRows(t *testing.T) {
	m := NewModel(Header{}, nil, time.Now())
	m.apply(prog.Event{Kind: prog.FindingStart, FindingID: "F1"})
	m.apply(prog.Event{Kind: prog.Action, Provider: "claude", Role: prog.RoleVoter, Action: "analyzing"})
	if len(m.rows) != 1 {
		t.Fatalf("rows after F1 = %d, want 1", len(m.rows))
	}
	m.apply(prog.Event{Kind: prog.FindingStart, FindingID: "F2"})
	if len(m.rows) != 0 || len(m.rowIdx) != 0 {
		t.Fatalf("rows not reset on new finding: rows=%d idx=%d", len(m.rows), len(m.rowIdx))
	}
}
