package delta

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// PendingLink is a link a note asked for before its target existed.
type PendingLink struct {
	Note  string `json:"note"`
	Field string `json:"field"` // about | supersedes | related
	Ref   string `json:"ref"`
	Why   string `json:"why,omitempty"`
}

func pendingPath(root string) string { return filepath.Join(root, ".brain", "pending-links.jsonl") }

func appendPending(root string, links []PendingLink) error {
	if err := os.MkdirAll(filepath.Dir(pendingPath(root)), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(pendingPath(root), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, l := range links {
		b, _ := json.Marshal(l)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// ResolvePending adds every queued link whose source and target now both exist, through the normal checked path, and
// keeps the rest queued. It returns how many links it added and how many still wait.
func ResolvePending(root string, now time.Time) (added, waiting int, err error) {
	raw, err := os.ReadFile(pendingPath(root))
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	var ready []PendingLink
	var rest bytes.Buffer
	seen := map[PendingLink]bool{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var l PendingLink
		if json.Unmarshal(sc.Bytes(), &l) != nil || seen[l] {
			continue
		}
		seen[l] = true
		if exists(root, l.Note) && exists(root, l.Ref) {
			ready = append(ready, l)
		} else {
			rest.Write(append(append([]byte{}, sc.Bytes()...), '\n'))
		}
	}
	if len(ready) > 0 {
		var ops []Op
		for _, l := range ready {
			op := Op{Op: "link", Src: l.Note, Rel: l.Field, Dst: l.Ref, Why: l.Why}
			if l.Field == "supersedes" {
				op = Op{Op: "supersede", Old: l.Ref, New: l.Note}
			}
			ops = append(ops, op)
		}
		rep, err := Apply(context.Background(), root, Delta{Ops: ops}, Options{Now: now})
		if err != nil {
			return 0, 0, err
		}
		added = len(ready) - len(rep.Rejected)
	}
	tmp := pendingPath(root) + ".tmp"
	if err := os.WriteFile(tmp, rest.Bytes(), 0o644); err != nil {
		return added, 0, err
	}
	return added, bytes.Count(rest.Bytes(), []byte("\n")), os.Rename(tmp, pendingPath(root))
}

func exists(root, id string) bool {
	for _, dir := range []string{"notes", "hubs"} {
		if _, err := os.Stat(filepath.Join(root, dir, id+".md")); err == nil {
			return true
		}
	}
	return false
}
