// Package evals builds evaluation sets from the owner's own history and measures the brain against them (DESIGN §9):
// navigation, repeat-mistake, answer-from-brain, and an offline A/B against a keyword-grep baseline. Sets and reports
// live under <brain>/.brain/evals and are never committed. No prompt text is stored: an item names its session note
// and the prompt is re-read when the eval runs.
package evals

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/parse"
	"saahil.local/brain/internal/session"
)

// DefaultCut splits history for the repeat-mistake set, which tests learning: only corrections after it are replayed.
// Navigation does not learn, so it uses the wider DefaultNavFrom window (coordinator, 2026-10-08).
var (
	DefaultCut     = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	DefaultNavFrom = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
)

func Dir(root string) string { return filepath.Join(root, ".brain", "evals") }

func setsDir(root string) string { return filepath.Join(Dir(root), "sets") }

type NavItem struct {
	ID         string    `json:"id"`
	Session    string    `json:"session"` // session note, brain-relative; its first owner prompt is the query
	Machine    string    `json:"machine"`
	Started    time.Time `json:"started"`
	CWD        string    `json:"cwd"`
	Transcript string    `json:"transcript,omitempty"`
	Dispatched bool      `json:"dispatched"` // the first prompt is an Orca dispatch brief, not typed by the owner
	Targets    []string  `json:"targets"`    // indexed doc/artifact paths the session read or wrote
}

type MistakeItem struct {
	ID         string    `json:"id"`
	Session    string    `json:"session"`
	Machine    string    `json:"machine"`
	Started    time.Time `json:"started"`
	CWD        string    `json:"cwd"`
	Transcript string    `json:"transcript,omitempty"`
	From       string    `json:"from"` // transcript | note: where the prompts were counted, and are re-read from
	Dispatched bool      `json:"dispatched"`
	Correction int       `json:"correction"` // index of the correcting prompt; the prompt before it is the replayed moment
	Lessons    []string  `json:"lessons"`    // lesson / anti-pattern ids learned from this session
}

type QAKind string

const (
	QAFact    QAKind = "fact"
	QAAsOf    QAKind = "as_of"   // what was true before a supersession: the superseded note
	QACurrent QAKind = "current" // the same topic asked now: the superseding note
	QAUnknown QAKind = "unknown" // not in the brain: the right answer is no pointer
)

type QAItem struct {
	ID       string   `json:"id"`
	Kind     QAKind   `json:"kind"`
	Source   string   `json:"source"` // note | memory | synthetic
	Question string   `json:"question"`
	Topic    string   `json:"topic"` // the question without its template words
	Target   string   `json:"target,omitempty"`
	Accept   []string `json:"accept,omitempty"` // other ids carrying the same answer (a memory and the note filed from it)
}

type Manifest struct {
	Built   time.Time      `json:"built"`
	NavFrom string         `json:"navigation_from"`
	Cut     string         `json:"cut"`
	Sizes   map[string]int `json:"sizes"`
	Skipped map[string]int `json:"skipped"` // reason → count
}

type Sets struct {
	Manifest   Manifest
	Navigation []NavItem
	Mistakes   []MistakeItem
	QA         []QAItem
}

// ---- session notes ----

type sessionNote struct {
	Path        string    `yaml:"-"`
	Prompts     []string  `yaml:"-"`
	Machine     string    `yaml:"machine"`
	CWD         string    `yaml:"cwd"`
	Transcript  string    `yaml:"transcript"`
	Started     time.Time `yaml:"started"`
	Touched     []string  `yaml:"touched"`
	Produced    []string  `yaml:"produced"`
	Corrections int       `yaml:"corrections"`
}

var (
	promptLine = regexp.MustCompile(`^- \d\d-\d\d \d\d:\d\dZ `)
	hiddenLine = regexp.MustCompile(`^- \(\+\d+ more not shown\)$`)
)

func readSession(root, rel string) (sessionNote, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return sessionNote{}, err
	}
	fm, body, ok := splitFrontmatter(data)
	if !ok {
		return sessionNote{}, errors.New("no frontmatter")
	}
	var s sessionNote
	if err := yaml.Unmarshal(fm, &s); err != nil {
		return s, err
	}
	s.Path = rel
	_, after, found := bytes.Cut(body, []byte("## Owner prompts\n"))
	if !found {
		return s, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(after))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			break
		}
		if loc := promptLine.FindStringIndex(line); loc != nil && !hiddenLine.MatchString(line) {
			s.Prompts = append(s.Prompts, line[loc[1]:])
		}
	}
	return s, sc.Err()
}

func splitFrontmatter(data []byte) (fm, body []byte, ok bool) {
	rest, found := bytes.CutPrefix(data, []byte("---\n"))
	if !found {
		return nil, nil, false
	}
	fm, body, ok = bytes.Cut(rest, []byte("\n---\n"))
	return fm, body, ok
}

func (s sessionNote) shortID() string {
	base := strings.TrimSuffix(path.Base(s.Path), ".md")
	if len(base) >= 19 {
		return base[:19] // YYYY-MM-DD-<session8>
	}
	return base
}

// Mirrors session.isCorrection (unexported there): the note keeps only the count, so the evals re-find which prompt
// it was.
var (
	correctionStart = regexp.MustCompile(`^(no|nope|stop|wrong|actually|instead|don't|dont|do not|why did you|that's not|thats not|not what)\b`)
	correctionWords = regexp.MustCompile(`\b(don't|do not|stop|wrong|actually|instead|why did you)\b`)
)

func isCorrection(text string) bool {
	if len(text) > 1500 {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, "’", "'")))
	if correctionStart.MatchString(t) {
		return true
	}
	if len(t) > 300 {
		t = t[:300]
	}
	return correctionWords.MatchString(t)
}

func isDispatchBrief(prompt string) bool {
	return strings.Contains(prompt, "You are working inside Orca") || strings.Contains(prompt, "from my Orca coordinator")
}

const maxOwnerPrompts = 2000

// ownerPrompts reads up to max owner prompts from a transcript as the session note's writer counts them (origin
// human on the main chain: user turns and commands queued while the agent was busy), redacted. They are used in
// memory only, never written.
func ownerPrompts(transcript string, max int) []string {
	if transcript == "" {
		return nil
	}
	f, err := os.Open(transcript)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for len(out) < max && sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"human"`)) {
			continue // a tool result or an assistant turn: not worth decoding
		}
		var e struct {
			Type        string `json:"type"`
			IsMeta      bool   `json:"isMeta"`
			IsSidechain bool   `json:"isSidechain"`
			Origin      *struct {
				Kind string `json:"kind"`
			} `json:"origin"`
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Attachment *struct {
				Type   string          `json:"type"`
				Prompt json.RawMessage `json:"prompt"`
				Origin *struct {
					Kind string `json:"kind"`
				} `json:"origin"`
			} `json:"attachment"`
		}
		if json.Unmarshal(line, &e) != nil || e.IsSidechain {
			continue
		}
		var text string
		switch {
		case e.Type == "user" && !e.IsMeta && e.Origin != nil && e.Origin.Kind == "human" && e.Message != nil:
			text = contentText(e.Message.Content)
		case e.Type == "attachment" && e.Attachment != nil && e.Attachment.Type == "queued_command" &&
			e.Attachment.Origin != nil && e.Attachment.Origin.Kind == "human":
			text = contentText(e.Attachment.Prompt) // typed while the agent was busy
		}
		if strings.TrimSpace(text) != "" {
			out = append(out, session.Redact(text))
		}
	}
	return out
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// genericFiles are read by nearly every session, so reaching one says nothing about navigation.
var genericFiles = map[string]bool{"README.md": true, "CLAUDE.md": true, "AGENTS.md": true, "GEMINI.md": true,
	"MEMORY.md": true, "BRAIN.md": true, "index.md": true, "hot.md": true}

// ---- build ----

type BuildOptions struct {
	NavFrom, Cut, Now time.Time
}

type builder struct {
	ix      *index.Index
	root    string
	skipped map[string]int
}

// Build writes the three sets from the session notes, notes and imported memories present now. It is re-runnable:
// items are ordered by id and every run replaces the previous sets.
func Build(ix *index.Index, opt BuildOptions) (Sets, error) {
	if opt.Cut.IsZero() {
		opt.Cut = DefaultCut
	}
	if opt.NavFrom.IsZero() {
		opt.NavFrom = DefaultNavFrom
	}
	b := &builder{ix: ix, root: ix.Root, skipped: map[string]int{}}
	sets := Sets{}
	var err error
	if sets.Navigation, sets.Mistakes, err = b.sessions(opt.NavFrom, opt.Cut); err != nil {
		return sets, err
	}
	if sets.QA, err = b.qa(); err != nil {
		return sets, err
	}
	sets.Manifest = Manifest{Built: opt.Now.UTC(), NavFrom: opt.NavFrom.Format(time.DateOnly), Cut: opt.Cut.Format(time.DateOnly), Skipped: b.skipped,
		Sizes: map[string]int{"navigation": len(sets.Navigation), "mistakes": len(sets.Mistakes), "qa": len(sets.QA)}}
	return sets, writeSets(b.root, sets)
}

func (b *builder) sessions(navFrom, cut time.Time) ([]NavItem, []MistakeItem, error) {
	var paths []string
	err := filepath.WalkDir(filepath.Join(b.root, "sessions"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			rel, _ := filepath.Rel(b.root, p)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	sort.Strings(paths)
	var nav []NavItem
	var mistakes []MistakeItem
	for _, rel := range paths {
		s, err := readSession(b.root, rel)
		switch {
		case err != nil:
			b.skipped["session: unreadable note"]++
			continue
		case s.Started.Before(navFrom) && s.Started.Before(cut):
			b.skipped["session: before both windows"]++
			continue
		case len(s.Prompts) == 0:
			b.skipped["session: no owner prompt"]++
			continue
		}
		dispatched := isDispatchBrief(s.Prompts[0])
		if s.Started.Before(navFrom) {
			b.skipped["navigation: before its window"]++
		} else if targets := b.targets(append(append([]string{}, s.Touched...), s.Produced...)); len(targets) > 0 {
			nav = append(nav, NavItem{ID: "nav-" + s.shortID(), Session: rel, Machine: s.Machine, Started: s.Started,
				CWD: s.CWD, Transcript: s.Transcript, Dispatched: dispatched, Targets: targets})
		} else {
			b.skipped["navigation: no indexed target"]++
		}
		prompts, from := s.Prompts, "note"
		if s.Corrections > 0 && !s.Started.Before(cut) {
			// a note fitted to its size cap drops prompts; the transcript, when on this machine, still has them all
			if all := ownerPrompts(s.Transcript, maxOwnerPrompts); len(all) > len(prompts) {
				prompts, from = all, "transcript"
			}
		}
		var lessons []string
		found := 0
		for i := 1; i < len(prompts) && !s.Started.Before(cut); i++ {
			if !isCorrection(prompts[i]) {
				continue
			}
			found++
			if lessons == nil {
				if lessons, err = b.lessonsFrom(rel); err != nil {
					return nil, nil, err
				}
			}
			mistakes = append(mistakes, MistakeItem{ID: fmt.Sprintf("mistake-%s-%d", s.shortID(), i), Session: rel,
				Machine: s.Machine, Started: s.Started, CWD: s.CWD, Transcript: s.Transcript, From: from, Dispatched: dispatched,
				Correction: i, Lessons: lessons})
		}
		if missing := s.Corrections - found; missing > 0 && !s.Started.Before(cut) {
			b.skipped["mistakes: counted by the note, prompt not on this machine"] += missing
		}
	}
	return nav, mistakes, nil
}

// targets resolves a session's files to indexed docs and artifacts (a byte copy counts as its canonical); generic
// files, folders, other sessions and files the index never saw (repo code) are dropped.
func (b *builder) targets(files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if genericFiles[path.Base(f)] || strings.HasPrefix(f, "sessions/") || strings.HasPrefix(f, "memory/") {
			continue
		}
		id, err := b.ix.Resolve(f)
		if err != nil {
			continue
		}
		var kind, typ, p, canonical string
		if b.ix.DB().QueryRow(`SELECT kind, type, path, canonical_of FROM node WHERE id = ?`, id).Scan(&kind, &typ, &p, &canonical) != nil ||
			(kind != "doc" && kind != "artifact") || typ == "session" {
			continue
		}
		if canonical != "" {
			_ = b.ix.DB().QueryRow(`SELECT path FROM node WHERE id = ?`, canonical).Scan(&p)
		}
		if !seen[p] && !genericFiles[path.Base(p)] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// lessonsFrom lists the lessons and anti-patterns the dream learned from a session (their sources name it).
func (b *builder) lessonsFrom(sessionPath string) ([]string, error) {
	rows, err := b.ix.DB().Query(`SELECT DISTINCT e.src FROM edge e JOIN node n ON n.id = e.src
		WHERE e.rel IN ('sources', 'learned_from') AND e.dst = ? AND n.type IN ('lesson', 'anti-pattern') ORDER BY e.src`, sessionPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- answer-from-brain ----

var qaNoteTypes = map[string]string{"decision": "What did we decide about %s?", "finding": "What did we find about %s?",
	"lesson": "What is the lesson about %s?", "anti-pattern": "What is the lesson about %s?"}

const memoryQuestion = "What do I know about %s?"

// unknownTopics are deliberately outside the brain's domains; any whose words the index does hold are dropped at build.
var unknownTopics = []string{
	"sourdough starter hydration", "kayak roll technique", "orchid repotting schedule", "violin bow rehair",
	"chess endgame tablebases", "beekeeping winter feeding", "espresso grinder burr alignment", "marathon taper weeks",
	"aquarium nitrogen cycle", "bonsai wiring season", "the zqvx-4471 migration", "the plorvane cache layer",
	"the quintrel billing service", "the varnok deploy freeze", "the ostrelid feature flag", "the brimwater replica lag",
}

var clauseBreak = regexp.MustCompile(`: |; | — | -- | \(|\. `)

// topicOf is the summary's first clause, at most 12 words: the question asks about the note in its own words.
func topicOf(summary string) string {
	s := strings.Trim(strings.TrimSpace(summary), "'\"")
	if loc := clauseBreak.FindStringIndex(s); loc != nil && len(strings.Fields(s[:loc[0]])) >= 3 {
		s = s[:loc[0]]
	}
	words := strings.Fields(s)
	if len(words) > 12 {
		words = words[:12]
	}
	return strings.TrimRight(strings.Join(words, " "), ".,;:")
}

type noteInfo struct {
	id, typ, status, summary, asOf string
	supersedes, sources            []string
}

func (b *builder) qa() ([]QAItem, error) {
	notes, err := readNotes(filepath.Join(b.root, "notes"))
	if err != nil {
		return nil, err
	}
	byID := map[string]noteInfo{}
	filedFrom := map[string][]string{} // memory key (<project>/<file>) → notes filed from it
	for _, n := range notes {
		byID[n.id] = n
		for _, src := range n.sources {
			if k, ok := memoryKey(src); ok {
				filedFrom[k] = append(filedFrom[k], n.id)
			}
		}
	}
	var items []QAItem
	add := func(it QAItem) {
		if _, err := b.ix.Resolve(it.Target); err != nil {
			b.skipped["qa: target not in the index yet"]++
			return
		}
		items = append(items, it)
	}
	for _, n := range notes {
		tmpl, ok := qaNoteTypes[n.typ]
		if ok && n.status == "current" && n.summary != "" {
			var accept []string
			for _, src := range n.sources {
				if k, ok := memoryKey(src); ok {
					accept = append(accept, "memory-import/"+k)
				}
			}
			topic := topicOf(n.summary)
			add(QAItem{ID: "qa-note-" + n.id, Kind: QAFact, Source: "note", Question: fmt.Sprintf(tmpl, topic), Topic: topic,
				Target: n.id, Accept: accept})
		}
		for _, old := range n.supersedes {
			o, ok := byID[old]
			if !ok || o.summary == "" {
				b.skipped["qa: superseded note missing"]++
				continue
			}
			topic := topicOf(o.summary)
			add(QAItem{ID: "qa-asof-" + old, Kind: QAAsOf, Source: "note", Target: old, Topic: topic,
				Question: fmt.Sprintf("As of %s, what was true about %s?", cmp.Or(o.asOf, "then"), topic)})
			add(QAItem{ID: "qa-current-" + old, Kind: QACurrent, Source: "note", Target: n.id, Topic: topic,
				Question: fmt.Sprintf("What is true now about %s?", topic)})
		}
	}
	memories, err := b.memories()
	if err != nil {
		return nil, err
	}
	for _, m := range memories {
		k := strings.TrimPrefix(m.id, "memory-import/")
		topic := topicOf(m.summary)
		add(QAItem{ID: "qa-memory-" + k, Kind: QAFact, Source: "memory", Question: fmt.Sprintf(memoryQuestion, topic),
			Topic: topic, Target: m.id, Accept: filedFrom[k]})
	}
	for i, topic := range unknownTopics {
		if held, err := b.holds(topic); err != nil {
			return nil, err
		} else if held {
			b.skipped["qa: unknown topic found in the brain"]++
			continue
		}
		items = append(items, QAItem{ID: fmt.Sprintf("qa-unknown-%02d", i), Kind: QAUnknown, Source: "synthetic",
			Question: fmt.Sprintf("What did we decide about %s?", topic), Topic: topic})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func readNotes(dir string) ([]noteInfo, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []noteInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		d := parse.Markdown(data)
		n := noteInfo{id: strings.TrimSuffix(e.Name(), ".md"), typ: d.Type, status: d.Status, summary: d.Summary, asOf: d.AsOf}
		for _, r := range d.Authored {
			switch r.Rel {
			case "supersedes":
				n.supersedes = append(n.supersedes, r.Dst)
			case "sources", "evidence":
				n.sources = append(n.sources, r.Dst)
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// memoryKey maps a filed note's source in the import shadow (.import/<machine>/<project>/<file>) to the key the index
// presents the live memory under (memory-import/<project>/<file>).
func memoryKey(src string) (string, bool) {
	rest, ok := strings.CutPrefix(src, ".import/")
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || !strings.HasSuffix(parts[2], ".md") || parts[2] == "MEMORY.md" {
		return "", false
	}
	return parts[1] + "/" + parts[2], true
}

type memoryNode struct{ id, summary string }

func (b *builder) memories() ([]memoryNode, error) {
	rows, err := b.ix.DB().Query(`SELECT id, summary FROM node WHERE path LIKE 'memory-import/%' AND kind = 'doc'
		AND canonical_of = '' AND summary != '' AND type IN ('feedback', 'project', 'reference', 'user', 'memory')
		AND path NOT LIKE '%/MEMORY.md' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memoryNode
	for rows.Next() {
		var m memoryNode
		if err := rows.Scan(&m.id, &m.summary); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// holds reports whether the index has a document with every word of topic (so it is not an unknown).
func (b *builder) holds(topic string) (bool, error) {
	var terms []string
	for _, w := range words(topic) {
		if w != "the" {
			terms = append(terms, `"`+w+`"`)
		}
	}
	var n int
	err := b.ix.DB().QueryRow(`SELECT count(*) FROM (SELECT 1 FROM fts WHERE fts MATCH ? LIMIT 1)`, strings.Join(terms, " ")).Scan(&n)
	return n > 0, err
}

// ---- files ----

func writeSets(root string, s Sets) error {
	dir := setsDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{"navigation.jsonl", jsonLines(s.Navigation)}, {"mistakes.jsonl", jsonLines(s.Mistakes)}, {"qa.jsonl", jsonLines(s.QA)}} {
		if err := writeAtomic(filepath.Join(dir, f.name), f.data); err != nil {
			return err
		}
	}
	m, err := json.MarshalIndent(s.Manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "manifest.json"), append(m, '\n'))
}

func jsonLines[T any](items []T) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, it := range items {
		_ = enc.Encode(it) // plain structs of strings, ints and times: cannot fail
	}
	return buf.Bytes()
}

func writeAtomic(p string, data []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

var ErrNoSets = errors.New("no eval sets: run `brain eval build` first")

func LoadSets(root string) (Sets, error) {
	dir := setsDir(root)
	var s Sets
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return s, ErrNoSets
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s.Manifest); err != nil {
		return s, err
	}
	if err := readLines(filepath.Join(dir, "navigation.jsonl"), &s.Navigation); err != nil {
		return s, err
	}
	if err := readLines(filepath.Join(dir, "mistakes.jsonl"), &s.Mistakes); err != nil {
		return s, err
	}
	return s, readLines(filepath.Join(dir, "qa.jsonl"), &s.QA)
}

func readLines[T any](p string, out *[]T) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var it T
		if err := json.Unmarshal(line, &it); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		*out = append(*out, it)
	}
	return nil
}
