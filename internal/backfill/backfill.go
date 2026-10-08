// Package backfill is the deterministic half of the one-time backfill (DESIGN §4.7, PLAN D2): which work boxes exist,
// what each one holds, where its CARD goes, and whether a CARD a model wrote may be kept. The model calls and the
// Temporal workflow live in internal/dream.
package backfill

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/parse"
)

const (
	StaleAfter     = 14 * 24 * time.Hour
	maxWalkEntries = 20000 // per box: a run-output tree is summarised, never listed
	maxTreeLines   = 200
	maxDocs        = 40
	maxSessions    = 30
	headBytes      = 2048
	maxKeyFiles    = 7
)

type Box struct {
	Name   string    `json:"name"`
	Abs    string    `json:"abs"`
	Newest time.Time `json:"newest"`
	Files  int       `json:"files"`
}

// Live is true once the cut-over has moved saahil-docs into <brain>/work.
func Live(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".brain", "live"))
	return err == nil
}

// CardPath is where a box's CARD is written: staged under .brain/ in shadow, in the box once live.
func CardPath(root, box string, live bool) string {
	if live {
		return filepath.Join(root, "work", box, "CARD.md")
	}
	return filepath.Join(root, ".brain", "staging", "work", box, "CARD.md")
}

// WorkRoot is the folder whose top-level directories are the boxes.
func WorkRoot(root, shadow string, live bool) string {
	if live {
		return filepath.Join(root, "work")
	}
	return shadow
}

func Enumerate(workRoot string, only []string) ([]Box, error) {
	list, err := os.ReadDir(workRoot)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, b := range only {
		want[b] = true
	}
	var out []Box
	for _, de := range list {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") || (len(want) > 0 && !want[de.Name()]) {
			continue
		}
		b := Box{Name: de.Name(), Abs: filepath.Join(workRoot, de.Name())}
		walkBox(b.Abs, func(_ string, fi fs.FileInfo) {
			b.Files++
			if fi.ModTime().After(b.Newest) {
				b.Newest = fi.ModTime().UTC()
			}
		})
		out = append(out, b)
	}
	return out, nil
}

func isBulk(name string) bool {
	return name == "artifacts" || name == "node_modules" || name == "venv" || name == "__pycache__" ||
		strings.HasPrefix(name, "cache") || strings.HasPrefix(name, ".")
}

// walkBox visits regular files below a box, never following links; bulk folders count as one entry (their own mtime).
func walkBox(abs string, visit func(rel string, fi fs.FileInfo)) {
	n := 0
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || n >= maxWalkEntries {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(abs, p)
		if d.IsDir() && p != abs && isBulk(d.Name()) {
			if fi, err := d.Info(); err == nil {
				visit(rel+"/", fi)
			}
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && !strings.HasPrefix(d.Name(), ".") {
			if fi, err := d.Info(); err == nil {
				n++
				visit(rel, fi)
			}
		}
		return nil
	})
}

// ---- the card skill's input ----

type Doc struct {
	Path     string   `json:"path"`
	Title    string   `json:"title,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Status   string   `json:"status,omitempty"`
	Headings []string `json:"headings,omitempty"`
}

type Inventory struct {
	Box      string            `json:"box"`
	Newest   string            `json:"newest"`
	Tree     []string          `json:"tree"`
	Docs     []Doc             `json:"docs"`
	Heads    map[string]string `json:"heads,omitempty"`
	Sessions []string          `json:"sessions,omitempty"`
	PRs      []string          `json:"prs,omitempty"`
}

var prRE = regexp.MustCompile(`[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#\d+`)

// BuildInventory reads one box: its tree (bulk folders summarised), its docs' titles and headings, the heads of
// STATE/README/GOAL, and the one-line account of every session note that worked in it. Same files → same bytes.
func BuildInventory(root string, b Box) Inventory {
	inv := Inventory{Box: b.Name, Newest: b.Newest.Format("2006-01-02"), Heads: map[string]string{}}
	prs := map[string]bool{}
	type file struct {
		rel string
		fi  fs.FileInfo
	}
	var files []file
	walkBox(b.Abs, func(rel string, fi fs.FileInfo) { files = append(files, file{rel, fi}) })
	sort.SliceStable(files, func(i, j int) bool { // shallow docs first, so a long tree keeps what matters
		di, dj := strings.Count(files[i].rel, "/"), strings.Count(files[j].rel, "/")
		mi, mj := strings.HasSuffix(files[i].rel, ".md"), strings.HasSuffix(files[j].rel, ".md")
		if mi != mj {
			return mi
		}
		return di < dj
	})
	for _, f := range files {
		if len(inv.Tree) == maxTreeLines {
			inv.Tree = append(inv.Tree, fmt.Sprintf("… %d more files", len(files)-maxTreeLines))
			break
		}
		line := fmt.Sprintf("%s  %d B  %s", f.rel, f.fi.Size(), f.fi.ModTime().UTC().Format("2006-01-02"))
		if strings.HasSuffix(f.rel, "/") {
			line = f.rel + "  (bulk, not listed)  " + f.fi.ModTime().UTC().Format("2006-01-02")
		}
		inv.Tree = append(inv.Tree, line)
		if !strings.HasSuffix(f.rel, ".md") || len(inv.Docs) >= maxDocs {
			continue
		}
		data, err := readCapped(filepath.Join(b.Abs, f.rel), 256*1024)
		if err != nil {
			continue
		}
		d := parse.Markdown(data)
		doc := Doc{Path: f.rel, Title: d.Title, Summary: d.Summary, Status: d.Status}
		for _, h := range d.Headings {
			if h.Level == 2 && len(doc.Headings) < 12 {
				doc.Headings = append(doc.Headings, h.Text)
			}
		}
		inv.Docs = append(inv.Docs, doc)
		for _, m := range d.Mentions {
			if pr, ok := strings.CutPrefix(m, "pr:"); ok {
				prs[pr] = true
			}
		}
		if base := strings.ToUpper(f.rel); base == "STATE.MD" || base == "README.MD" || strings.HasPrefix(base, "GOAL") {
			inv.Heads[f.rel] = string(data[:min(len(data), headBytes)])
		}
	}
	for _, s := range BoxSessions(root, b.Name) {
		inv.Sessions = append(inv.Sessions, s.Line())
		for _, pr := range s.PRs {
			prs[pr] = true
		}
	}
	for pr := range prs {
		inv.PRs = append(inv.PRs, pr)
	}
	sort.Strings(inv.PRs)
	return inv
}

func readCapped(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	k, err := f.Read(buf)
	if err != nil && k == 0 {
		return nil, err
	}
	return buf[:k], nil
}

// Session is a session note's header: enough to say what happened in a box, never its prompts.
type Session struct {
	Rel         string   `yaml:"-"`
	Name        string   `yaml:"name"`
	SessionID   string   `yaml:"session_id"`
	Started     string   `yaml:"started"`
	Transcript  string   `yaml:"transcript"`
	Works       []string `yaml:"works"`
	Touched     []string `yaml:"touched"`
	PRs         []string `yaml:"prs"`
	Corrections int      `yaml:"corrections"`
}

func (s Session) Line() string {
	line := fmt.Sprintf("%s %s", strings.SplitN(s.Started, "T", 2)[0], s.Name)
	if len(s.PRs) > 0 {
		line += " (PRs " + strings.Join(s.PRs, ", ") + ")"
	}
	if s.Corrections > 0 {
		line += fmt.Sprintf(" (%d corrections)", s.Corrections)
	}
	return line + " — " + s.Rel
}

// ReadSession parses a session note's frontmatter.
func ReadSession(root, rel string) (Session, error) {
	f, err := os.Open(filepath.Join(root, rel))
	if err != nil {
		return Session{}, err
	}
	defer f.Close()
	var fm strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for i := 0; sc.Scan(); i++ {
		if sc.Text() == "---" {
			if i == 0 {
				continue
			}
			break
		}
		fm.WriteString(sc.Text() + "\n")
	}
	var s Session
	err = yaml.Unmarshal([]byte(fm.String()), &s)
	s.Rel = rel
	return s, err
}

// BoxSessions lists the session notes whose works include box, newest first.
func BoxSessions(root, box string) []Session {
	var out []Session
	paths, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "*.md"))
	sort.Sort(sort.Reverse(sort.StringSlice(paths))) // file names start with the date
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		s, err := ReadSession(root, rel)
		if err != nil {
			continue
		}
		for _, w := range s.Works {
			if w == box && len(out) < maxSessions {
				out = append(out, s)
			}
		}
	}
	return out
}

// ---- the card a model wrote ----

// Answer is what the card skill returns.
type Answer struct {
	Card struct {
		Status  string   `json:"status"`
		Summary string   `json:"summary"`
		Hubs    []string `json:"hubs"`
		Aliases []string `json:"aliases"`
	} `json:"card"`
	Body string `json:"body"`
}

const AnswerSchema = `{"type":"object","required":["card","body"],"additionalProperties":false,"properties":{
"card":{"type":"object","required":["status","summary","hubs"],"properties":{
"status":{"type":"string","enum":["live","paused","done"]},"summary":{"type":"string"},
"hubs":{"type":"array","items":{"type":"string"}},"aliases":{"type":"array","items":{"type":"string"}}}},
"body":{"type":"string"}}}`

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func CardID(box string) string {
	return "card-" + strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(box), "-"), "-")
}

var backticked = regexp.MustCompile("`([^`\\s]+)`")

// RenderCard turns an answer into CARD.md, or says why it may not be kept: over 3 KB, a hub that is not seeded, or a
// key file that does not exist in the box or is a superseded copy. id, type and as_of are set by code.
func RenderCard(raw []byte, b Box, hubs map[string]bool) (content, status string, err error) {
	var ans Answer
	if err := json.Unmarshal(raw, &ans); err != nil {
		return "", "", fmt.Errorf("answer is not the card JSON: %w", err)
	}
	c := ans.Card
	var probs []string
	if c.Status != "live" && c.Status != "paused" && c.Status != "done" {
		probs = append(probs, fmt.Sprintf("status %q is not live, paused or done", c.Status))
	}
	if n := len([]rune(c.Summary)); n == 0 || n > config.SummaryMaxChars {
		probs = append(probs, fmt.Sprintf("summary is %d chars (1–%d)", n, config.SummaryMaxChars))
	}
	if len(c.Hubs) == 0 || len(c.Hubs) > 3 {
		probs = append(probs, fmt.Sprintf("%d hubs (1–3)", len(c.Hubs)))
	}
	for _, h := range c.Hubs {
		if !hubs[h] {
			probs = append(probs, fmt.Sprintf("hub %q is not one of the input hubs", h))
		}
	}
	keys := KeyFiles(ans.Body, b.Name)
	if len(keys) > maxKeyFiles {
		probs = append(probs, fmt.Sprintf("%d key files (≤ %d)", len(keys), maxKeyFiles))
	}
	for _, k := range keys {
		data, err := readCapped(filepath.Join(b.Abs, k), 4096)
		switch {
		case err != nil || strings.HasPrefix(filepath.Clean(k), ".."):
			probs = append(probs, fmt.Sprintf("key file %s does not exist in the box", k))
		case strings.Contains("/"+k, "/.archive/") || strings.Contains("/"+k, "/log/") ||
			parse.Markdown(data).Status == "superseded":
			probs = append(probs, fmt.Sprintf("key file %s is a superseded or history copy; cite the live version", k))
		}
	}
	n := delta.Note{ID: CardID(b.Name), Type: "card", Status: c.Status, Summary: c.Summary, Scope: "work",
		Hubs: c.Hubs, Aliases: c.Aliases, AsOf: b.Newest.Format("2006-01-02"), Body: "\n" + strings.TrimSpace(ans.Body) + "\n"}
	content = string(delta.Render(n))
	if len(content) > config.CardMaxBytes {
		probs = append(probs, fmt.Sprintf("CARD is %d bytes (> %d)", len(content), config.CardMaxBytes))
	}
	if len(probs) > 0 {
		return "", "", errors.New(strings.Join(probs, "; "))
	}
	return content, c.Status, nil
}

// KeyFiles are the box-relative paths in backticks under the card's "## Key files" heading.
func KeyFiles(body, box string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.HasPrefix(strings.ToLower(line), "## key files")
			continue
		}
		if !in {
			continue
		}
		for _, m := range backticked.FindAllStringSubmatch(line, -1) {
			p, _, _ := strings.Cut(m[1], "#")
			out = append(out, strings.TrimPrefix(p, "work/"+box+"/"))
		}
	}
	return out
}

// ---- plan ----

type Planned struct {
	Box      Box    `json:"box"`
	Action   string `json:"action"` // card | campaign | skip
	Reason   string `json:"reason,omitempty"`
	Finished bool   `json:"finished"` // already known finished: a done CARD, or stale with a STATE that says so
}

var finishedRE = regexp.MustCompile(`(?i)\b(finished|landed|merged|complete[d]?|shipped)\b`)

// Plan decides per box: write a CARD unless the box's CARD is newer than every file in it; a finished campaign also
// gets its notes (repeated runs answer those from the ledger for $0).
func Plan(root, shadow string, only []string, now time.Time) (bool, []Planned, error) {
	live := Live(root)
	boxes, err := Enumerate(WorkRoot(root, shadow, live), only)
	if err != nil {
		return live, nil, err
	}
	var out []Planned
	for _, b := range boxes {
		p := Planned{Box: b, Action: "card"}
		state, _ := readCapped(filepath.Join(b.Abs, "STATE.md"), headBytes)
		p.Finished = now.Sub(b.Newest) > StaleAfter && finishedRE.Match(state)
		if fi, err := os.Stat(CardPath(root, b.Name, live)); err == nil && fi.ModTime().After(b.Newest) {
			card, _ := readCapped(CardPath(root, b.Name, live), config.CardMaxBytes)
			p.Finished = p.Finished || parse.Markdown(card).Status == "done"
			p.Action, p.Reason = "skip", "CARD is newer than every file in the box"
			if p.Finished {
				p.Action, p.Reason = "campaign", "CARD is current; finished campaign"
			}
		}
		out = append(out, p)
	}
	return live, out, nil
}

// ---- report ----

type BoxResult struct {
	Box        string  `json:"box"`
	Action     string  `json:"action"`
	Status     string  `json:"status"` // planned | ok | skipped | failed
	Reason     string  `json:"reason,omitempty"`
	Card       string  `json:"card,omitempty"`
	CardStatus string  `json:"card_status,omitempty"`
	Finished   bool    `json:"finished,omitempty"`
	Notes      int     `json:"notes,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

type Report struct {
	ID           string      `json:"id"`
	DryRun       bool        `json:"dry_run"`
	Live         bool        `json:"live"`
	BudgetUSD    float64     `json:"budget_usd"`
	Started      time.Time   `json:"started"`
	Ended        time.Time   `json:"ended"`
	Batches      int         `json:"batches"`
	Boxes        []BoxResult `json:"boxes"`
	CardsWritten int         `json:"cards_written"`
	NotesAdded   int         `json:"notes_added"`
	Finished     int         `json:"finished_campaigns"`
	CostUSD      float64     `json:"cost_usd"`
	Stopped      string      `json:"stopped,omitempty"`
}

func (r *Report) Add(b BoxResult) {
	r.Boxes = append(r.Boxes, b)
	r.CostUSD += b.CostUSD
	r.NotesAdded += b.Notes
	if b.Card != "" {
		r.CardsWritten++
	}
	if b.Finished {
		r.Finished++
	}
}

func ReportPath(root, id string) string {
	return filepath.Join(root, ".brain", "backfill-runs", strings.ReplaceAll(id, "/", "-")+".json")
}

// PromoteCards moves staged CARDs into work/<box>/ once live; a CARD already there is first kept in .archive/.
func PromoteCards(root, stamp string) (moved, skipped []string, err error) {
	if !Live(root) {
		return nil, nil, errors.New("not live: CARDs stay staged until the cut-over writes .brain/live")
	}
	staged := filepath.Join(root, ".brain", "staging", "work")
	list, err := os.ReadDir(staged)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, de := range list {
		src := filepath.Join(staged, de.Name(), "CARD.md")
		if _, err := os.Stat(src); err != nil {
			continue
		}
		box := filepath.Join(root, "work", de.Name())
		if fi, err := os.Stat(box); err != nil || !fi.IsDir() {
			skipped = append(skipped, de.Name()+": no work/"+de.Name()+"/ folder")
			continue
		}
		dst := filepath.Join(box, "CARD.md")
		if old, err := os.ReadFile(dst); err == nil {
			keep := filepath.Join(root, ".archive", "backfill", "promote-"+stamp, "work", de.Name(), "CARD.md")
			if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
				return moved, skipped, err
			}
			if err := os.WriteFile(keep, old, 0o644); err != nil {
				return moved, skipped, err
			}
		}
		if err := os.Rename(src, dst); err != nil {
			return moved, skipped, err
		}
		moved = append(moved, "work/"+de.Name()+"/CARD.md")
	}
	return moved, skipped, nil
}
