package install

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
)

// Report is what one host run did (install, off) or would do (dry run, verify: Added is then what is missing).
type Report struct {
	Host      string       `json:"host"`
	Present   bool         `json:"present"`
	Added     []Entry      `json:"added,omitempty"`
	Removed   []Entry      `json:"removed,omitempty"`
	Conflicts []string     `json:"conflicts,omitempty"`
	Changes   []FileChange `json:"changes,omitempty"`
	Labels    []string     `json:"labels,omitempty"`  // every label the host's steps carry
	Missing   []string     `json:"missing,omitempty"` // labels with something not in place
}

type run struct {
	now    time.Time
	dryRun bool
	action string
}

// Install ensures every present host's steps (only the named hosts when given) and records the result in m.
func Install(m *Manifest, repo string, only []string, now time.Time, dryRun bool) ([]Report, error) {
	r := run{now: now, dryRun: dryRun, action: "install"}
	var reports []Report
	for _, h := range Hosts(repo) {
		if !selected(h.Name, only) {
			continue
		}
		rep := Report{Host: h.Name, Present: h.Present()}
		if rep.Present {
			if err := r.ensure(h, &rep); err != nil {
				return reports, fmt.Errorf("%s: %w", h.Name, err)
			}
			rec := m.record(h.Name)
			rec.merge(rep.Added)
			rec.Changes = append(rec.Changes, rep.Changes...)
		}
		reports = append(reports, rep)
	}
	m.Updated = now.UTC()
	return reports, nil
}

// Verify re-checks each present host without writing: Added lists what is missing, Conflicts what is in the way.
func Verify(repo string, only []string) []Report {
	r := run{now: time.Now(), dryRun: true, action: "verify"}
	var reports []Report
	for _, h := range Hosts(repo) {
		if !selected(h.Name, only) {
			continue
		}
		rep := Report{Host: h.Name, Present: h.Present()}
		if rep.Present {
			if err := r.ensure(h, &rep); err != nil {
				rep.Conflicts = append(rep.Conflicts, err.Error())
			}
			rep.Changes = nil
			if h.Name == "claude" {
				rep.Conflicts = append(rep.Conflicts, claudeDuplicateHooks()...)
			}
		}
		reports = append(reports, rep)
	}
	return reports
}

// Off removes what the manifest says the installer added to host, and only that: an entry the owner changed since
// is left in place and reported. Nothing is restored from a backup, so later edits by anyone else survive.
func Off(m *Manifest, host string, now time.Time, dryRun bool) (Report, error) {
	r := run{now: now, dryRun: dryRun, action: "off"}
	rep := Report{Host: host, Present: true}
	rec := m.Hosts[host]
	if rec == nil || len(rec.Added) == 0 {
		return rep, nil
	}
	for _, path := range pathsOf(rec.Added) {
		var entries []Entry
		for _, e := range rec.Added {
			if e.Path == path {
				entries = append(entries, e)
			}
		}
		if err := r.remove(path, entries, &rep); err != nil {
			return rep, err
		}
	}
	rec.Added = nil
	rec.Changes = append(rec.Changes, rep.Changes...)
	m.Updated = now.UTC()
	return rep, nil
}

func selected(name string, only []string) bool {
	if len(only) == 0 {
		return true
	}
	for _, o := range only {
		if o == name {
			return true
		}
	}
	return false
}

func pathsOf(entries []Entry) []string {
	var paths []string
	seen := map[string]bool{}
	for _, e := range entries {
		if !seen[e.Path] {
			seen[e.Path] = true
			paths = append(paths, e.Path)
		}
	}
	return paths
}

// ensure applies a host's steps, one read and at most one write per file.
func (r run) ensure(h Host, rep *Report) error {
	var order []string
	byPath := map[string][]Step{}
	for _, s := range h.Steps {
		if byPath[s.Path] == nil {
			order = append(order, s.Path)
		}
		byPath[s.Path] = append(byPath[s.Path], s)
		if !contains(rep.Labels, s.Label) {
			rep.Labels = append(rep.Labels, s.Label)
		}
	}
	for _, path := range order {
		before := len(rep.Added)
		if err := r.ensureFile(path, byPath[path], rep); err != nil {
			return err
		}
		for _, s := range byPath[path] {
			for _, e := range rep.Added[before:] {
				if entryOf(s, e) && !contains(rep.Missing, s.Label) {
					rep.Missing = append(rep.Missing, s.Label)
				}
			}
		}
	}
	return nil
}

func entryOf(s Step, e Entry) bool { return e.Path == s.Path && e.Kind == s.Kind }

func (r run) ensureFile(path string, steps []Step, rep *Report) error {
	switch family(steps[0].Kind) {
	case "symlink":
		s := steps[0]
		if _, err := os.Stat(filepath.Join(s.Target, ".claude-plugin", "plugin.json")); err != nil {
			rep.Conflicts = append(rep.Conflicts, "the mod is not at "+s.Target)
			return nil
		}
		if have, err := os.Readlink(path); err == nil && have == s.Target {
			return nil
		}
		if _, err := os.Lstat(path); err == nil {
			rep.Conflicts = append(rep.Conflicts, path+" exists and is not our link")
			return nil
		}
		rep.Added = append(rep.Added, Entry{Kind: "symlink", Path: path, Target: s.Target})
		rep.Changes = append(rep.Changes, FileChange{Path: path, Action: r.action, SHA256After: "symlink → " + s.Target, At: r.now.UTC()})
		if r.dryRun {
			return nil
		}
		if err := guardOutsideRepos(path); err != nil {
			return err
		}
		return os.Symlink(s.Target, path)
	}

	raw, existed, err := readOptional(path)
	if err != nil {
		return err
	}
	var out []byte
	var added []Entry
	switch family(steps[0].Kind) {
	case "json":
		doc, err := parseJSONDoc(path, raw)
		if err != nil {
			return err
		}
		for _, s := range steps {
			a, c := ensureJSON(doc, s)
			added, rep.Conflicts = append(added, a...), append(rep.Conflicts, c...)
		}
		if len(added) > 0 {
			if out, err = marshalJSON(doc); err != nil {
				return err
			}
		}
	case "toml-table":
		text := string(raw)
		for _, s := range steps {
			if !tomlHasTable(text, s.Table) {
				text = tomlAppendTable(text, s.Table, s.Body)
				added = append(added, Entry{Kind: s.Kind, Path: path, Text: s.Table})
			}
		}
		out = []byte(text)
	case "file":
		s := steps[0]
		switch {
		case existed && bytes.Equal(raw, s.Content):
		case existed && !bytes.HasPrefix(raw, s.Owned):
			rep.Conflicts = append(rep.Conflicts, path+" exists and is not ours")
		default:
			out = s.Content
			added = append(added, Entry{Kind: "file", Path: path, SHA256: sha(s.Content)})
		}
	case "ignore-line":
		s := steps[0]
		text, changes := mergeIgnore(string(raw), s.Drop, s.Lines)
		for _, c := range changes {
			if strings.HasPrefix(c, "+") {
				added = append(added, Entry{Kind: "ignore-line", Path: path, Text: c[1:]})
			}
		}
		if len(changes) > 0 {
			out = []byte(text)
			if len(added) == 0 {
				added = []Entry{} // a dropped wrong form still needs the write
			}
		}
	}
	if added == nil {
		return nil
	}
	rep.Added = append(rep.Added, added...)
	return r.write(path, raw, existed, out, rep)
}

func (r run) remove(path string, entries []Entry, rep *Report) error {
	if entries[0].Kind == "symlink" {
		if have, err := os.Readlink(path); err != nil || have != entries[0].Target {
			rep.Conflicts = append(rep.Conflicts, path+" is no longer our link; left in place")
			return nil
		}
		rep.Removed = append(rep.Removed, entries[0])
		rep.Changes = append(rep.Changes, FileChange{Path: path, Action: r.action, SHA256Before: "symlink → " + entries[0].Target, At: r.now.UTC()})
		if r.dryRun {
			return nil
		}
		return os.Remove(path)
	}
	raw, existed, err := readOptional(path)
	if err != nil {
		return err
	}
	if !existed {
		rep.Conflicts = append(rep.Conflicts, path+" is gone")
		return nil
	}
	var out []byte
	var removed []Entry
	leftInPlace := func(e Entry) {
		rep.Conflicts = append(rep.Conflicts, fmt.Sprintf("%s: %s %s%s%s changed since install; left in place", path, e.Kind, e.Event, e.Text, strings.Join(e.Key, ".")))
	}
	switch family(entries[0].Kind) {
	case "json":
		doc, err := parseJSONDoc(path, raw)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if removeJSON(doc, e) {
				removed = append(removed, e)
			} else {
				leftInPlace(e)
			}
		}
		if len(removed) > 0 {
			if out, err = marshalJSON(doc); err != nil {
				return err
			}
		}
	case "toml-table":
		text := string(raw)
		for _, e := range entries {
			var ok bool
			if text, ok = tomlRemoveTable(text, e.Text); ok {
				removed = append(removed, e)
			} else {
				leftInPlace(e)
			}
		}
		out = []byte(text)
	case "ignore-line":
		text := string(raw)
		for _, e := range entries {
			var ok bool
			if text, ok = removeIgnoreLine(text, e.Text); ok {
				removed = append(removed, e)
			}
		}
		out = []byte(text)
	case "file":
		if sha(raw) != entries[0].SHA256 {
			leftInPlace(entries[0])
			return nil
		}
		removed = entries
		rep.Removed = append(rep.Removed, removed...)
		rep.Changes = append(rep.Changes, FileChange{Path: path, Action: r.action, Backup: r.backupPath(path), SHA256Before: sha(raw), At: r.now.UTC()})
		if r.dryRun {
			return nil
		}
		if err := os.WriteFile(r.backupPath(path), raw, 0o600); err != nil {
			return err
		}
		return os.Remove(path)
	}
	if len(removed) == 0 {
		return nil
	}
	rep.Removed = append(rep.Removed, removed...)
	return r.write(path, raw, true, out, rep)
}

func (r run) backupPath(path string) string {
	return path + ".bak-brain-" + r.now.UTC().Format("20060102T150405Z")
}

// write replaces path atomically, keeping the previous bytes beside it, and records the change.
func (r run) write(path string, previous []byte, existed bool, out []byte, rep *Report) error {
	change := FileChange{Path: path, Action: r.action, SHA256After: sha(out), At: r.now.UTC()}
	if existed {
		change.Backup, change.SHA256Before = r.backupPath(path), sha(previous)
	}
	rep.Changes = append(rep.Changes, change)
	if r.dryRun {
		return nil
	}
	if err := guardOutsideRepos(path); err != nil {
		return err
	}
	if existed {
		if err := os.WriteFile(change.Backup, previous, 0o600); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".brain-tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// guardOutsideRepos refuses any write inside a git work tree below the home folder: the installer must never put a
// file or link into a repo checkout (company repos above all).
func guardOutsideRepos(path string) error {
	home := config.Home()
	for dir := filepath.Dir(path); dir != home && strings.HasPrefix(dir, home+"/"); dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return fmt.Errorf("refusing to write %s: %s is a git repository", path, dir)
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
