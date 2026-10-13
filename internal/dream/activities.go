package dream

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/llm"
	"saahil.local/brain/internal/rank"
	"saahil.local/brain/internal/store"
)

type Config struct {
	BrainRoot  string
	ShadowDocs []string // read-only doc roots presented as work/… (saahil-docs until the cut-over)
	SkillsDir  string   // <repo>/skills: <name>.md per skill
	BrainBin   string   // the brain CLI, for index / lint / render / capture
	Forbidden  []string // never written, even when reachable through a link
}

type Activities struct {
	Cfg    Config
	LLM    llm.Runner
	Ledger *llm.Ledger
}

func (a *Activities) path(parts ...string) string {
	return filepath.Join(append([]string{a.Cfg.BrainRoot}, parts...)...)
}

// ---- gate: material, watermarks, lock ----

type Material struct {
	Rel   string    `json:"rel"` // brain-relative; shadow docs appear as work/…
	Abs   string    `json:"abs"`
	MTime time.Time `json:"mtime"`
	Size  int64     `json:"size"`
}

type MaterialCounts struct {
	Sessions        int `json:"sessions"`
	SessionsWaiting int `json:"sessions_waiting,omitempty"`
	Captures        int `json:"captures"`
	CapturesWaiting int `json:"captures_waiting,omitempty"`
	Docs            int `json:"docs"`
	BigDocs         int `json:"big_docs"`
	BigDocsWaiting  int `json:"big_docs_waiting,omitempty"`
}

// State is .brain/dream-state.json: a watermark (newest mtime consumed) per kind of material. Only material areas
// are scanned — never notes/ or hubs/ — so the dream's own writes do not wake the next dream.
type State struct {
	LastRunID  string               `json:"last_run_id"`
	LastRun    time.Time            `json:"last_run"`
	Watermarks map[string]time.Time `json:"watermarks"`
}

type GateInput struct {
	RunID  string
	Now    time.Time
	Force  bool
	Shadow bool
}

type GateResult struct {
	Skip     string
	Counts   MaterialCounts
	Sessions []Material
	Captures []Material
	BigDocs  []Material
	Prev     map[string]time.Time
	Next     map[string]time.Time // per area; applied only for areas whose step ran
}

const bigDocBytes = config.SectionWarnBytes

func (a *Activities) Gate(_ context.Context, in GateInput) (GateResult, error) {
	st, err := a.loadState()
	if err != nil {
		return GateResult{}, err
	}
	res := GateResult{Prev: st.Watermarks, Next: map[string]time.Time{}}
	for k, v := range st.Watermarks {
		res.Next[k] = v
	}

	sessions, err := scan(st.Watermarks["sessions"], root{a.path("sessions"), "sessions"})
	if err != nil {
		return res, err
	}
	res.Sessions, res.Counts.SessionsWaiting = capped(sessions, MaxSessions, res.Next, "sessions")
	res.Counts.Sessions = len(res.Sessions)

	captures, err := scan(st.Watermarks["captures"],
		root{a.path("memory"), "memory"}, root{a.path("inbox"), "inbox"})
	if err != nil {
		return res, err
	}
	res.Captures, res.Counts.CapturesWaiting = capped(captures, MaxCaptures, res.Next, "captures")
	res.Counts.Captures = len(res.Captures)

	// Doc history before the first dream is the backfill's job; the dream starts from the last day.
	docsSince := st.Watermarks["docs"]
	if docsSince.IsZero() && st.LastRun.IsZero() {
		docsSince = in.Now.Add(-24 * time.Hour)
	}
	docRoots := []root{{a.path("work"), "work"}}
	if in.Shadow {
		for _, d := range a.Cfg.ShadowDocs {
			docRoots = append(docRoots, root{d, "work"})
		}
	}
	docs, err := scan(docsSince, docRoots...)
	if err != nil {
		return res, err
	}
	res.Counts.Docs = len(docs)
	var big []Material
	for _, d := range docs {
		if d.Size >= bigDocBytes {
			big = append(big, d)
		}
	}
	if len(big) > MaxBigDocs {
		res.BigDocs, res.Counts.BigDocsWaiting = capped(big, MaxBigDocs, res.Next, "docs")
	} else {
		capped(docs, 0, res.Next, "docs")
		res.BigDocs = big
	}
	res.Counts.BigDocs = len(res.BigDocs)

	switch {
	case res.Counts.Sessions+res.Counts.Captures+res.Counts.Docs == 0:
		res.Skip = "nothing new since the last dream"
	case !in.Force && !st.LastRun.IsZero() && in.Now.Sub(st.LastRun) < MinGap && res.Counts.Sessions < EarlySessions:
		res.Skip = fmt.Sprintf("waiting: %s since the last dream and %d new sessions (needs %s or %d)",
			in.Now.Sub(st.LastRun).Round(time.Minute), res.Counts.Sessions, MinGap, EarlySessions)
	default:
		res.Skip, err = a.acquireLock(in.RunID, in.Now)
	}
	if res.Skip != "" {
		res.Sessions, res.Captures, res.BigDocs = nil, nil, nil
	}
	return res, err
}

type root struct{ abs, relPrefix string }

// scan lists .md files changed after since, oldest first. It never follows links and skips dot-folders below a root.
func scan(since time.Time, roots ...root) ([]Material, error) {
	var out []Material
	for _, r := range roots {
		err := filepath.WalkDir(r.abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if p != r.abs && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || filepath.Ext(p) != ".md" || strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel, _ := filepath.Rel(r.abs, p)
			rel = filepath.Join(r.relPrefix, rel)
			if rel == filepath.Join("memory", "MEMORY.md") || isCard(rel) { // written by the dream itself
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			if fi.ModTime().After(since) {
				out = append(out, Material{Rel: rel, Abs: p, MTime: fi.ModTime().UTC(), Size: fi.Size()})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].MTime.Equal(out[j].MTime) {
			return out[i].MTime.Before(out[j].MTime)
		}
		return out[i].Rel < out[j].Rel
	})
	return out, nil
}

func isCard(rel string) bool {
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 3 && parts[0] == "work" && parts[2] == "CARD.md"
}

// capped takes the oldest n (n == 0: all) and moves the area's watermark to the newest one taken, so the rest waits.
func capped(found []Material, n int, next map[string]time.Time, area string) ([]Material, int) {
	waiting := 0
	if n > 0 && len(found) > n {
		waiting = len(found) - n
		found = found[:n]
	}
	if len(found) > 0 {
		next[area] = found[len(found)-1].MTime
	}
	return found, waiting
}

func (a *Activities) loadState() (State, error) {
	var st State
	b, err := os.ReadFile(a.path(".brain", "dream-state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

type lockFile struct {
	PID   int       `json:"pid"`
	Start time.Time `json:"start"`
	RunID string    `json:"run_id"`
}

// acquireLock returns a skip reason when another live run holds .brain/dream.lock; a lock older than 3 h is stale.
// The same run re-acquiring (an activity retry) is allowed.
func (a *Activities) acquireLock(runID string, now time.Time) (string, error) {
	p := a.path(".brain", "dream.lock")
	if b, err := os.ReadFile(p); err == nil {
		var held lockFile
		if json.Unmarshal(b, &held) == nil && held.RunID != runID && now.Sub(held.Start) < LockStaleAfter {
			return fmt.Sprintf("locked by run %s (pid %d) since %s", held.RunID, held.PID,
				held.Start.Format(time.RFC3339)), nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	b, _ := json.Marshal(lockFile{PID: os.Getpid(), Start: now, RunID: runID})
	return "", writeFile(p, b)
}

func (a *Activities) ReleaseLock(_ context.Context, runID string) error {
	p := a.path(".brain", "dream.lock")
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var held lockFile
	if json.Unmarshal(b, &held) == nil && held.RunID != runID {
		return nil
	}
	return os.Remove(p) // the lock file is the dream's own runtime state, not brain content
}

// ---- snapshot and commit (git of the brain's text) ----

type SnapshotResult struct {
	Commit  string
	Tarball string
}

// work/ holds every box (hundreds of GB of artifacts); the dream only writes CARD.md there, so that is all git tracks
var gitIgnored = []string{".index/", ".data/", ".archive/", ".import/", "*.db*", ".brain/dream.lock", ".tmp-*",
	"/work/*", "!/work/*/", "/work/*/*", "!/work/*/CARD.md"}

func (a *Activities) Snapshot(ctx context.Context, runID string) (SnapshotResult, error) {
	if err := a.ensureRepo(ctx); err != nil {
		return SnapshotResult{}, err
	}
	commit, err := a.gitCommit(ctx, "snapshot before dream "+runID)
	if err != nil {
		return SnapshotResult{}, err
	}
	tarball := a.path(".archive", "snapshots", runID+".tar.gz")
	if _, err := os.Stat(tarball); errors.Is(err, os.ErrNotExist) {
		if err := a.writeTarball(tarball, "memory", "notes"); err != nil {
			return SnapshotResult{}, err
		}
	}
	return SnapshotResult{Commit: commit, Tarball: tarball}, nil
}

func (a *Activities) ensureRepo(ctx context.Context) error {
	if _, err := os.Stat(a.path(".git")); errors.Is(err, os.ErrNotExist) {
		if _, err := a.git(ctx, "init", "-q", "-b", "main"); err != nil {
			return err
		}
	}
	existing, _ := os.ReadFile(a.path(".gitignore"))
	have := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, pat := range gitIgnored {
		if !have[pat] {
			add = append(add, pat)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		existing = append(existing, '\n')
	}
	return writeFile(a.path(".gitignore"), append(existing, []byte(strings.Join(add, "\n")+"\n")...))
}

// gitCommit commits everything not ignored; with nothing staged it returns HEAD (or makes the first, empty commit).
func (a *Activities) gitCommit(ctx context.Context, msg string) (string, error) {
	if _, err := a.git(ctx, "add", "-A"); err != nil {
		return "", err
	}
	_, diffErr := a.git(ctx, "diff", "--cached", "--quiet")
	head, headErr := a.git(ctx, "rev-parse", "--verify", "-q", "HEAD")
	if diffErr == nil && headErr == nil {
		return head, nil
	}
	if _, err := a.git(ctx, "commit", "-q", "--allow-empty", "-m", msg); err != nil {
		return "", err
	}
	return a.git(ctx, "rev-parse", "HEAD")
}

// git runs with a fixed identity and no hooks or signing: the brain's repo is local history, and the code repo's
// pre-commit guard (which refuses brain data) must not apply to it.
func (a *Activities) git(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-C", a.Cfg.BrainRoot, "-c", "user.name=brain-dream", "-c", "user.email=dream@brain.local",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (a *Activities) writeTarball(dst string, dirs ...string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, dir := range dirs {
		err := filepath.WalkDir(a.path(dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(fi, "")
			if err != nil {
				return err
			}
			hdr.Name, _ = filepath.Rel(a.Cfg.BrainRoot, p)
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			src, err := os.Open(p)
			if err != nil {
				return err
			}
			defer src.Close()
			_, err = io.Copy(tw, src)
			return err
		})
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := errors.Join(tw.Close(), gz.Close(), f.Close()); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

type CommitInput struct {
	Report Report
	State  State
}

// Commit advances the watermarks, writes the run report and commits the brain's text.
func (a *Activities) Commit(ctx context.Context, in CommitInput) (string, error) {
	b, err := json.MarshalIndent(in.State, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeFile(a.path(".brain", "dream-state.json"), b); err != nil {
		return "", err
	}
	if err := a.WriteReport(ctx, in.Report); err != nil {
		return "", err
	}
	r := in.Report
	return a.gitCommit(ctx, fmt.Sprintf("dream %s: %d edits, %d llm calls, $%.2f",
		r.Started.Format("2006-01-02"), r.Edits, r.LLMCalls, r.CostUSD))
}

// WriteReport keeps one file per run under .brain/dream-runs/; hourly skips overwrite a single last-skip file.
func (a *Activities) WriteReport(_ context.Context, rep Report) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if rep.Status == "skipped" {
		return writeFile(a.path(".brain", "dream-last-skip.json"), b)
	}
	return writeFile(a.path(".brain", "dream-runs", rep.RunID+".json"), b)
}

// ---- deterministic steps ----

type StepOutcome struct {
	Status   string // ok | skipped | failed
	Reason   string
	ExitCode int
}

// Demote applies the usage funnel's demotion to the index (rank.Demote): a ranking prior, never a status change or a
// file edit, so it needs no snapshot and is the same in shadow.
func (a *Activities) Demote(_ context.Context) (StepOutcome, error) {
	path := a.path(".index", "brain.db")
	if _, err := os.Stat(path); err != nil {
		return StepOutcome{Status: "skipped", Reason: "no index yet"}, nil
	}
	st, err := store.Open(path)
	if err != nil {
		return StepOutcome{}, err
	}
	defer st.Close()
	demoted, restored, err := rank.Demote(st.DB, time.Now())
	if err != nil {
		return StepOutcome{}, err
	}
	return StepOutcome{Status: "ok", Reason: fmt.Sprintf("demoted %d, restored %d (shown ≥ %d times as a pointer, never opened)",
		demoted, restored, rank.DemoteAfterShown)}, nil
}

// Reconcile reports sync-conflict copies; resolving them is manual until Unison runs (cut-over).
func (a *Activities) Reconcile(_ context.Context) (StepOutcome, error) {
	n := 0
	err := filepath.WalkDir(a.Cfg.BrainRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != a.Cfg.BrainRoot && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if !d.IsDir() && config.IsSyncConflictCopy(d.Name()) {
			n++
		}
		return nil
	})
	if err != nil {
		return StepOutcome{}, err
	}
	if n == 0 {
		return StepOutcome{Status: "ok", Reason: "no conflict copies"}, nil
	}
	return StepOutcome{Status: "ok", Reason: fmt.Sprintf("%d conflict copies left for the owner", n)}, nil
}

type BrainCommandInput struct {
	Command []string // e.g. ["lint"], ["sessions", "capture"]
	Args    []string
}

// BrainCommand runs a brain CLI step if this build has it; a missing command is a recorded skip, not a failure,
// because the index, lint and capture commands land from other areas. Output stays out of the step record, which
// reaches dream status and notifications (it may quote content); a failure's last line goes to the worker log.
func (a *Activities) BrainCommand(ctx context.Context, in BrainCommandInput) (StepOutcome, error) {
	name := "brain " + strings.Join(in.Command, " ")
	if _, err := os.Stat(a.Cfg.BrainBin); err != nil {
		return StepOutcome{Status: "skipped", Reason: "brain binary not found at " + a.Cfg.BrainBin}, nil
	}
	has, err := a.brainHas(ctx, strings.Join(in.Command, " "))
	if err != nil {
		return StepOutcome{}, err
	}
	if !has {
		return StepOutcome{Status: "skipped", Reason: "`" + name + "` does not exist yet"}, nil
	}
	cmd := exec.CommandContext(ctx, a.Cfg.BrainBin, append(in.Command, in.Args...)...)
	cmd.Env = append(os.Environ(), "BRAIN_ROOT="+a.Cfg.BrainRoot)
	cmd.Dir = a.Cfg.BrainRoot // step arguments are brain-relative paths (lint gets notes/x.md)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return StepOutcome{Status: "ok", Reason: name + ": exit 0"}, nil
	case errors.As(err, &exitErr):
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		last := lines[len(lines)-1]
		if len(last) > 300 {
			last = last[:300]
		}
		slog.Warn("brain step failed", "step", name, "exit", exitErr.ExitCode(), "last_line", last)
		return StepOutcome{Status: "failed", ExitCode: exitErr.ExitCode(),
			Reason: fmt.Sprintf("%s: exit %d (%d lines of output)", name, exitErr.ExitCode(), bytes.Count(out.Bytes(), []byte("\n")))}, nil
	default:
		return StepOutcome{}, err
	}
}

func (a *Activities) brainHas(ctx context.Context, path string) (bool, error) {
	out, err := exec.CommandContext(ctx, a.Cfg.BrainBin, "--help").Output()
	if err != nil {
		return false, fmt.Errorf("brain --help: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "brain "+path+" ") || line == "brain "+path {
			return true, nil
		}
	}
	return false, nil
}

// ---- LLM steps ----

type LLMStepInput struct {
	Workflow    string
	Step        string
	Skill       string
	Model       string
	BudgetUSD   float64
	RunSpentUSD float64
	Material    []Material
	Proposed    []Edit
}

type LLMStepResult struct {
	Status  string
	Reason  string
	CostUSD float64
	Called  bool
	Cached  bool
	StopLLM bool
	Edits   []Edit
	Refused int // ops the applier refused, other than deletes (those come back as delete edits)
}

const (
	fileContentCap  = 16 * 1024
	inputContentCap = 400 * 1024
)

type stepPayload struct {
	Step      string        `json:"step"`
	Material  []payloadFile `json:"material,omitempty"`
	Proposed  []Edit        `json:"proposed,omitempty"`
	Truncated int           `json:"files_left_out,omitempty"`
}

type payloadFile struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (a *Activities) LLMStep(ctx context.Context, in LLMStepInput) (LLMStepResult, error) {
	skill, err := a.readSkill(in.Skill)
	if errors.Is(err, os.ErrNotExist) {
		return LLMStepResult{Status: "skipped", Reason: "skill missing: " + in.Skill + ".md"}, nil
	}
	if err != nil {
		return LLMStepResult{}, err
	}
	input, err := buildInput(in)
	if err != nil {
		return LLMStepResult{}, err
	}
	key := llm.Key(in.Workflow, in.Step, in.Model, string(skill), input)

	cached, ok, err := a.Ledger.Done(key)
	if err != nil {
		return LLMStepResult{}, err
	}
	if ok {
		return withEdits(LLMStepResult{Status: "ok", Reason: "cached", Cached: true}, cached), nil
	}

	if in.RunSpentUSD+in.BudgetUSD > RunCapUSD {
		return LLMStepResult{Status: "skipped", StopLLM: true, Reason: fmt.Sprintf(
			"run cap: $%.2f spent + $%.2f step budget > $%.2f; no more LLM calls this run",
			in.RunSpentUSD, in.BudgetUSD, RunCapUSD)}, nil
	}
	now := time.Now().UTC()
	today, err := a.Ledger.SpentSince(now.Truncate(24 * time.Hour))
	if err != nil {
		return LLMStepResult{}, err
	}
	if today+in.BudgetUSD > DailyCapUSD {
		return LLMStepResult{Status: "skipped", StopLLM: true, Reason: fmt.Sprintf(
			"daily cap: $%.2f spent today + $%.2f step budget > $%.2f", today, in.BudgetUSD, DailyCapUSD)}, nil
	}

	if err := a.Ledger.Begin(key, in.Workflow, in.Step, in.Model, now); err != nil {
		return LLMStepResult{}, err
	}
	res, runErr := a.LLM.Run(ctx, llm.Call{Skill: string(skill), Input: input, Model: in.Model,
		MaxBudgetUSD: in.BudgetUSD, JSONSchema: EditsSchema})
	var modelErr *llm.ModelError
	switch {
	case errors.As(runErr, &modelErr):
		if err := a.Ledger.Finish(key, res, "failed", time.Now()); err != nil {
			return LLMStepResult{}, err
		}
		return LLMStepResult{Status: "failed", Reason: modelErr.Error(), CostUSD: res.CostUSD, Called: true}, nil
	case runErr != nil:
		_ = a.Ledger.Finish(key, res, "failed", time.Now())
		return LLMStepResult{}, runErr
	}
	if err := a.Ledger.Finish(key, res, "done", time.Now()); err != nil {
		return LLMStepResult{}, err
	}
	return withEdits(LLMStepResult{Status: "ok", CostUSD: res.CostUSD, Called: true}, res), nil
}

// buildInput reads the material now, so the key hashes the content actually sent; paths are brain-relative so the
// key does not depend on which spelling of home the worker sees.
func buildInput(in LLMStepInput) (string, error) {
	p := stepPayload{Step: in.Step, Proposed: in.Proposed}
	total := 0
	for _, m := range in.Material {
		b, err := os.ReadFile(m.Abs)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		f := payloadFile{Path: m.Rel}
		if len(b) > fileContentCap {
			b, f.Truncated = b[:fileContentCap], true
		}
		if total+len(b) > inputContentCap {
			p.Truncated++
			continue
		}
		total += len(b)
		f.Content = string(b)
		p.Material = append(p.Material, f)
	}
	b, err := json.Marshal(p)
	return string(b), err
}

func withEdits(r LLMStepResult, res llm.Result) LLMStepResult {
	raw := res.JSON
	if len(raw) == 0 {
		raw = json.RawMessage(res.Text)
	}
	var out struct {
		Edits []Edit `json:"edits"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		r.Status, r.Reason = "failed", "answer is not the edits JSON"
		return r
	}
	r.Edits = out.Edits
	return r
}
