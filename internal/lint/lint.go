// Package lint checks the brain's files against FORMAT.md: per-type schema, caps, hubs, evidence and link
// resolution, the broader and supersedes graphs, secrets, and the brain-ready header on work docs.
package lint

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"saahil.local/brain/internal/session"
	"sort"
	"strings"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/parse"
)

type Severity string

const (
	Error Severity = "error"
	Warn  Severity = "warn"
)

type Issue struct {
	Path string   `json:"path"`
	Rule string   `json:"rule"`
	Sev  Severity `json:"severity"`
	Msg  string   `json:"msg"`
}

type Report struct {
	Files    int     `json:"files"`
	Errors   int     `json:"errors"`
	Warnings int     `json:"warnings"`
	Dangling int     `json:"dangling"` // links and authored refs that resolve to nothing
	Issues   []Issue `json:"issues"`
}

// ExitCode is 0 clean, 1 warnings only, 2 any error.
func (r Report) ExitCode() int {
	switch {
	case r.Errors > 0:
		return 2
	case r.Warnings > 0:
		return 1
	}
	return 0
}

func (r *Report) add(issues ...Issue) {
	for _, is := range issues {
		if is.Sev == Error {
			r.Errors++
		} else {
			r.Warnings++
		}
		if is.Rule == "dangling" || is.Rule == "links" {
			r.Dangling++
		}
		r.Issues = append(r.Issues, is)
	}
}

func (r *Report) Merge(o Report) {
	r.Files += o.Files
	r.Errors += o.Errors
	r.Warnings += o.Warnings
	r.Dangling += o.Dangling
	r.Issues = append(r.Issues, o.Issues...)
}

// Linter checks files of the brain at Root. WorkRoot is where work/ paths resolve before the cut-over fills
// <root>/work (the read-only saahil-docs shadow).
type Linter struct {
	Root, WorkRoot string
}

func New(root string) *Linter {
	return &Linter{Root: root, WorkRoot: config.ShadowDocRoot()}
}

func Root(root string) (Report, error) { return New(root).All() }

const maxRead = 2_500_000

// skipped folders hold the index, history copies or bulk data; lint reads what agents and the dream write.
var skipDirs = map[string]bool{".git": true, ".index": true, ".archive": true, ".import": true, ".data": true,
	".brain": true, "node_modules": true}

var textExt = map[string]bool{".md": true, ".txt": true, ".json": true, ".jsonl": true, ".yaml": true, ".yml": true, ".toml": true}

// All lints every text file under the brain, then the rules that span files.
func (l *Linter) All() (Report, error) {
	var r Report
	g := graphFacts{}
	err := filepath.WalkDir(l.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a vanished or unreadable entry is not the brain's fault
		}
		if d.IsDir() {
			if p != l.Root && (skipDirs[d.Name()] || config.IsSyncBulkDir(d.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !textExt[filepath.Ext(p)] {
			return nil
		}
		rel, _ := filepath.Rel(l.Root, p)
		rel = filepath.ToSlash(rel)
		data, err := readCapped(p)
		if err != nil {
			return nil
		}
		r.Files++
		r.add(l.Check(rel, data)...)
		g.collect(rel, data)
		return nil
	})
	if err != nil {
		return r, err
	}
	r.add(g.check()...)
	sort.SliceStable(r.Issues, func(i, j int) bool {
		a, b := r.Issues[i], r.Issues[j]
		return a.Sev < b.Sev || (a.Sev == b.Sev && a.Path < b.Path)
	})
	return r, nil
}

// File lints one file of this brain by its path relative to Root.
func (l *Linter) File(rel string) (Report, error) {
	data, err := readCapped(filepath.Join(l.Root, filepath.FromSlash(rel)))
	if err != nil {
		return Report{}, err
	}
	var r Report
	r.Files = 1
	r.add(l.Check(rel, data)...)
	return r, nil
}

func readCapped(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxRead))
}

// Kind names the rule set a brain path falls under; "" means secrets only.
func Kind(rel string) string {
	base := path.Base(rel)
	switch {
	case !strings.HasSuffix(rel, ".md"):
		return ""
	case strings.HasPrefix(rel, "notes/") && strings.Count(rel, "/") == 1:
		return "note"
	case strings.HasPrefix(rel, "hubs/") && strings.Count(rel, "/") == 1:
		return "hub"
	case rel == "memory/MEMORY.md":
		return "memory-index"
	case rel == "hot.md":
		return "hot"
	case !strings.HasPrefix(rel, "work/") || strings.Contains(rel, "/log/") || strings.Contains(rel, "/."):
		return ""
	case base == "STATE.md":
		return "state"
	case base == "CARD.md":
		return "card"
	case isBrainReadyDoc(rel):
		return "doc"
	}
	return ""
}

var docDirs = map[string]bool{"plans": true, "specs": true, "reports": true, "reviews": true, "experiments": true, "handoffs": true}

// isBrainReadyDoc: the docs §4.8 asks to carry the header — specs, plans, reports, reviews, experiments, handoffs.
func isBrainReadyDoc(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, dir := range parts[1 : len(parts)-1] {
		if docDirs[dir] {
			return true
		}
	}
	base := strings.ToUpper(parts[len(parts)-1])
	for _, k := range []string{"SPEC", "PLAN", "REPORT", "REVIEW", "HANDOFF", "DESIGN", "EXPERIMENT"} {
		if strings.HasPrefix(base, k) {
			return true
		}
	}
	return false
}

// Check returns every issue of one file, given its path relative to the brain root and its full content.
func (l *Linter) Check(rel string, data []byte) []Issue {
	var out []Issue
	add := func(sev Severity, rule, format string, args ...any) {
		out = append(out, Issue{Path: rel, Rule: rule, Sev: sev, Msg: fmt.Sprintf(format, args...)})
	}
	if kinds := Secrets(data); len(kinds) > 0 {
		add(Error, "secret", "looks like a secret (%s): remove it, the brain syncs to both machines", strings.Join(kinds, ", "))
	}
	if n := MaybeSecrets(data); n > 0 {
		add(Warn, "maybe-secret", "%d random-looking token(s): a credential must not be here (a document id is fine)", n)
	}
	kind := Kind(rel)
	switch kind {
	case "note", "hub":
		l.checkNote(rel, data, add) // delta.Check reports their caps as schema errors
	case "memory-index", "hot", "state", "card":
		if msg := Cap(rel, data); msg != "" {
			add(Error, "cap", "%s", msg)
		}
	}
	switch kind {
	case "card":
		l.checkCard(data, add)
	case "doc":
		checkDoc(data, add)
	}
	return out
}

type addFn func(sev Severity, rule, format string, args ...any)

var liveStatus = map[string]bool{"current": true, "proposed": true}

func (l *Linter) checkNote(rel string, data []byte, add addFn) {
	n, err := delta.Parse(data)
	if err != nil {
		add(Error, "schema", "%v", err)
		return
	}
	// resolution is checked below with its own severities; Check reports the schema only
	for _, msg := range delta.Check(n, data, func(string, string) bool { return true }) {
		add(Error, "schema", "%s", msg)
	}
	if want := strings.TrimSuffix(path.Base(rel), ".md"); n.ID != want {
		add(Error, "schema", "id %q is not the file name %q", n.ID, want)
	}
	if isHubDir := strings.HasPrefix(rel, "hubs/"); isHubDir != (n.Type == "hub") {
		add(Error, "schema", "type %q does not belong in %s", n.Type, path.Dir(rel))
	}
	live := liveStatus[n.Status]
	if n.Type != "hub" && live {
		resolving := 0
		for _, h := range n.Hubs {
			if l.hubExists(h) {
				resolving++
			} else {
				add(Warn, "dangling", "hub %q does not resolve", h)
			}
		}
		if resolving == 0 && len(n.Hubs) > 0 {
			add(Error, "hubs", "none of its hubs %v resolves: a live note sits in ≥ 1 hub", n.Hubs)
		}
	}
	for _, b := range n.Broader {
		if !l.hubExists(b) {
			add(Error, "broader", "broader hub %q does not resolve", b)
		}
	}
	needsEvidence := live && (n.Type == "finding" || n.Type == "decision" || n.Type == "lesson")
	for _, ev := range n.Evidence {
		if l.pathExists(ev) {
			continue
		}
		if needsEvidence {
			add(Error, "evidence", "evidence %q does not resolve", ev)
		} else {
			add(Warn, "dangling", "evidence %q does not resolve", ev)
		}
	}
	for _, src := range n.Sources {
		if !l.pathExists(src) {
			add(Warn, "dangling", "source %q does not resolve", src)
		}
	}
	refs := append(append([]string{}, n.About...), n.Supersedes...)
	for _, r := range n.Related {
		refs = append(refs, r.ID)
	}
	for _, id := range refs {
		if !l.idExists(id) {
			add(Warn, "dangling", "%q does not resolve", id)
		}
	}
	var dangling []string
	for _, lk := range parse.Markdown(data).Links {
		if !l.linkResolves(rel, lk) {
			dangling = append(dangling, lk.Target)
		}
	}
	if len(dangling) > 0 {
		add(Warn, "links", "%d dangling link(s): %s", len(dangling), strings.Join(dangling, ", "))
	}
}

var (
	docTypes    = map[string]bool{"spec": true, "plan": true, "report": true, "review": true, "experiment": true, "handoff": true, "readme": true}
	docStatuses = map[string]bool{"live": true, "draft": true, "superseded": true, "done": true}
	cardStatus  = map[string]bool{"live": true, "paused": true, "draft": true, "superseded": true, "done": true, "current": true, "proposed": true, "retired": true}
)

// Cap reports how a baseline or note breaks its size cap (DESIGN §4.6), or "" when it fits or has none.
func Cap(rel string, data []byte) string {
	switch Kind(rel) {
	case "memory-index":
		if n := lineCount(data); n > config.MemoryIndexMaxLines {
			return fmt.Sprintf("MEMORY.md is %d lines (> %d)", n, config.MemoryIndexMaxLines)
		}
		if len(data) > config.MemoryIndexMaxBytes {
			return fmt.Sprintf("MEMORY.md is %d bytes (> %d)", len(data), config.MemoryIndexMaxBytes)
		}
	case "hot":
		if n := len(strings.Fields(parse.Markdown(data).Body)); n > config.HotMaxWords {
			return fmt.Sprintf("hot.md is %d words (> %d)", n, config.HotMaxWords)
		}
	case "state":
		if len(data) > config.StateMaxBytes {
			return fmt.Sprintf("STATE.md is %d bytes (> %d): overwrite it with where things stand, move history to log/", len(data), config.StateMaxBytes)
		}
	case "card":
		if len(data) > config.CardMaxBytes {
			return fmt.Sprintf("CARD.md is %d bytes (> %d)", len(data), config.CardMaxBytes)
		}
	case "hub":
		if n := lineCount(data); n > config.HubMaxLines {
			return fmt.Sprintf("hub is %d lines (> %d)", n, config.HubMaxLines)
		}
	case "note":
		if len(data) > config.NoteMaxBytes {
			return fmt.Sprintf("note is %d bytes (> %d)", len(data), config.NoteMaxBytes)
		}
	}
	return ""
}

func (l *Linter) checkCard(data []byte, add addFn) {
	fm := headerKeys(data)
	if fm == nil {
		add(Error, "schema", "CARD.md has no frontmatter")
		return
	}
	d := parse.Markdown(data)
	if d.Type != "card" {
		add(Error, "schema", "CARD.md type is %q, not card", d.Type)
	}
	if !cardStatus[d.Status] {
		add(Error, "schema", "CARD.md status %q is not a card status", d.Status)
	}
	if s := len([]rune(fm["summary"])); !fmHas(fm, "summary") || s == 0 || s > config.SummaryMaxChars {
		add(Error, "schema", "CARD.md summary must be 1–%d chars", config.SummaryMaxChars)
	}
	if n := len(d.Hubs); n == 0 || n > 3 {
		add(Error, "schema", "CARD.md sits in 1–3 hubs, has %d", n)
	}
	for _, h := range d.Hubs {
		if !l.hubExists(h) {
			add(Warn, "dangling", "hub %q does not resolve", h)
		}
	}
}

func checkDoc(data []byte, add addFn) {
	fm := headerKeys(data)
	var missing []string
	for _, k := range []string{"type", "status", "summary", "hubs"} {
		if !fmHas(fm, k) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		add(Warn, "doc-header", "brain-ready header lacks %s (type, status, summary, hubs — see brain-write)", strings.Join(missing, ", "))
	} else {
		d := parse.Markdown(data)
		if !docTypes[d.Type] || !docStatuses[d.Status] {
			add(Warn, "doc-header", "type %q / status %q: want spec|plan|report|review|experiment|handoff|readme and live|draft|superseded|done", d.Type, d.Status)
		}
	}
	for _, s := range parse.Markdown(data).Sections() {
		if s.Bytes > config.SectionWarnBytes {
			add(Warn, "doc-section", "section %q is %d KB (> %d KB): split it so agents can read one part", s.Heading, s.Bytes/1024, config.SectionWarnBytes/1024)
		}
	}
}

// headerKeys returns the top-level frontmatter keys with their raw values, or nil without frontmatter.
func headerKeys(data []byte) map[string]string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil
	}
	fm, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return nil
	}
	keys := map[string]string{}
	for _, line := range strings.Split(fm, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && k != "" && !strings.HasPrefix(k, " ") && !strings.HasPrefix(k, "#") {
			keys[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return keys
}

func fmHas(fm map[string]string, k string) bool {
	v, ok := fm[k]
	return ok && v != "" && v != "[]"
}

func lineCount(data []byte) int {
	n := bytes.Count(data, []byte("\n"))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// ---- resolution ----

// Resolves reports whether an authored ref (an id, or a path relative to the brain) or a link target exists.
func (l *Linter) Resolves(ref string) bool {
	if strings.Contains(ref, "/") {
		return l.pathExists(ref)
	}
	return l.idExists(ref)
}

func (l *Linter) hubExists(id string) bool {
	return validRef(id) && exists(filepath.Join(l.Root, "hubs", id+".md"))
}

func (l *Linter) idExists(id string) bool {
	if strings.Contains(id, "/") {
		return l.pathExists(id)
	}
	return validRef(id) && (exists(filepath.Join(l.Root, "notes", id+".md")) || exists(filepath.Join(l.Root, "hubs", id+".md")))
}

func validRef(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && id != "." && id != ".."
}

// pathExists resolves path[#heading] against the brain root, then work/ against the shadow.
func (l *Linter) pathExists(ref string) bool {
	p, _, _ := strings.Cut(ref, "#")
	p = path.Clean(strings.TrimSpace(p))
	if p == "." || path.IsAbs(p) || strings.HasPrefix(p, "..") {
		return false
	}
	if exists(filepath.Join(l.Root, filepath.FromSlash(p))) {
		return true
	}
	if rest, ok := strings.CutPrefix(p, "work/"); ok && l.WorkRoot != "" {
		return exists(filepath.Join(l.WorkRoot, filepath.FromSlash(rest)))
	}
	return false
}

func (l *Linter) linkResolves(rel string, lk parse.Link) bool {
	t := lk.Target
	switch {
	case lk.Wiki && !strings.Contains(t, "/"):
		return l.idExists(strings.TrimSuffix(t, ".md")) || exists(filepath.Join(l.Root, filepath.Dir(rel), t+".md"))
	case strings.HasPrefix(t, "/"):
		return exists(t) || exists(config.Canonical(t)) || exists(strings.Replace(t, config.LaptopHome(), config.DevboxHome(), 1))
	case lk.Wiki:
		return l.pathExists(t)
	}
	return l.pathExists(path.Join(path.Dir(rel), t))
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// ---- secrets ----

var secretPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{"aws key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`)},
	{"api key", regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`)},
	{"password", regexp.MustCompile(`(?i)\b(?:password|passwd|pwd)\s*[=:]\s*["']?[^\s"'<>{}()$*,;]{6,}`)},
}

// placeholders a password= pair may legitimately carry
var notASecret = regexp.MustCompile(`(?i)(redacted|example|placeholder|changeme|xxxxxx|your[_-]?password|\.\.\.)`)

// Secrets names the kinds of secret the text appears to hold; never the secret itself.
func Secrets(data []byte) []string {
	var kinds []string
	for _, p := range secretPatterns {
		for _, m := range p.re.FindAll(data, 8) {
			if p.kind == "password" && notASecret.Match(m) {
				continue
			}
			kinds = append(kinds, p.kind)
			break
		}
	}
	return kinds
}

// MaybeSecrets counts random-looking bare tokens (credentials, but also random document ids): a warning, never an
// error, so a Drive id in a note cannot fail a dream's check.
func MaybeSecrets(data []byte) int {
	n := 0
	for _, m := range bareToken.FindAll(data, -1) {
		if session.IsLikelySecret(string(m)) {
			n++
		}
	}
	return n
}

var bareToken = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)
