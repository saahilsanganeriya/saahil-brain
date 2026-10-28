package delta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"

	_ "modernc.org/sqlite"
)

// Delta is what every skill answers with. Verdicts (verify) and summaries (summarise) ride along for the caller.
type Delta struct {
	Ops       []Op              `json:"ops"`
	Notes     string            `json:"notes,omitempty"`
	Verdicts  []json.RawMessage `json:"verdicts,omitempty"`
	Summaries []json.RawMessage `json:"summaries,omitempty"`
}

type Op struct {
	Op          string                     `json:"op"`
	Note        *Note                      `json:"note,omitempty"` // add_note, propose_hub
	ID          string                     `json:"id,omitempty"`   // update_note, counter, delete
	Set         map[string]json.RawMessage `json:"set,omitempty"`
	AppendBody  string                     `json:"append_body,omitempty"`
	ReplaceBody *string                    `json:"replace_body,omitempty"`
	Src         string                     `json:"src,omitempty"` // link
	Rel         string                     `json:"rel,omitempty"`
	Dst         string                     `json:"dst,omitempty"`
	Old         string                     `json:"old,omitempty"` // supersede
	New         string                     `json:"new,omitempty"`
	Field       string                     `json:"field,omitempty"` // counter
	Delta       int                        `json:"delta,omitempty"`
	Members     []string                   `json:"members,omitempty"` // propose_hub
	Why         string                     `json:"why,omitempty"`
}

// Schema is the --json-schema a skill answers with.
const Schema = `{"type":"object","required":["ops"],"additionalProperties":false,"properties":{
"ops":{"type":"array","items":{"type":"object","required":["op"],"properties":{
"op":{"type":"string","enum":["add_note","update_note","link","supersede","counter","propose_hub","delete"]},
"note":{"type":"object","required":["id","type","status","summary","body"]},
"id":{"type":"string"},"set":{"type":"object"},"append_body":{"type":"string"},"replace_body":{"type":"string"},
"src":{"type":"string"},"rel":{"type":"string","enum":["hubs","about","evidence","sources","related","broader"]},
"dst":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"},
"field":{"type":"string","enum":["shown","opened","followed","helpful","harmful","owner_confirmed"]},
"delta":{"type":"integer"},"members":{"type":"array","items":{"type":"string"}},"why":{"type":"string"}}}},
"notes":{"type":"string"},"verdicts":{"type":"array"},"summaries":{"type":"array"}}}`

var counterFields = map[string]bool{"shown": true, "opened": true, "followed": true, "helpful": true, "harmful": true,
	"owner_confirmed": true}

const minHubMembers = 8 // DESIGN §4.5: a hub is proposed only for ≥ 8 live notes no hub covers

type Counter struct {
	ID    string `json:"id"`
	Field string `json:"field"`
	Delta int    `json:"delta"`
}

// Stats is where counters live (node_stats). AddCounters applies a delta's counters once per key and reports
// whether it did; internal/store can replace the SQLite default when it lands.
type Stats interface {
	AddCounters(key string, cs []Counter) (bool, error)
}

type Options struct {
	DryRun   bool      // write nothing: no files, no counters
	Now      time.Time // stamped into `updated` of changed files
	Stats    Stats     // nil: counters are validated and reported, not stored
	Key      string    // identity of this delta for counters; default sha256 of its JSON
	WorkRoot string    // where work/… evidence resolves before the cut-over (saahil-docs, read-only)
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Created bool   `json:"created"`
	// Group ties the files one op wrote together (a supersede writes two): its smallest path, empty for a file alone.
	// The dream's edit cap lands a group whole or holds it whole.
	Group string `json:"group,omitempty"`
}

type Rejection struct {
	Index  int    `json:"index"`
	Op     string `json:"op"`
	Target string `json:"target,omitempty"`
	Reason string `json:"reason"`
}

type Report struct {
	Files           []File      `json:"files"` // changed or created; written unless DryRun
	Noop            int         `json:"noop"`
	Rejected        []Rejection `json:"rejected,omitempty"`
	DeletesRejected int         `json:"deletes_rejected"`
	Counters        int         `json:"counters"` // stored this time
	PendingCounters []Counter   `json:"pending_counters,omitempty"`
	// DroppedRefs are about/supersedes/related links to notes that do not exist yet: the note is kept without them
	// and they wait in .brain/pending-links.jsonl until their target lands (ResolvePending).
	DroppedRefs []PendingLink `json:"dropped_refs,omitempty"`
	Normalized  int           `json:"normalized"` // summaries cut to 160 chars + missing evidence paths dropped
}

type entry struct {
	note Note
	orig []byte // nil for a new file
}

type applier struct {
	root       string
	opts       Options
	files      map[string]*entry // by id
	adding     map[string]bool   // ids this delta adds, so notes in one batch may cite each other
	dropped    []PendingLink
	normalized int
	touched    []string          // ids the current op wrote
	tie        map[string]string // union-find over ids written by one op
}

// Apply checks every op, applies the valid ones in order and writes each changed file atomically. It never deletes:
// a delete op is refused and counted, and supersede only marks the old note.
func Apply(ctx context.Context, root string, d Delta, opts Options) (Report, error) {
	var rep Report
	a := &applier{root: root, opts: opts, files: map[string]*entry{}, adding: map[string]bool{}, tie: map[string]string{}}
	for _, op := range d.Ops {
		if op.Op == "add_note" && op.Note != nil {
			a.adding[op.Note.ID] = true
		}
	}
	for i, op := range d.Ops {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		a.touched = a.touched[:0]
		counter, noop, err := a.apply(op)
		if err == nil {
			for _, id := range a.touched {
				a.union(a.touched[0], id)
			}
		}
		switch {
		case err != nil:
			rep.Rejected = append(rep.Rejected, Rejection{Index: i, Op: op.Op, Target: op.target(), Reason: err.Error()})
			if op.Op == "delete" {
				rep.DeletesRejected++
			}
		case counter != nil:
			rep.PendingCounters = append(rep.PendingCounters, *counter)
		case noop:
			rep.Noop++
		}
	}
	a.dropRefsToRejected()
	rep.DroppedRefs = a.dropped
	rep.Normalized = a.normalized
	ids := make([]string, 0, len(a.files))
	for id := range a.files {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := a.files[id]
		content := Render(e.note)
		if e.orig != nil && bytes.Equal(content, Render(mustParse(e.orig))) {
			continue
		}
		e.note.Updated = opts.Now.UTC().Format(time.RFC3339)
		content = Render(e.note)
		rep.Files = append(rep.Files, File{Path: e.note.Path(), Content: string(content), Created: e.orig == nil})
		if !opts.DryRun {
			if err := writeAtomic(filepath.Join(root, e.note.Path()), content); err != nil {
				return rep, err
			}
		}
	}
	a.groupFiles(rep.Files)
	if len(rep.DroppedRefs) > 0 && !opts.DryRun {
		if err := appendPending(root, rep.DroppedRefs); err != nil {
			return rep, err
		}
	}
	if len(rep.PendingCounters) > 0 && opts.Stats != nil && !opts.DryRun {
		key := opts.Key
		if key == "" {
			b, _ := json.Marshal(d)
			sum := sha256.Sum256(b)
			key = hex.EncodeToString(sum[:])
		}
		done, err := opts.Stats.AddCounters(key, rep.PendingCounters)
		if err != nil {
			return rep, err
		}
		if done {
			rep.Counters = len(rep.PendingCounters)
		}
	}
	return rep, nil
}

func mustParse(b []byte) Note {
	n, _ := Parse(b)
	return n
}

func (op Op) target() string {
	switch {
	case op.Note != nil:
		return op.Note.ID
	case op.Src != "":
		return op.Src
	case op.Old != "":
		return op.Old
	}
	return op.ID
}

// apply runs one op against the in-memory files; nothing changes unless every file it touches still passes.
func (a *applier) apply(op Op) (*Counter, bool, error) {
	switch op.Op {
	case "add_note", "propose_hub":
		if op.Note == nil {
			return nil, false, errors.New("needs a note")
		}
		n := op.Note.clone()
		n.Updated, n.Extra = "", nil
		if op.Op == "propose_hub" {
			if n.Type != "hub" || len(op.Members) < minHubMembers {
				return nil, false, fmt.Errorf("a proposed hub has type hub and ≥ %d member notes", minHubMembers)
			}
			n.Status = "proposed"
		} else if n.Type == "hub" {
			return nil, false, errors.New("hubs are seeded or proposed (propose_hub), not added")
		}
		existing, err := a.load(n.ID)
		if err != nil {
			return nil, false, err
		}
		if existing == nil {
			return nil, false, a.commit(n, nil)
		}
		merged := existing.note.clone()
		merged.merge(n)
		if !bytes.Equal(Render(merged), Render(existing.note)) || merged.Type != n.Type {
			return nil, false, fmt.Errorf("id %s already exists: use update_note", n.ID)
		}
		return nil, true, nil
	case "update_note":
		return a.edit(op.ID, func(n *Note) error {
			for field, raw := range op.Set {
				if err := setField(n, field, raw); err != nil {
					return err
				}
			}
			if op.ReplaceBody != nil {
				n.Body = *op.ReplaceBody
			}
			n.appendBody(op.AppendBody)
			return nil
		})
	case "link":
		return a.edit(op.Src, func(n *Note) error {
			if op.Rel == "related" {
				if strings.TrimSpace(op.Why) == "" {
					return errors.New("a related link needs a why")
				}
				n.addRelated(Related{ID: op.Dst, Why: op.Why})
				return nil
			}
			list := n.list(op.Rel)
			if list == nil || op.Rel == "supersedes" || op.Rel == "aliases" {
				return fmt.Errorf("rel %q is not a link (hubs about evidence sources related broader)", op.Rel)
			}
			*list = union(*list, []string{op.Dst})
			return nil
		})
	case "supersede":
		if op.Old == op.New {
			return nil, false, errors.New("a note cannot supersede itself")
		}
		before, had := a.files[op.New]
		_, newNoop, err := a.edit(op.New, func(n *Note) error {
			if n.Status == "superseded" {
				return fmt.Errorf("%s is itself superseded", op.New)
			}
			n.Supersedes = union(n.Supersedes, []string{op.Old})
			return nil
		})
		if err != nil {
			return nil, false, err
		}
		_, oldNoop, err := a.edit(op.Old, func(n *Note) error {
			if contains(n.Supersedes, op.New) {
				return errors.New("supersede cycle")
			}
			n.Status = "superseded"
			return nil
		})
		if err != nil { // both files or neither
			if had {
				a.files[op.New] = before
			} else {
				delete(a.files, op.New)
			}
		}
		return nil, newNoop && oldNoop, err
	case "counter":
		if !counterFields[op.Field] || op.Delta < 1 || op.Delta > 100 {
			return nil, false, fmt.Errorf("counter %q %+d: fields %v, delta 1–100", op.Field, op.Delta, keys(counterFields))
		}
		if !a.resolve("id", op.ID) {
			return nil, false, fmt.Errorf("%s does not resolve", op.ID)
		}
		return &Counter{ID: op.ID, Field: op.Field, Delta: op.Delta}, false, nil
	case "delete":
		return nil, false, errors.New("refused: the brain never deletes; supersede or retire instead")
	}
	return nil, false, fmt.Errorf("unknown op %q", op.Op)
}

func (a *applier) edit(id string, change func(*Note) error) (*Counter, bool, error) {
	e, err := a.load(id)
	if err != nil {
		return nil, false, err
	}
	if e == nil {
		return nil, false, fmt.Errorf("%s does not exist", id)
	}
	n := e.note.clone()
	if err := change(&n); err != nil {
		return nil, false, err
	}
	a.repoint(&n)
	if bytes.Equal(Render(n), Render(e.note)) {
		return nil, true, nil
	}
	return nil, false, a.commit(n, e.orig)
}

func (a *applier) commit(n Note, orig []byte) error {
	// work/ stays empty until the cut-over renames saahil-docs into it; a delta only ever writes these two folders
	if p := n.Path(); filepath.Clean(p) != p || strings.Count(p, "/") != 1 {
		return fmt.Errorf("refusing path %q: a delta writes only notes/<id>.md and hubs/<id>.md", p)
	}
	if orig == nil {
		// a new note may cite notes that land later; an edit (link, update) must name an existing target
		a.dropUnresolved(&n)
		a.repoint(&n)
		a.normalize(&n)
	}
	resolve := func(kind, ref string) bool { return (kind == "id" && a.adding[ref]) || a.resolve(kind, ref) }
	if probs := Check(n, Render(n), resolve); len(probs) > 0 {
		return errors.New(strings.Join(probs, "; "))
	}
	a.files[n.ID] = &entry{note: n, orig: orig}
	a.touched = append(a.touched, n.ID)
	return nil
}

func (a *applier) find(id string) string {
	for a.tie[id] != "" && a.tie[id] != id {
		id = a.tie[id]
	}
	return id
}

func (a *applier) union(x, y string) {
	if rx, ry := a.find(x), a.find(y); rx != ry {
		a.tie[ry] = rx
	}
}

// groupFiles names each set of two or more written files tied by one op after its smallest path.
func (a *applier) groupFiles(files []File) {
	smallest, size := map[string]string{}, map[string]int{}
	root := make([]string, len(files))
	for i, f := range files {
		root[i] = a.find(strings.TrimSuffix(filepath.Base(f.Path), ".md"))
		if s, ok := smallest[root[i]]; !ok || f.Path < s {
			smallest[root[i]] = f.Path
		}
		size[root[i]]++
	}
	for i := range files {
		if size[root[i]] > 1 {
			files[i].Group = smallest[root[i]]
		}
	}
}

// normalize enforces two mechanical caps on a new note instead of rejecting it: the summary is cut to 160 chars at a
// word boundary (the body keeps the whole fact), and evidence/source paths that do not exist are dropped (a note that
// needs evidence and is left with none is still rejected by Check).
func (a *applier) normalize(n *Note) {
	if r := []rune(n.Summary); len(r) > config.SummaryMaxChars {
		cut := string(r[:config.SummaryMaxChars-1])
		if i := strings.LastIndex(cut, " "); i > config.SummaryMaxChars/2 {
			cut = cut[:i]
		}
		n.Summary = strings.TrimRight(cut, " ,;:—-") + "…"
		a.normalized++
	}
	keep := func(paths []string) []string {
		var out []string
		for _, p := range paths {
			if a.resolve("path", p) {
				out = append(out, p)
			} else {
				a.normalized++
			}
		}
		return out
	}
	n.Evidence, n.Sources = keep(n.Evidence), keep(n.Sources)
}

// sessionNoteRE is a session note's path (FORMAT.md); its <date>-<session8> part names the session, its slug does not.
var sessionNoteRE = regexp.MustCompile(`^(sessions/[^/]+/\d{4}/\d{2})/(\d{4}-\d{2}-\d{2}-[0-9a-f]{8}-)[^/]+\.md$`)

// repoint moves an evidence or source path that names a session note by a slug it no longer has onto that session's
// one note, keeping any #part: session notes written before they kept their first path moved on /rename.
func (a *applier) repoint(n *Note) {
	fix := func(paths []string) []string {
		var out []string
		for _, p := range paths {
			file, part, hasPart := strings.Cut(p, "#")
			if m := sessionNoteRE.FindStringSubmatch(file); m != nil && !a.resolve("path", p) {
				entries, _ := os.ReadDir(filepath.Join(a.root, m[1]))
				var same []string
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), m[2]) && strings.HasSuffix(e.Name(), ".md") {
						same = append(same, m[1]+"/"+e.Name())
					}
				}
				if len(same) == 1 {
					p = same[0]
					if hasPart {
						p += "#" + part
					}
					a.normalized++
				}
			}
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
		return out
	}
	n.Evidence, n.Sources = fix(n.Evidence), fix(n.Sources)
}

// dropUnresolved removes links to notes that neither exist nor are added by this delta; they are queued, not lost.
func (a *applier) dropUnresolved(n *Note) {
	known := func(ref string) bool { return ref != n.ID && (a.adding[ref] || a.resolve("id", ref)) }
	keep := func(field string, refs []string) []string {
		var out []string
		for _, ref := range refs {
			if known(ref) {
				out = append(out, ref)
			} else {
				a.dropped = append(a.dropped, PendingLink{Note: n.ID, Field: field, Ref: ref})
			}
		}
		return out
	}
	n.About = keep("about", n.About)
	n.Supersedes = keep("supersedes", n.Supersedes)
	var related []Related
	for _, r := range n.Related {
		if known(r.ID) {
			related = append(related, r)
		} else {
			a.dropped = append(a.dropped, PendingLink{Note: n.ID, Field: "related", Ref: r.ID, Why: r.Why})
		}
	}
	n.Related = related
}

// dropRefsToRejected removes links to notes this delta meant to add but rejected, after every op has run.
func (a *applier) dropRefsToRejected() {
	for id, e := range a.files {
		exists := func(ref string) bool {
			if _, ok := a.files[ref]; ok {
				return true
			}
			_, err := os.Stat(filepath.Join(a.root, "notes", ref+".md"))
			return err == nil
		}
		n := e.note
		changed := false
		filter := func(field string, refs []string) []string {
			var out []string
			for _, ref := range refs {
				if exists(ref) {
					out = append(out, ref)
				} else {
					changed = true
					a.dropped = append(a.dropped, PendingLink{Note: id, Field: field, Ref: ref})
				}
			}
			return out
		}
		n.About = filter("about", n.About)
		n.Supersedes = filter("supersedes", n.Supersedes)
		var related []Related
		for _, r := range n.Related {
			if exists(r.ID) {
				related = append(related, r)
			} else {
				changed = true
				a.dropped = append(a.dropped, PendingLink{Note: id, Field: "related", Ref: r.ID, Why: r.Why})
			}
		}
		n.Related = related
		if changed {
			e.note = n
		}
	}
}

// load finds an id in this delta's pending files, then under notes/ or hubs/.
func (a *applier) load(id string) (*entry, error) {
	if e, ok := a.files[id]; ok {
		return e, nil
	}
	if !slugRE.MatchString(id) {
		return nil, fmt.Errorf("id %q is not a slug", id)
	}
	for _, dir := range []string{"notes", "hubs"} {
		b, err := os.ReadFile(filepath.Join(a.root, dir, id+".md"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		n, err := Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s/%s.md: %w", dir, id, err)
		}
		return &entry{note: n, orig: b}, nil
	}
	return nil, nil
}

func (a *applier) resolve(kind, ref string) bool {
	switch kind {
	case "path":
		p, _, _ := strings.Cut(ref, "#")
		if filepath.IsAbs(p) || strings.HasPrefix(filepath.Clean(p), "..") {
			return false
		}
		if _, err := os.Stat(filepath.Join(a.root, p)); err == nil {
			return true
		}
		if rest, ok := strings.CutPrefix(p, "work/"); ok && a.opts.WorkRoot != "" {
			_, err := os.Stat(filepath.Join(a.opts.WorkRoot, rest))
			return err == nil
		}
		return false
	case "hub":
		if e, ok := a.files[ref]; ok {
			return e.note.Type == "hub"
		}
		_, err := os.Stat(filepath.Join(a.root, "hubs", ref+".md"))
		return slugRE.MatchString(ref) && err == nil
	}
	e, err := a.load(ref)
	return err == nil && e != nil
}

func setField(n *Note, field string, raw json.RawMessage) error {
	if s := n.scalar(field); s != nil {
		return json.Unmarshal(raw, s)
	}
	if l := n.list(field); l != nil {
		var add []string
		if err := json.Unmarshal(raw, &add); err != nil {
			return fmt.Errorf("set %s: %w", field, err)
		}
		*l = union(*l, add) // lists only grow: an update never drops a hub, source or alias
		return nil
	}
	if field == "related" {
		var rs []Related
		if err := json.Unmarshal(raw, &rs); err != nil {
			return fmt.Errorf("set related: %w", err)
		}
		n.addRelated(rs...)
		return nil
	}
	return fmt.Errorf("field %q cannot be set (id and type are fixed; updated is stamped by code)", field)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
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
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ---- node_stats (SQLite) ----

// The tables are owned by internal/store (FORMAT.md); creating them here keeps counters working before that lands.
const statsDDL = `CREATE TABLE IF NOT EXISTS node_stats(id TEXT PRIMARY KEY, shown INTEGER NOT NULL DEFAULT 0,
	opened INTEGER NOT NULL DEFAULT 0, followed INTEGER NOT NULL DEFAULT 0, helpful INTEGER NOT NULL DEFAULT 0,
	harmful INTEGER NOT NULL DEFAULT 0, owner_confirmed INTEGER NOT NULL DEFAULT 0, last_shown_at TEXT,
	last_helpful_at TEXT);
CREATE TABLE IF NOT EXISTS delta_applied(key TEXT PRIMARY KEY, applied TEXT NOT NULL);`

type SQLiteStats struct{ db *sql.DB }

func OpenStats(dbPath string) (*SQLiteStats, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(statsDDL); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStats{db: db}, nil
}

func (s *SQLiteStats) Close() error { return s.db.Close() }

// AddCounters records the key and the increments in one transaction, so a replayed delta adds nothing.
func (s *SQLiteStats) AddCounters(key string, cs []Counter) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(`INSERT OR IGNORE INTO delta_applied(key, applied) VALUES(?, ?)`, key, now)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	for _, c := range cs {
		// the field name is from the closed counterFields set, never from free text
		q := fmt.Sprintf(`INSERT INTO node_stats(id, %[1]s) VALUES(?, ?)
			ON CONFLICT(id) DO UPDATE SET %[1]s = %[1]s + excluded.%[1]s`, c.Field)
		if _, err := tx.Exec(q, c.ID, c.Delta); err != nil {
			return false, err
		}
		if c.Field == "helpful" {
			if _, err := tx.Exec(`UPDATE node_stats SET last_helpful_at = ? WHERE id = ?`, now, c.ID); err != nil {
				return false, err
			}
		}
	}
	return true, tx.Commit()
}

func (s *SQLiteStats) Get(id, field string) (int, error) {
	if !counterFields[field] {
		return 0, fmt.Errorf("unknown counter %q", field)
	}
	var n int
	err := s.db.QueryRow(`SELECT `+field+` FROM node_stats WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
