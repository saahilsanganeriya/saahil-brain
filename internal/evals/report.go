package evals

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Cell struct {
	Value float64 `json:"value"`
	N     int     `json:"n"`
}

// Metric is one measured number. Navigation-style metrics carry the coordinator's three-way split (all items,
// human-typed first prompts, dispatched briefs); the pass mark is judged on All.
type Metric struct {
	Eval       string  `json:"eval"`
	Name       string  `json:"name"`
	Split      string  `json:"split,omitempty"` // dev | test; "" = every item
	Kind       string  `json:"kind"` // rate | lift | mean
	All        Cell    `json:"all"`
	Human      *Cell   `json:"human,omitempty"`
	Dispatched *Cell   `json:"dispatched,omitempty"`
	Mark       string  `json:"mark,omitempty"`
	Pass       *bool   `json:"pass,omitempty"`
	NA         string  `json:"na,omitempty"` // why it could not be measured
	min        float64 // the pass mark as a rate; 0 = none
}

func cellOf[R any](rows []R, include func(R) bool, value func(R) float64) Cell {
	var c Cell
	sum := 0.0
	for _, r := range rows {
		if include(r) {
			c.N++
			sum += value(r)
		}
	}
	if c.N > 0 {
		c.Value = sum / float64(c.N)
	}
	return c
}

func withMark(m Metric, min float64) Metric {
	if min > 0 {
		m.min, m.Mark = min, fmt.Sprintf("≥ %.0f%%", min*100)
		m.Pass = boolPtr(m.All.N > 0 && m.All.Value >= min)
	}
	if m.All.N == 0 {
		m.NA, m.Pass = "no items", nil
	}
	return m
}

func split3[R any](eval, name, kind string, rows []R, include, dispatched func(R) bool, value func(R) float64) Metric {
	human := cellOf(rows, func(r R) bool { return include(r) && !dispatched(r) }, value)
	disp := cellOf(rows, func(r R) bool { return include(r) && dispatched(r) }, value)
	return Metric{Eval: eval, Name: name, Kind: kind, All: cellOf(rows, include, value), Human: &human, Dispatched: &disp}
}

func split[R any](eval, name string, rows []R, include, hit, dispatched func(R) bool, min float64) Metric {
	return withMark(split3(eval, name, "rate", rows, include, dispatched, func(r R) float64 { return b2f(hit(r)) }), min)
}

func rate[R any](eval, name string, rows []R, include, hit func(R) bool, min float64) Metric {
	return withMark(Metric{Eval: eval, Name: name, Kind: "rate", All: cellOf(rows, include, func(r R) float64 { return b2f(hit(r)) })}, min)
}

func lift[R any](eval, name string, rows []R, dispatched func(R) bool, delta func(R) float64) Metric {
	return withMark(split3(eval, name, "lift", rows, func(R) bool { return true }, dispatched, delta), 0)
}

func mean[R any](eval, name string, rows []R, dispatched func(R) bool, value func(R) float64) Metric {
	return withMark(split3(eval, name, "mean", rows, func(R) bool { return true }, dispatched, value), 0)
}

func (m Metric) format(c *Cell) string {
	if c == nil {
		return "—"
	}
	if c.N == 0 {
		return "n/a (n=0)"
	}
	switch m.Kind {
	case "lift":
		return fmt.Sprintf("%+.1f pp (n=%d)", c.Value*100, c.N)
	case "mean":
		return fmt.Sprintf("%.2f (n=%d)", c.Value, c.N)
	}
	return fmt.Sprintf("%.1f%% (n=%d)", c.Value*100, c.N)
}

func (m Metric) verdict() string {
	switch {
	case m.NA != "":
		return "n/a — " + m.NA
	case m.Pass == nil:
		return "(no mark)"
	case *m.Pass:
		return "PASS " + m.Mark
	}
	return "FAIL " + m.Mark
}

// Line is the metric in one line for the terminal: all / human-typed / dispatched, then the verdict.
func (m Metric) Line() string {
	label := m.Eval
	if m.Split != "" {
		label += "/" + m.Split
	}
	if m.NA != "" && m.All.N == 0 {
		return fmt.Sprintf("%-15s %-52s %s", label, m.Name, m.verdict())
	}
	s := fmt.Sprintf("%-15s %-52s all %s", label, m.Name, m.format(&m.All))
	if m.Human != nil {
		s += fmt.Sprintf(" · human %s · dispatched %s", m.format(m.Human), m.format(m.Dispatched))
	}
	return s + "  " + m.verdict()
}

// ---- reports ----

type tableRow interface {
	header() []string
	cells() []string
}

type Report struct {
	Eval         string    `json:"eval"`
	Ran          time.Time `json:"ran"`
	SetsBuilt    time.Time `json:"sets_built"`
	IndexUpdated string    `json:"index_updated"`
	Ranker       string    `json:"ranker"`
	Metrics      []Metric  `json:"metrics"`
	Notes        []string  `json:"notes"`
	Rows         any       `json:"rows"`
	Worst        any       `json:"worst"`
	header       []string
	rowCells     [][]string
	worstCells   [][]string
}

func newReport[R tableRow](e *Env, eval string, metrics []Metric, rows, worst []R, notes []string) Report {
	r := Report{Eval: eval, Ran: e.Now.UTC(), SetsBuilt: e.sets.Manifest.Built, Ranker: fmt.Sprintf("%T", e.Ranker),
		Metrics: metrics, Notes: notes, Rows: rows, Worst: worst}
	_ = e.Index.DB().QueryRow(`SELECT coalesce((SELECT value FROM meta WHERE key = 'run.updated_at'), 'never')`).Scan(&r.IndexUpdated)
	var zero R
	r.header = zero.header()
	for _, row := range rows {
		r.rowCells = append(r.rowCells, row.cells())
	}
	for _, row := range worst {
		r.worstCells = append(r.worstCells, row.cells())
	}
	if rows == nil {
		r.Rows = []R{}
	}
	if worst == nil {
		r.Worst = []R{}
	}
	return r
}

// ReportsDir is where one day's reports go; a re-run the same day replaces them.
func ReportsDir(root string, ran time.Time) string {
	return filepath.Join(Dir(root), "reports", ran.UTC().Format(time.DateOnly))
}

// Write puts the report's JSON and markdown under ReportsDir and returns the markdown path.
func (r Report) Write(root string) (string, error) {
	dir := ReportsDir(root, r.Ran)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(dir, r.Eval+".json"), append(raw, '\n')); err != nil {
		return "", err
	}
	md := filepath.Join(dir, r.Eval+".md")
	return md, writeAtomic(md, []byte(r.markdown()))
}

func (r Report) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Eval: %s — %s\n\nran %s · sets built %s · index updated %s · ranker `%s`\n\n", r.Eval,
		r.Ran.Format(time.DateOnly), r.Ran.Format(time.RFC3339), r.SetsBuilt.Format(time.RFC3339), r.IndexUpdated, r.Ranker)
	b.WriteString("| split | metric | all | human-typed | dispatched | verdict |\n|---|---|---|---|---|---|\n")
	for _, m := range r.Metrics {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", cmp.Or(m.Split, "all"), m.Name, m.format(&m.All), m.format(m.Human), m.format(m.Dispatched), cell(m.verdict()))
	}
	b.WriteString("\n## Notes\n\n")
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	fmt.Fprintf(&b, "\n## Worst %d\n\n", len(r.worstCells))
	table(&b, r.header, r.worstCells)
	fmt.Fprintf(&b, "\n## Every item (%d)\n\n", len(r.rowCells))
	table(&b, r.header, r.rowCells)
	return b.String()
}

func table(b *strings.Builder, header []string, rows [][]string) {
	if len(rows) == 0 {
		b.WriteString("(none)\n")
		return
	}
	fmt.Fprintf(b, "| %s |\n|%s\n", strings.Join(header, " | "), strings.Repeat("---|", len(header)))
	for _, row := range rows {
		for i := range row {
			row[i] = cell(row[i])
		}
		fmt.Fprintf(b, "| %s |\n", strings.Join(row, " | "))
	}
}

func cell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ") }

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func callsCell(n int) string {
	if n == 0 {
		return "> 3"
	}
	return strconv.Itoa(n)
}

func list(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	return strings.Join(ids, ", ")
}

func (NavRow) header() []string {
	return []string{"id", "split", "session", "dispatched", "query from", "targets", "top-3", "box top-3", "calls", "pointers"}
}

func (r NavRow) cells() []string {
	q := r.Query
	if r.Error != "" {
		q = "error: " + r.Error
	}
	return []string{r.ID, r.Split, r.Session, yes(r.Dispatched), q, strconv.Itoa(r.Targets), yes(r.Top3), yes(r.BoxTop3), callsCell(r.Calls), list(r.Pointers)}
}

func (QARow) header() []string {
	return []string{"id", "split", "kind", "source", "question", "target", "hit", "topic hit", "returned"}
}

func (r QARow) cells() []string {
	return []string{r.ID, r.Split, string(r.Kind), r.Source, r.Question, cmp.Or(r.Target, "(abstain)"), yes(r.Hit), yes(r.TopicHit), list(r.Returned)}
}

func (MistakeRow) header() []string {
	return []string{"id", "split", "session", "dispatched", "lessons", "surfaced", "pointers"}
}

func (r MistakeRow) cells() []string {
	return []string{r.ID, r.Split, r.Session, yes(r.Dispatched), strconv.Itoa(r.Lessons), yes(r.Surfaced), list(r.Pointers)}
}

func (ABRow) header() []string {
	return []string{"id", "split", "dispatched", "brain top-3", "grep top-3", "brain calls", "grep calls"}
}

func (r ABRow) cells() []string {
	return []string{r.ID, r.Split, yes(r.Dispatched), yes(r.BrainTop3), yes(r.GrepTop3), callsCell(r.BrainCalls), callsCell(r.GrepCalls)}
}

// RunAll runs every eval on the stored sets (rebuilding them first when build is set) and writes each report.
func RunAll(e *Env, build bool) ([]Report, error) {
	if build {
		sets, err := Build(e.Index, BuildOptions{Now: e.Now})
		if err != nil {
			return nil, err
		}
		e.sets = sets
	}
	var out []Report
	for _, name := range Names {
		rep, err := e.Run(name)
		if err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		if _, err := rep.Write(e.Index.Root); err != nil {
			return out, err
		}
		out = append(out, rep)
	}
	return out, nil
}

// Sizes are the item counts of the sets the evals run on.
func (e *Env) Sizes() map[string]int { return e.sets.Manifest.Sizes }
