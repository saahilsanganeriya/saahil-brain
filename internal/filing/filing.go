// Package filing turns the imported memory folders (.import/<machine>/) into notes through the `file` skill, once per
// memory however many copies of it the two machines hold, and accounts for every imported file.
package filing

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/llm"
)

const (
	reasonIndex   = "folder index (MEMORY.md)"
	reasonMissing = "listed in the manifest, missing on disk"
	reasonNotMD   = "not markdown"
	reasonGaveUp  = "left out by two answers"
	maxAttempts   = 2
	contentCap    = 16 * 1024
)

type Item struct {
	Machine  string    `json:"machine"`
	Path     string    `json:"path"` // brain-relative: .import/<machine>/<folder>/<file>
	Name     string    `json:"-"`
	AsOf     string    `json:"as_of"`
	Copies   []string  `json:"copies,omitempty"` // other imported paths holding the same memory
	Attempt  int       `json:"attempt,omitempty"`
	SHA      string    `json:"-"`
	shas     []string  // this copy's and every duplicate's
	bodySHA  string    // content without frontmatter: the two machines' copies differ only there
	modified time.Time // newest copy wins
}

type Reconcile struct {
	Imported         int            `json:"imported"` // manifest entries
	ByMachine        map[string]int `json:"by_machine"`
	NotInManifest    int            `json:"not_in_manifest"` // on disk only; outside the count
	Unique           int            `json:"unique"`          // distinct memories
	AlreadyFiled     int            `json:"already_filed"`
	ThisRun          int            `json:"this_run"`
	Waiting          int            `json:"waiting"` // beyond --limit
	Duplicates       int            `json:"duplicates"`
	CrossMachine     int            `json:"cross_machine"` // duplicates whose kept copy is on the other machine
	Skipped          map[string]int `json:"skipped"`
	OrphanIndexLines int            `json:"orphan_index_lines"` // MEMORY.md lines whose file is gone: facts only there
}

// Balanced is the filing invariant: every imported file is filed, in flight, waiting, a duplicate or skipped.
func (r Reconcile) Balanced() bool {
	n := r.AlreadyFiled + r.ThisRun + r.Waiting + r.Duplicates
	for _, c := range r.Skipped {
		n += c
	}
	return n == r.Imported
}

type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Plan struct {
	Reconcile Reconcile `json:"reconcile"`
	Batches   [][]Item  `json:"-"`
	Skips     []Skip    `json:"skips"`
}

type filed struct {
	Path  string    `json:"path"`
	Notes []string  `json:"notes"`
	At    time.Time `json:"at"`
}

// state is .brain/filing-state.json, keyed by sha256 so a renamed or re-imported copy is still known.
type state struct {
	Filed    map[string]filed `json:"filed"`
	Attempts map[string]int   `json:"attempts"` // answers that left the memory out
}

func statePath(root string) string { return filepath.Join(root, ".brain", "filing-state.json") }

func loadState(root string) (state, error) {
	st := state{Filed: map[string]filed{}, Attempts: map[string]int{}}
	b, err := os.ReadFile(statePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

var (
	modifiedRE  = regexp.MustCompile(`(?m)^\s*modified:\s*["']?([0-9T:.\-+Z]+)`)
	indexLinkRE = regexp.MustCompile(`\]\(([^)#\s]+\.md)\)`)
)

// NewPlan inventories the import of the given machines against its manifests and the filing state.
func NewPlan(root string, machines []string, batch, limit int) (Plan, error) {
	st, err := loadState(root)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Reconcile: Reconcile{Skipped: map[string]int{}, ByMachine: map[string]int{}}}
	r := &p.Reconcile
	skip := func(path, reason string) {
		r.Skipped[reason]++
		p.Skips = append(p.Skips, Skip{path, reason})
	}
	var items []*Item
	for _, m := range machines {
		dir := filepath.Join(root, ".import", m)
		listed, err := readManifest(filepath.Join(root, ".import", m+"-manifest.tsv"))
		if err != nil {
			return p, err
		}
		r.Imported += len(listed)
		r.ByMachine[m] = len(listed)
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if rel, _ := filepath.Rel(dir, path); !hasKey(listed, rel) {
					r.NotInManifest++
				}
			}
			return nil
		})
		for rel, mtime := range listed {
			path := filepath.Join(".import", m, rel)
			b, err := os.ReadFile(filepath.Join(root, path))
			switch {
			case errors.Is(err, os.ErrNotExist):
				skip(path, reasonMissing)
				continue
			case err != nil:
				return p, err
			case filepath.Base(rel) == "MEMORY.md":
				r.OrphanIndexLines += orphanLines(filepath.Join(dir, filepath.Dir(rel)), b)
				skip(path, reasonIndex)
				continue
			case filepath.Ext(rel) != ".md":
				skip(path, reasonNotMD)
				continue
			}
			it := &Item{Machine: m, Path: path, Name: filepath.Base(rel), SHA: hash(b), bodySHA: hash([]byte(body(b))),
				modified: modifiedAt(b, mtime, filepath.Join(root, path))}
			it.AsOf, it.shas = it.modified.UTC().Format("2006-01-02"), []string{it.SHA}
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].modified.Equal(items[j].modified) {
			return items[i].modified.After(items[j].modified)
		}
		return items[i].Path < items[j].Path
	})
	bySHA, byNameBody := map[string]*Item{}, map[string]*Item{}
	var pending []Item
	var canon []*Item
	for _, it := range items {
		keep := bySHA[it.SHA]
		if keep == nil {
			keep = byNameBody[it.Name+"\x00"+it.bodySHA]
		}
		if keep != nil {
			keep.Copies, keep.shas = append(keep.Copies, it.Path), append(keep.shas, it.SHA)
			r.Duplicates++
			if keep.Machine != it.Machine {
				r.CrossMachine++
			}
			continue
		}
		bySHA[it.SHA], byNameBody[it.Name+"\x00"+it.bodySHA] = it, it
		canon = append(canon, it)
	}
	r.Unique = len(canon)
	for _, it := range canon {
		switch it.Attempt = st.Attempts[it.SHA]; {
		case anyFiled(st, it.shas):
			r.AlreadyFiled++
		case it.Attempt >= maxAttempts:
			skip(it.Path, reasonGaveUp)
		default:
			pending = append(pending, *it)
		}
	}
	// same-named memories sit next to each other, so diverged copies meet in one batch
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Name != pending[j].Name {
			return pending[i].Name < pending[j].Name
		}
		return pending[i].Path < pending[j].Path
	})
	if limit > 0 && len(pending) > limit {
		r.Waiting, pending = len(pending)-limit, pending[:limit]
	}
	r.ThisRun = len(pending)
	for batch > 0 && len(pending) > 0 {
		n := min(batch, len(pending))
		p.Batches, pending = append(p.Batches, pending[:n]), pending[n:]
	}
	return p, nil
}

func hasKey(m map[string]time.Time, k string) bool { _, ok := m[k]; return ok }

func anyFiled(st state, shas []string) bool {
	for _, s := range shas {
		if _, ok := st.Filed[s]; ok {
			return true
		}
	}
	return false
}

// readManifest maps each listed path to the modified time the manifest carries (devbox: column 3), else zero.
func readManifest(path string) (map[string]time.Time, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]time.Time{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]time.Time{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		cols := strings.Split(sc.Text(), "\t")
		if cols[0] == "" {
			continue
		}
		var t time.Time
		if len(cols) > 2 {
			t, _ = time.Parse(time.RFC3339Nano, cols[2])
		}
		out[strings.TrimPrefix(cols[0], "./")] = t
	}
	return out, sc.Err()
}

func modifiedAt(b []byte, manifest time.Time, abs string) time.Time {
	if m := modifiedRE.FindSubmatch([]byte(frontmatter(b))); m != nil {
		if t, err := time.Parse(time.RFC3339Nano, string(m[1])); err == nil {
			return t
		}
	}
	if !manifest.IsZero() {
		return manifest
	}
	if fi, err := os.Stat(abs); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

func frontmatter(b []byte) string {
	if rest, ok := strings.CutPrefix(string(b), "---\n"); ok {
		fm, _, _ := strings.Cut(rest, "\n---\n")
		return fm
	}
	return ""
}

func body(b []byte) string {
	if rest, ok := strings.CutPrefix(string(b), "---\n"); ok {
		if _, after, ok := strings.Cut(rest, "\n---\n"); ok {
			return strings.TrimSpace(after)
		}
	}
	return strings.TrimSpace(string(b))
}

func orphanLines(dir string, index []byte) int {
	n := 0
	for _, m := range indexLinkRE.FindAllSubmatch(index, -1) {
		if _, err := os.Stat(filepath.Join(dir, string(m[1]))); errors.Is(err, os.ErrNotExist) {
			n++
		}
	}
	return n
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---- running the skill ----

type Config struct {
	Root      string
	Machines  []string
	Batch     int
	Limit     int
	Model     string
	Runner    llm.Runner
	Ledger    *llm.Ledger
	Skill     string
	BudgetUSD float64 // run cap; a batch is sent only while spent + its per-call budget fits
	Now       time.Time
}

type Result struct {
	Reconcile  Reconcile `json:"reconcile"`
	FiledNow   int       `json:"filed_now"`
	Unfiled    int       `json:"unfiled"` // sent, but no applied note cites it: retried once
	Batches    int       `json:"batches"`
	Calls      int       `json:"calls"`
	Cached     int       `json:"cached"`
	CostUSD    float64   `json:"cost_usd"`
	Rejected   int       `json:"rejected_ops"`
	BadAnswers int       `json:"bad_answers,omitempty"` // not a delta: its memories count as left out
	Stopped    string    `json:"stopped,omitempty"`
	Skips      []Skip    `json:"skips,omitempty"`
}

type batchInput struct {
	Step     string     `json:"step"`
	Today    string     `json:"today"`
	Hubs     []HubEntry `json:"hubs"`
	Material []material `json:"material"`
}

type material struct {
	Item
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Run files every pending batch, saving the state after each, so a stop or crash resumes where it left off.
func Run(ctx context.Context, cfg Config) (Result, error) {
	plan, err := NewPlan(cfg.Root, cfg.Machines, cfg.Batch, cfg.Limit)
	if err != nil {
		return Result{}, err
	}
	res := Result{Reconcile: plan.Reconcile, Skips: plan.Skips}
	st, err := loadState(cfg.Root)
	if err != nil {
		return res, err
	}
	hubs, err := Hubs(cfg.Root)
	if err != nil {
		return res, err
	}
	perCall := 0.50
	if cfg.Model == config.ModelJudgment {
		perCall = 2.50
	}
	for _, batch := range plan.Batches {
		if res.CostUSD+perCall > cfg.BudgetUSD {
			res.Stopped = fmt.Sprintf("budget: $%.2f spent + $%.2f per call > $%.2f", res.CostUSD, perCall, cfg.BudgetUSD)
			break
		}
		in := batchInput{Step: "file", Today: cfg.Now.UTC().Format("2006-01-02"), Hubs: hubs}
		for _, it := range batch {
			b, err := os.ReadFile(filepath.Join(cfg.Root, it.Path))
			if err != nil {
				return res, err
			}
			m := material{Item: it, Truncated: len(b) > contentCap}
			m.Content = string(b[:min(len(b), contentCap)])
			in.Material = append(in.Material, m)
		}
		input, _ := json.Marshal(in)
		answer, err := ask(ctx, cfg, string(input), perCall, &res)
		if err != nil {
			return res, err
		}
		res.Batches++
		var d delta.Delta
		cited := map[string][]string{}
		if err := json.Unmarshal(answer, &d); err != nil {
			res.BadAnswers++ // its memories count as left out; their attempt marker makes the retry a new question
		} else {
			rep, err := delta.Apply(ctx, cfg.Root, d, delta.Options{Now: cfg.Now})
			if err != nil {
				return res, err
			}
			res.Rejected += len(rep.Rejected)
			cited = citedSources(d, rep)
		}
		for _, it := range batch {
			notes := cited[it.Path]
			for _, c := range it.Copies {
				notes = append(notes, cited[c]...)
			}
			if len(notes) == 0 {
				st.Attempts[it.SHA]++
				res.Unfiled++
				continue
			}
			for _, s := range it.shas {
				st.Filed[s] = filed{Path: it.Path, Notes: notes, At: cfg.Now}
			}
			delete(st.Attempts, it.SHA)
			res.FiledNow++
		}
		b, _ := json.MarshalIndent(st, "", " ")
		if err := writeAtomic(statePath(cfg.Root), b); err != nil {
			return res, err
		}
	}
	b, _ := json.MarshalIndent(res, "", " ")
	return res, writeAtomic(filepath.Join(cfg.Root, ".brain", "filing-report.json"), b)
}

// ask answers from the ledger when this exact batch was answered before; otherwise it pays for one call.
func ask(ctx context.Context, cfg Config, input string, perCall float64, res *Result) ([]byte, error) {
	key := llm.Key("filing", "file", cfg.Model, cfg.Skill, input)
	out, ok, err := cfg.Ledger.Done(key)
	if err != nil {
		return nil, err
	}
	if ok {
		res.Cached++
	} else {
		if err := cfg.Ledger.Begin(key, "filing", "file", cfg.Model, time.Now()); err != nil {
			return nil, err
		}
		var runErr error
		out, runErr = cfg.Runner.Run(ctx, llm.Call{Skill: cfg.Skill, Input: input, Model: cfg.Model,
			MaxBudgetUSD: perCall, JSONSchema: delta.Schema})
		res.Calls++
		res.CostUSD += out.CostUSD
		status := "done"
		if runErr != nil {
			status = "failed"
		}
		if err := cfg.Ledger.Finish(key, out, status, time.Now()); err != nil || runErr != nil {
			return nil, errors.Join(runErr, err)
		}
	}
	if len(out.JSON) > 0 {
		return out.JSON, nil
	}
	return []byte(out.Text), nil
}

// citedSources maps each source path of a note that an accepted op added or updated to that note's id.
func citedSources(d delta.Delta, rep delta.Report) map[string][]string {
	rejected := map[int]bool{}
	for _, r := range rep.Rejected {
		rejected[r.Index] = true
	}
	out := map[string][]string{}
	for i, op := range d.Ops {
		if rejected[i] {
			continue
		}
		var id string
		var sources []string
		switch {
		case op.Op == "add_note" && op.Note != nil:
			id, sources = op.Note.ID, op.Note.Sources
		case op.Op == "update_note":
			id = op.ID
			_ = json.Unmarshal(op.Set["sources"], &sources)
		}
		for _, s := range sources {
			out[s] = append(out[s], id)
		}
	}
	return out
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
