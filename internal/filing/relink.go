package filing

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var wikiLink = regexp.MustCompile(`\[\[([^\]|#]+)((?:#[^\]|]*)?)((?:\|[^\]]*)?)\]\]`)

// RelinkReport counts what Relink did to the [[wikilinks]] filed notes carry over from their memories.
type RelinkReport struct {
	Files     int `json:"files_changed"`
	Rewritten int `json:"rewritten"` // [[old-memory-name]] → [[note-id]] it was filed as
	Unwrapped int `json:"unwrapped"` // no note came from it: the words stay, the brackets go
}

// Relink rewrites wikilinks that still name a memory file (they came along when the memory was filed) to the note
// the memory became, and unwraps the ones that have no note, so brain-written notes keep no dangling link.
func Relink(root string) (RelinkReport, error) {
	var rep RelinkReport
	st, err := loadState(root)
	if err != nil {
		return rep, err
	}
	bySlug := map[string]string{}
	for _, f := range st.Filed {
		if len(f.Notes) == 0 {
			continue
		}
		notes := append([]string(nil), f.Notes...)
		sort.Strings(notes)
		bySlug[strings.TrimSuffix(filepath.Base(f.Path), ".md")] = notes[0]
	}
	exists := func(id string) bool {
		for _, dir := range []string{"notes", "hubs"} {
			if _, err := os.Stat(filepath.Join(root, dir, id+".md")); err == nil {
				return true
			}
		}
		return false
	}
	paths, err := filepath.Glob(filepath.Join(root, "notes", "*.md"))
	if err != nil {
		return rep, err
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return rep, err
		}
		head, body := splitFrontmatter(raw)
		changed := false
		out := wikiLink.ReplaceAllFunc(body, func(m []byte) []byte {
			g := wikiLink.FindSubmatch(m)
			target, anchor, alias := strings.TrimSpace(string(g[1])), string(g[2]), string(g[3])
			if exists(target) {
				return m
			}
			changed = true
			if id, ok := bySlug[target]; ok && exists(id) {
				rep.Rewritten++
				return []byte("[[" + id + anchor + alias + "]]")
			}
			rep.Unwrapped++
			if alias != "" {
				return []byte(strings.TrimPrefix(alias, "|"))
			}
			return []byte(target)
		})
		if !changed {
			continue
		}
		tmp := path + ".brain-tmp"
		if err := os.WriteFile(tmp, append(head, out...), 0o644); err != nil {
			return rep, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return rep, err
		}
		rep.Files++
	}
	return rep, nil
}

// splitFrontmatter returns the YAML block (with its fences) and the body after it; links are only rewritten in the body.
func splitFrontmatter(raw []byte) (head, body []byte) {
	if !bytes.HasPrefix(raw, []byte("---\n")) {
		return nil, raw
	}
	end := bytes.Index(raw[4:], []byte("\n---\n"))
	if end < 0 {
		return nil, raw
	}
	cut := 4 + end + len("\n---\n")
	return raw[:cut], raw[cut:]
}
