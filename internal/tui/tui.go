// Package tui renders a realtime Bubble Tea dashboard for a triage run: overall
// progress, the finding in flight, what each model is doing right now, and the
// live accrued LLM cost. It consumes progress.Event values produced by the
// engine/provider layers.
//
// It is used only for interactive (TTY) runs; cmd falls back to a plain line
// printer otherwise. The Model's Update is a pure reducer over events, so the
// state transitions are unit-testable without a terminal.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	prog "github.com/seschis/harmonia/internal/progress"
)

// Header is the static run description shown at the top.
type Header struct {
	InputFile string
	Models    []string
	Unpriced  []string // names of voters without a price (marked, costed at $0)
	Analysts  []string // analyst-panel persona names, if any
	Strategy  string
	SrcRoot   string
	Effort    string
	Context   []string // labels of extra architecture-context roots, if any
}

// eventMsg wraps a progress.Event as a tea.Msg. doneMsg signals the engine
// finished; the run error (if any) rides along.
type (
	eventMsg prog.Event
	doneMsg  struct{ err error }
)

// completedRow is a finished finding's outcome, shown in the accruing results
// list.
type completedRow struct {
	idx       int
	id        string
	typ       string
	class     string // TRUE_POSITIVE | FALSE_POSITIVE | UNKNOWN
	verdict   string // raw verdict, e.g. CONFIRMED_REAL
	agree     string
	cvssScore float64 // CVSS 4.0 score, 0 if not computed
}

// modelRow is one model's live state for the current finding.
type modelRow struct {
	label   string // display label, e.g. "claude" or "explorer"
	role    string
	action  string // current action while running
	verdict string // set once done
	cost    float64
	done    bool
}

// Model is the Bubble Tea model. Exported so its reducer can be tested.
type Model struct {
	header Header
	cancel context.CancelFunc

	total     int
	doneCount int

	curIdx      int
	curID       string
	curType     string
	curSeverity string

	rows   []*modelRow
	rowIdx map[string]int // provider|role -> index into rows

	completed []completedRow // finished findings, in order, with their verdict
	tally     map[string]int // classification -> count (TRUE_POSITIVE/FALSE_POSITIVE/UNKNOWN)

	totalCost float64
	start     time.Time

	spinner      spinner.Model
	bar          progress.Model
	width        int
	height       int
	quitArmed    bool // q pressed once; a second q confirms a graceful quit
	interrupting bool // a quit is unwinding: waiting for the engine to stop
	saveReports  bool // the pending quit should write a (partial) report
	forced       bool // ctrl+c pressed during an unwind: quit now, discard results
	finished     bool
	err          error
}

// NewModel builds the initial model. start is passed in (not read from the
// clock here) so callers control timing.
func NewModel(h Header, cancel context.CancelFunc, start time.Time) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	bar := progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage())
	return &Model{
		header:  h,
		cancel:  cancel,
		start:   start,
		spinner: sp,
		bar:     bar,
		width:   80,
		rowIdx:  map[string]int{},
		tally:   map[string]int{},
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return m.spinner.Tick }

// Update implements tea.Model. It is a pure reducer over messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.bar.Width = clamp(msg.Width-30, 10, 60)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			// Hard abort: cancel the run and wait for the engine to unwind
			// (doneMsg) so results are never read while still being written; a
			// second press force-quits even if the unwind hangs. No report is
			// written on this path.
			if m.cancel != nil {
				m.cancel()
			}
			if m.interrupting {
				m.forced = true
				m.finished = true
				return m, tea.Quit
			}
			m.quitArmed = false
			m.saveReports = false
			m.interrupting = true
			return m, nil
		case "q":
			// Graceful quit: the first q arms, a second q confirms. On confirm
			// we cancel the run and wait for the engine to unwind, then the
			// caller writes a report of whatever findings completed. ctrl+c can
			// still force-quit if the unwind hangs.
			if m.interrupting {
				return m, nil
			}
			if !m.quitArmed {
				m.quitArmed = true
				return m, nil
			}
			m.quitArmed = false
			m.saveReports = true
			m.interrupting = true
			if m.cancel != nil {
				m.cancel()
			}
			return m, nil
		default:
			// Any other key disarms a pending q confirmation.
			m.quitArmed = false
			return m, nil
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case eventMsg:
		m.apply(prog.Event(msg))
		return m, nil

	case doneMsg:
		m.finished = true
		m.err = msg.err
		return m, tea.Quit
	}
	return m, nil
}

// apply folds one event into the model state. Kept separate from Update so tests
// can drive it directly.
func (m *Model) apply(e prog.Event) {
	switch e.Kind {
	case prog.RunStart:
		m.total = e.Total
	case prog.FindingStart:
		m.curIdx = e.FindingIdx
		m.curID = e.FindingID
		m.curType = e.VulnType
		m.curSeverity = e.Severity
		m.rows = m.rows[:0]
		m.rowIdx = map[string]int{}
	case prog.Action:
		r := m.row(e.Provider, e.Role)
		r.action = e.Action
		r.cost += e.CostUSD
		m.totalCost += e.CostUSD
	case prog.ModelDone:
		r := m.row(e.Provider, e.Role)
		r.done = true
		r.verdict = e.Verdict
		r.cost += e.CostUSD
		m.totalCost += e.CostUSD
	case prog.FindingDone:
		m.doneCount = e.FindingIdx + 1
		m.completed = append(m.completed, completedRow{
			idx: e.FindingIdx, id: e.FindingID, typ: m.curType,
			class: e.Class, verdict: e.Verdict, agree: e.Agreement,
			cvssScore: e.CVSSScore,
		})
		if e.Class != "" {
			m.tally[e.Class]++
		}
	case prog.RunDone:
		m.doneCount = m.total
	}
}

// row returns the row for a provider+role, creating it on first sighting so rows
// keep a stable order (explorer, then voters, then adjudicator).
func (m *Model) row(provider, role string) *modelRow {
	key := provider + "|" + role
	if i, ok := m.rowIdx[key]; ok {
		return m.rows[i]
	}
	label := provider
	switch role {
	case prog.RoleExplorer:
		label = "explorer"
	}
	r := &modelRow{label: label, role: role}
	m.rowIdx[key] = len(m.rows)
	m.rows = append(m.rows, r)
	return r
}

var (
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	headerStyle = lipgloss.NewStyle().Bold(true)
	costStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	doneStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	tpStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203")) // true positive, red
	fpStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // false positive, green
	unknownStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // unknown, amber
)

// classStyle colors a finding classification.
func classStyle(class string) lipgloss.Style {
	switch class {
	case "TRUE_POSITIVE":
		return tpStyle
	case "FALSE_POSITIVE":
		return fpStyle
	default:
		return unknownStyle
	}
}

// tallyLine renders the running counts of classifications, e.g. "TP 3  FP 8  ? 1".
// Returns "" until at least one finding has completed.
func (m *Model) tallyLine() string {
	if len(m.completed) == 0 {
		return ""
	}
	return fmt.Sprintf("%s  %s  %s",
		tpStyle.Render(fmt.Sprintf("TP %d", m.tally["TRUE_POSITIVE"])),
		fpStyle.Render(fmt.Sprintf("FP %d", m.tally["FALSE_POSITIVE"])),
		unknownStyle.Render(fmt.Sprintf("? %d", m.tally["UNKNOWN"])))
}

// visibleCompleted is how many finished findings fit above the in-flight block,
// derived from terminal height with a floor so the list is always useful.
func (m *Model) visibleCompleted() int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	const reserved = 16 // header, progress, in-flight block, summary
	return clamp(h-reserved, 4, len(m.completed))
}

// View implements tea.Model.
func (m *Model) View() string {
	var b strings.Builder

	models := strings.Join(m.header.Models, ", ")
	b.WriteString(headerStyle.Render("harmonia") + dimStyle.Render(
		fmt.Sprintf("  %s · %s · effort=%s", short(m.header.InputFile), m.header.Strategy, m.header.Effort)) + "\n")
	b.WriteString(dimStyle.Render("models: "+models) + "\n")
	if len(m.header.Unpriced) > 0 {
		b.WriteString(dimStyle.Render("unpriced: "+strings.Join(m.header.Unpriced, ", ")+" (no price set; costed at $0)") + "\n")
	}
	if len(m.header.Analysts) > 0 {
		b.WriteString(dimStyle.Render("analysts: "+strings.Join(m.header.Analysts, ", ")) + "\n")
	}
	if len(m.header.Context) > 0 {
		b.WriteString(dimStyle.Render("context: "+strings.Join(m.header.Context, ", ")) + "\n")
	}
	b.WriteString("\n")

	pct := 0.0
	if m.total > 0 {
		pct = float64(m.doneCount) / float64(m.total)
	}
	elapsed := time.Since(m.start).Round(time.Second)
	fmt.Fprintf(&b, "%s  %d/%d  %s  %s   %s\n",
		m.bar.ViewAs(pct), m.doneCount, m.total,
		m.costLabel(),
		dimStyle.Render(elapsed.String()),
		m.tallyLine())

	// Accruing results: the outcome of each finished finding, most recent last,
	// tailed to fit the terminal height.
	if len(m.completed) > 0 {
		b.WriteString("\n")
		start := 0
		if vis := m.visibleCompleted(); len(m.completed) > vis {
			start = len(m.completed) - vis
			b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d earlier\n", start)))
		}
		for _, c := range m.completed[start:] {
			agree := ""
			if c.agree != "" {
				agree = dimStyle.Render(" " + c.agree)
			}
			cvss := ""
			if c.cvssScore > 0 {
				cvss = "  " + cvssStyle(c.cvssScore).Render(fmt.Sprintf("CVSS %.1f", c.cvssScore))
			}
			fmt.Fprintf(&b, "  %s  %s  %s  %s%s%s\n",
				dimStyle.Render(fmt.Sprintf("[%2d]", c.idx+1)),
				labelStyle.Render(pad(c.id, 9)),
				pad(c.typ, 32),
				classStyle(c.class).Render(pad(c.class, 14)),
				cvss,
				agree)
		}
	}

	if !m.finished && m.curID != "" {
		b.WriteString("\n" + headerStyle.Render(fmt.Sprintf("▶ [%d/%d] %s", m.curIdx+1, m.total, m.curID)) +
			"  " + m.curType + dimStyle.Render(" ("+m.curSeverity+")") + "\n")
		for _, r := range m.rows {
			mark := m.spinner.View()
			right := r.action
			style := lipgloss.NewStyle()
			if r.done {
				mark = doneStyle.Render("·")
				right = r.verdict
				style = verdictStyle(r.verdict)
			}
			fmt.Fprintf(&b, "  %s %s  %s   %s\n",
				mark, labelStyle.Render(pad(r.label, 12)),
				style.Render(pad(right, 38)),
				costStyle.Render(fmt.Sprintf("$%.4f", r.cost)))
		}
	}

	if m.interrupting && !m.finished {
		amber := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
		msg := "interrupting… (finishing in-flight calls)"
		if m.saveReports {
			msg = "quitting… (finishing in-flight calls, then writing a partial report)"
		}
		b.WriteString("\n" + amber.Render(msg) + "\n")
	} else if m.quitArmed && !m.finished {
		amber := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
		b.WriteString("\n" + amber.Render("press q again to quit and write a partial report") + "\n")
	} else if !m.finished && m.curID != "" {
		b.WriteString("\n" + dimStyle.Render("q q: quit & save report   ·   ctrl+c: abort") + "\n")
	}

	if m.finished {
		switch {
		case m.saveReports:
			b.WriteString("\n" + headerStyle.Render(fmt.Sprintf("Stopped early. %d/%d findings · ", m.doneCount, m.total)) +
				m.tallyLine() + "  " +
				m.costLabel() +
				dimStyle.Render("  "+time.Since(m.start).Round(time.Second).String()) +
				dimStyle.Render("  (writing partial report)") + "\n")
		case m.err != nil:
			b.WriteString("\n" + verdictStyle("error").Render("interrupted: "+m.err.Error()) + "\n")
		default:
			b.WriteString("\n" + headerStyle.Render(fmt.Sprintf("Done. %d findings · ", m.total)) +
				m.tallyLine() + "  " +
				m.costLabel() +
				dimStyle.Render("  "+time.Since(m.start).Round(time.Second).String()) + "\n")
		}
	}
	return b.String()
}

// costLabel renders the live total cost, marked when any unpriced model
// contributes to it at $0.
func (m *Model) costLabel() string {
	s := costStyle.Render(fmt.Sprintf("$%.4f", m.totalCost))
	if len(m.header.Unpriced) > 0 {
		s += dimStyle.Render(" (unpriced: " + strings.Join(m.header.Unpriced, ", ") + ")")
	}
	return s
}

func cvssStyle(score float64) lipgloss.Style {
	switch {
	case score >= 9.0:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("203")) // critical, red
	case score >= 7.0:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // high, orange
	case score >= 4.0:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // medium, amber
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42")) // low, green
	}
}

func verdictStyle(v string) lipgloss.Style {
	s := lipgloss.NewStyle()
	up := strings.ToUpper(v)
	switch {
	case strings.Contains(up, "TRUE_POSITIVE"), strings.Contains(up, "REAL"), strings.Contains(up, "ERROR"), strings.Contains(up, "FAILED"):
		return s.Foreground(lipgloss.Color("203")) // red
	case strings.Contains(up, "FALSE_POSITIVE"), strings.Contains(up, "NOT_EXPLOITABLE"), strings.Contains(up, "UNLIKELY"):
		return s.Foreground(lipgloss.Color("42")) // green
	case strings.Contains(up, "NEEDS_MORE"), strings.Contains(up, "UNKNOWN"):
		return s.Foreground(lipgloss.Color("214")) // amber
	default:
		return s.Foreground(lipgloss.Color("245"))
	}
}

// Outcome tells the caller what to do once the dashboard exits.
//
//   - WriteReport is true on a normal finish and on a graceful "q q" quit; the
//     caller should write reports from whatever results it collected.
//   - WriteReport is false on a ctrl+c abort; results are discarded.
//   - Partial marks a graceful quit that stopped before every finding ran, so
//     the report covers only the completed ones.
//
// Err carries a fatal error (nil when WriteReport is true).
type Outcome struct {
	WriteReport bool
	Partial     bool
	Err         error
}

// Run starts the dashboard and runs work in a goroutine, feeding it a sink that
// forwards events to the program. It blocks until the run finishes or the user
// quits, then reports what the caller should do next. work must install the
// sink into its context (progress.WithSink) so the pipeline emits.
//
// The engine runs under workCtx, which the quit keys cancel; the Bubble Tea
// program watches the parent ctx instead. Cancelling work therefore stops the
// engine without tearing the UI down mid-frame — the program exits cleanly via
// doneMsg -> tea.Quit, so the final "Stopped early" frame renders. On a save or
// normal exit Run waits on the work goroutine (the done channel) before
// returning, so the caller never reads results while they are still being
// written.
func Run(ctx context.Context, h Header, work func(ctx context.Context, sink prog.Sink) error) Outcome {
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()

	m := NewModel(h, cancelWork, time.Now())
	p := tea.NewProgram(m, tea.WithContext(ctx))

	done := make(chan error, 1)
	sink := prog.Func(func(e prog.Event) { p.Send(eventMsg(e)) })
	go func() {
		err := work(workCtx, sink)
		done <- err // buffered: never blocks, even if nobody waits (forced quit)
		p.Send(doneMsg{err: err})
	}()

	if _, runErr := p.Run(); runErr != nil {
		// The program was killed by the parent ctx or a signal, not our quit
		// keys; the work goroutine is unsynchronized, so don't read results.
		return Outcome{Err: runErr}
	}
	// The event loop stopped cleanly (QuitMsg), so m is safe to read.
	if m.forced || (m.interrupting && !m.saveReports) {
		// User aborted (ctrl+c) or force-quit a hung unwind: discard results
		// without waiting on the possibly-stuck work goroutine.
		return Outcome{Err: context.Canceled}
	}
	// Normal finish or graceful "q q": wait for work to fully return so its
	// results are visible to the caller (happens-before through done).
	<-done
	if m.saveReports {
		return Outcome{WriteReport: true, Partial: true}
	}
	return Outcome{WriteReport: true, Err: m.err}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func pad(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		if n <= 1 {
			return string(r[:n])
		}
		return string(r[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-len(r))
}

func short(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}
