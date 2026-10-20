package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"saahil.local/brain/internal/config"
)

const (
	MaxNoteBytes = 8 * 1024
	maxDidBytes  = 2 * 1024
	maxListed    = 60
	slugMaxLen   = 40
)

type Options struct {
	BrainRoot string
	Machine   config.Machine
	Force     bool // rewrite even when the transcript is unchanged
}

type Status string

const (
	Written   Status = "written"
	Unchanged Status = "unchanged"
	Skipped   Status = "skipped"
)

type Result struct {
	Path         string
	Status       Status
	Reason       string // why it was skipped
	Subagents    int
	SkippedLines int
}

// Note writes (or leaves alone) the session note for a transcript; a subagent transcript means its main one. The note
// is rewritten only when the transcript grew, so hooks can call it freely.
func Note(transcript string, opt Options) (Result, error) {
	transcript, err := filepath.Abs(MainTranscript(transcript))
	if err != nil {
		return Result{}, err
	}
	dir := filepath.Join(opt.BrainRoot, "sessions", string(opt.Machine))
	if strings.Contains(config.Canonical(dir)+"/", "/saahil-docs/") {
		return Result{}, fmt.Errorf("refusing to write session notes under saahil-docs: %s", dir)
	}
	info, err := os.Stat(transcript)
	if err != nil {
		return Result{}, err
	}
	if newerCopy(transcript, info) {
		return Result{Status: Skipped, Reason: "older copy"}, nil
	}
	fp := fingerprint{size: info.Size(), mtime: info.ModTime().UTC().Format(time.RFC3339Nano), redact: RedactVersion}
	id := strings.TrimSuffix(filepath.Base(transcript), ".jsonl")
	existing := existingNotes(dir, id)
	if !opt.Force {
		for path, old := range existing {
			if old.size == fp.size && old.redact == fp.redact {
				return Result{Path: path, Status: Unchanged}, nil
			}
		}
	}
	s, err := Parse(transcript)
	if err != nil {
		return Result{}, err
	}
	res := Result{Subagents: s.Subagents, SkippedLines: s.SkippedLines}
	switch {
	case s.Entries == 0:
		res.Status, res.Reason = Skipped, "no messages"
		return res, nil
	case s.Started.IsZero():
		res.Status, res.Reason = Skipped, "no timestamps"
		return res, nil
	}
	// a note keeps the path of its first write: notes cite sessions by path, so a rename changes name:, not the file
	res.Path = filepath.Join(dir, s.Started.Format("2006"), s.Started.Format("01"),
		fmt.Sprintf("%s-%s-%s.md", s.Started.Format("2006-01-02"), short(s.ID), slugFor(s)))
	if len(existing) > 0 {
		res.Path = slices.Min(slices.Collect(maps.Keys(existing)))
	}
	if err := writeAtomic(res.Path, renderFitted(s, opt.Machine, fp)); err != nil {
		return Result{}, err
	}
	for path := range existing {
		if path != res.Path {
			_ = os.Remove(path) // a second note of the same session (an older writer renamed it): one note per session
		}
	}
	res.Status = Written
	return res, nil
}

// newerCopy reports whether the same session also lives under another project dir in a copy that wins: a session moved
// to another worktree keeps its id and continues in the new copy, so the larger (then newer, then later-named) one is
// the session and the other is not noted at all.
func newerCopy(transcript string, info os.FileInfo) bool {
	others, _ := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(transcript)), "*", filepath.Base(transcript)))
	for _, other := range others {
		o, err := os.Stat(other)
		if other == transcript || err != nil {
			continue
		}
		switch {
		case o.Size() != info.Size():
			if o.Size() > info.Size() {
				return true
			}
		case !o.ModTime().Equal(info.ModTime()):
			if o.ModTime().After(info.ModTime()) {
				return true
			}
		case other > transcript:
			return true
		}
	}
	return false
}

// fingerprint decides whether a note is current: same transcript size AND written by the current redactor. A transcript
// only grows by appending, so its size is its content; the mtime is recorded but not compared, because an idle
// session's process moves it every hour without writing a byte.
type fingerprint struct {
	size   int64
	mtime  string
	redact int
}

// existingNotes finds this session's notes (any month, any slug) by the session_id in their frontmatter.
func existingNotes(dir, id string) map[string]fingerprint {
	out := map[string]fingerprint{}
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*-"+short(id)+"-*.md"))
	for _, path := range matches {
		front := readFrontmatter(path)
		if front["session_id"] != id {
			continue
		}
		size, _ := strconv.ParseInt(front["transcript_size"], 10, 64)
		redact, _ := strconv.Atoi(front["redact_version"])
		out[path] = fingerprint{size: size, mtime: front["transcript_mtime"], redact: redact}
	}
	return out
}

func readFrontmatter(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), MaxNoteBytes*2)
	for i := 0; sc.Scan(); i++ {
		line := sc.Text()
		if i == 0 && line == "---" {
			continue
		}
		if line == "---" {
			break
		}
		if k, v, ok := strings.Cut(line, ": "); ok {
			if unq, err := strconv.Unquote(v); err == nil {
				v = unq
			}
			out[k] = v
		}
	}
	return out
}

func writeAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".note-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugFor is ascii kebab-case from the session name, else the first owner prompt, ≤ 40 chars on a word boundary.
func slugFor(s *Session) string {
	source := s.Name()
	if source == "" && len(s.Prompts) > 0 {
		source = s.Prompts[0].Text
	}
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(Redact(source)), "-"), "-")
	if len(slug) > slugMaxLen {
		slug = slug[:slugMaxLen]
		if i := strings.LastIndex(slug, "-"); i > slugMaxLen/2 {
			slug = slug[:i]
		}
		slug = strings.Trim(slug, "-")
	}
	if slug == "" {
		return "session"
	}
	return slug
}

// ---- rendering ----

type limits struct{ touched, produced, prompts int }

// renderFitted lays the note out per FORMAT.md and fits it under MaxNoteBytes by trimming the least-used files first, then
// later prompts. The same transcript always renders to the same bytes.
func renderFitted(s *Session, machine config.Machine, fp fingerprint) []byte {
	try := func(lim limits) ([]byte, bool) {
		out := Redact(render(s, machine, fp, lim))
		return []byte(out), len(out) <= MaxNoteBytes
	}
	lim := limits{touched: maxListed, produced: maxListed, prompts: len(s.Prompts)}
	for {
		if out, ok := try(lim); ok {
			return out
		}
		if lim.touched <= 20 && lim.produced <= 20 {
			break
		}
		lim.touched, lim.produced = max(20, lim.touched-5), max(20, lim.produced-5)
	}
	// Keep as many of the earliest prompts as fit; fewer prompts is never more bytes, so binary search.
	if lo := min(5, len(s.Prompts)); lo < len(s.Prompts) {
		if _, ok := try(limits{lim.touched, lim.produced, lo}); ok {
			hi := len(s.Prompts)
			for hi-lo > 1 {
				mid := (lo + hi) / 2
				if _, ok := try(limits{lim.touched, lim.produced, mid}); ok {
					lo = mid
				} else {
					hi = mid
				}
			}
			out, _ := try(limits{lim.touched, lim.produced, lo})
			return out
		}
	}
	for _, last := range []limits{{10, 10, 1}, {0, 0, 1}} {
		if out, ok := try(last); ok {
			return out
		}
	}
	out, _ := try(limits{0, 0, 0})
	return []byte(truncateBytes(string(out), MaxNoteBytes-1) + "\n")
}

func render(s *Session, machine config.Machine, fp fingerprint, lim limits) string {
	self := string(machine) + ":" + short(s.ID)
	if s.Renamed != "" {
		self = string(machine) + ":" + s.Renamed
	}
	touched := firstN(s.paths.ranked(false), lim.touched)
	produced := firstN(s.paths.ranked(true), lim.produced)
	messages := make([]string, 0, len(s.Messages))
	for _, m := range s.Messages {
		from, to := orSelf(m.From, self), orSelf(m.To, self)
		messages = append(messages, fmt.Sprintf("{from: %s, to: %s, id: %s}", yamlScalar(from), yamlScalar(to), yamlScalar(m.ID)))
	}

	var b strings.Builder
	field := func(k, v string) { fmt.Fprintf(&b, "%s: %s\n", k, v) }
	b.WriteString("---\n")
	field("id", "session-"+s.Started.Format("2006-01-02")+"-"+short(s.ID))
	field("type", "session")
	field("machine", string(machine))
	field("session_id", yamlScalar(s.ID))
	field("name", yamlScalar(collapseSpace(s.Name())))
	field("cwd", yamlScalar(s.CWD))
	field("started", s.Started.UTC().Format(time.RFC3339))
	field("ended", s.Ended.UTC().Format(time.RFC3339))
	field("transcript", yamlScalar(s.Transcript))
	field("transcript_size", strconv.FormatInt(fp.size, 10))
	field("transcript_mtime", yamlScalar(fp.mtime))
	field("redact_version", strconv.Itoa(fp.redact))
	field("subagents", strconv.Itoa(s.Subagents))
	field("works", yamlList(s.paths.works()))
	field("touched", yamlList(touched))
	field("produced", yamlList(produced))
	field("prs", yamlList(s.PRs))
	field("corrections", strconv.Itoa(s.Corrections))
	field("messages", "["+strings.Join(messages, ", ")+"]")
	b.WriteString("---\n\n## Owner prompts\n\n")
	if len(s.Prompts) == 0 {
		b.WriteString("(none)\n")
	}
	for _, p := range firstN(s.Prompts, lim.prompts) {
		fmt.Fprintf(&b, "- %s %s\n", p.At.UTC().Format("01-02 15:04Z"), p.Text)
	}
	if hidden := s.PromptCount - min(lim.prompts, len(s.Prompts)); hidden > 0 {
		fmt.Fprintf(&b, "- (+%d more not shown)\n", hidden)
	}
	b.WriteString("\n## What it did\n\n")
	b.WriteString(whatItDid(s))
	b.WriteString("\n## Files\n\n")
	b.WriteString(filesByBox(touched, produced, s))
	return b.String()
}

// whatItDid is the session's own account: short summaries, the last compact summary, the final message; ≤ 2 KB.
func whatItDid(s *Session) string {
	final := truncateBytes(collapseSpace(Redact(s.FinalText)), 900)
	var lines []string
	if len(s.Summaries) > 0 {
		lines = append(lines, truncateBytes("Summaries: "+strings.Join(s.Summaries, "; "), 300))
	}
	used := len(final) + 40
	for _, l := range lines {
		used += len(l) + 1
	}
	if s.Compact != "" && maxDidBytes-used > 100 {
		lines = append(lines, truncateBytes("Compact summary: "+collapseSpace(s.Compact), maxDidBytes-used))
	}
	if final != "" {
		lines = append(lines, "Final message: "+final)
	}
	if len(lines) == 0 {
		return "(nothing recorded)\n"
	}
	return strings.Join(lines, "\n\n") + "\n"
}

// filesByBox groups the listed files under their work box; a trailing * marks a file the session wrote.
func filesByBox(touched, produced []string, s *Session) string {
	isProduced := map[string]bool{}
	for _, p := range produced {
		isProduced[p] = true
	}
	groups := map[string][]string{}
	var order []string
	seen := map[string]bool{}
	for _, path := range append(append([]string{}, touched...), produced...) {
		if seen[path] {
			continue
		}
		seen[path] = true
		group, name := "other", path
		if box := boxOf(path); box != "" {
			group = "work/" + box
			name = strings.TrimPrefix(strings.TrimPrefix(path, group), "/")
			if name == "" {
				name = "."
			}
		}
		if isProduced[path] {
			name += "*"
		}
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], name)
	}
	if len(order) == 0 {
		return "(none)\n"
	}
	// work boxes in the order of `works`, then everything else
	rank := map[string]int{}
	for i, box := range s.paths.works() {
		rank["work/"+box] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		ri, iok := rank[order[i]]
		rj, jok := rank[order[j]]
		if iok != jok {
			return iok
		}
		return iok && ri < rj
	})
	var b strings.Builder
	for _, g := range order {
		fmt.Fprintf(&b, "- %s: %s\n", g, strings.Join(groups[g], ", "))
	}
	return b.String()
}

func orSelf(v, self string) string {
	if v == "" {
		return self
	}
	return v
}

func firstN[T any](list []T, n int) []T {
	if n < len(list) {
		return list[:n]
	}
	return list
}

var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_./~@+-][A-Za-z0-9_./~@+=#-]*$`)

// yamlScalar leaves safe words bare and double-quotes the rest (a JSON string is a valid YAML double-quoted scalar).
func yamlScalar(s string) string {
	switch strings.ToLower(s) {
	case "", "true", "false", "null", "yes", "no", "on", "off", "~":
		return strconv.Quote(s)
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil || !plainScalar.MatchString(s) {
		raw, _ := json.Marshal(s)
		return string(raw)
	}
	return s
}

func yamlList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = yamlScalar(item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// ---- backfill ----

type BackfillOptions struct {
	Options
	ProjectsDir string    // ~/.claude/projects
	Since       time.Time // skip transcripts last modified before this; zero means all
	Workers     int
	OnError     func(transcript string, err error)
}

type Counts struct {
	Transcripts  int // main transcripts considered
	Older        int // left out by --since
	Written      int
	Unchanged    int
	Skipped      map[string]int // reason → count
	Subagents    int
	SkippedLines int
	Took         time.Duration
}

// Backfill writes notes for every main transcript under ProjectsDir with a bounded worker pool.
func Backfill(opt BackfillOptions) (Counts, error) {
	start := time.Now()
	counts := Counts{Skipped: map[string]int{}}
	transcripts, err := mainTranscripts(opt.ProjectsDir)
	if err != nil {
		return counts, err
	}
	jobs := make(chan string)
	results := make(chan Result)
	var wg sync.WaitGroup
	for range max(1, opt.Workers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				res, err := Note(path, opt.Options)
				if err != nil {
					if opt.OnError != nil {
						opt.OnError(path, err)
					}
					res = Result{Status: Skipped, Reason: "unreadable"}
				}
				results <- res
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, path := range transcripts {
			if !opt.Since.IsZero() {
				if info, err := os.Stat(path); err == nil && info.ModTime().Before(opt.Since) {
					results <- Result{Status: "older"}
					continue
				}
			}
			jobs <- path
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	for res := range results {
		counts.Subagents += res.Subagents
		counts.SkippedLines += res.SkippedLines
		switch res.Status {
		case "older":
			counts.Older++
			continue
		case Written:
			counts.Written++
		case Unchanged:
			counts.Unchanged++
		case Skipped:
			counts.Skipped[res.Reason]++
		}
		counts.Transcripts++
	}
	counts.Took = time.Since(start)
	return counts, nil
}

// mainTranscripts are <projects>/<slug>/<id>.jsonl; subagent transcripts live one level deeper and fold into these.
func mainTranscripts(projects string) ([]string, error) {
	slugs, err := os.ReadDir(projects)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, slug := range slugs {
		if !slug.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(projects, slug.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".jsonl") {
				out = append(out, filepath.Join(projects, slug.Name(), f.Name()))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
