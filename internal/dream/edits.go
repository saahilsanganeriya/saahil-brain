package dream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Edit is what a skill may ask for. Only "write" is ever applied; "delete" is in the schema so a skill that wants one
// says so, and the refusal is counted.
type Edit struct {
	Path    string `json:"path"` // relative to the brain root
	Action  string `json:"action"`
	Content string `json:"content,omitempty"`
	Why     string `json:"why,omitempty"`
}

type Applied struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

type Rejection struct {
	Step   string `json:"step"`
	Path   string `json:"path,omitempty"`
	Action string `json:"action,omitempty"`
	Reason string `json:"reason"`
	Held   bool   `json:"held,omitempty"` // the run's edit cap held it
	Lost   bool   `json:"lost,omitempty"` // held, and its step judges only this run's proposals: it never returns
}

type ApplyInput struct {
	RunID     string
	Step      string
	Edits     []Edit
	Remaining int      // new files this step may still change
	Changed   []string // files the run already changed: changing one again costs nothing
	Whole     bool     // one unit (a learn session's answer): every edit lands, or every edit waits for the next dream
}

type ApplyResult struct {
	Applied  []Applied
	Rejected []Rejection
	Noop     int
}

type RevertInput struct {
	RunID   string
	Applied []Applied
}

// EditsSchema is the --json-schema every dream skill answers with.
const EditsSchema = `{"type":"object","required":["edits"],"additionalProperties":false,"properties":{
"edits":{"type":"array","items":{"type":"object","required":["path","action"],"additionalProperties":false,"properties":{
"path":{"type":"string","description":"relative to the brain root, e.g. notes/finding-x.md"},
"action":{"type":"string","enum":["write","delete"]},
"content":{"type":"string","description":"the whole new file"},
"why":{"type":"string"}}}},
"notes":{"type":"string"}}}`

// The dream writes knowledge files only; sessions/, inbox/, BRAIN.md and every dot-folder belong to someone else.
var writableTops = map[string]bool{"notes": true, "hubs": true, "journal": true, "memory": true, "work": true,
	"hot.md": true, "index.md": true}

const maxEditBytes = 64 * 1024

func (a *Activities) ApplyEdits(_ context.Context, in ApplyInput) (ApplyResult, error) {
	var res ApplyResult
	changed := make(map[string]bool, len(in.Changed))
	for _, p := range in.Changed {
		changed[p] = true
	}
	holdAll, needed := false, 0
	if in.Whole {
		var err error
		if needed, err = a.newFilesNeeded(in.Edits, changed); err != nil {
			return res, err
		}
		// a unit larger than a whole run could never land whole: it applies what fits, as any other step
		holdAll = needed > in.Remaining && needed <= MaxEdits
	}
	newFiles := 0
	for _, e := range in.Edits {
		reject := func(reason string) {
			res.Rejected = append(res.Rejected, Rejection{Step: in.Step, Path: e.Path, Action: e.Action, Reason: reason})
		}
		hold := func() {
			limit := fmt.Sprintf("run edit cap %d reached", MaxEdits)
			if slices.Contains(curationSteps, in.Step) {
				limit = fmt.Sprintf("%s's share of the edit cap (%d new files) reached", in.Step, in.Remaining)
			}
			fate, lost := "waits for the next dream", !rebuildsEachRun[in.Step]
			if holdAll {
				limit += fmt.Sprintf(" (%d new files needed, %d left)", needed, in.Remaining)
				fate, lost = "waits for the next dream, whole", false
			} else if lost {
				fate = "lost: " + in.Step + " judges only this run's proposals"
			}
			res.Rejected = append(res.Rejected, Rejection{Step: in.Step, Path: e.Path, Action: e.Action, Held: true,
				Lost: lost, Reason: limit + "; " + fate})
		}
		if e.Action != "write" {
			reject("refused " + e.Action + ": the dream never deletes; only writes are applied")
			continue
		}
		abs, err := a.writablePath(e.Path)
		if err != nil {
			reject(err.Error())
			continue
		}
		if len(e.Content) > maxEditBytes {
			reject(fmt.Sprintf("content is %d bytes (> %d)", len(e.Content), maxEditBytes))
			continue
		}
		old, err := os.ReadFile(abs)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
		if exists && bytes.Equal(old, []byte(e.Content)) {
			res.Noop++
			continue
		}
		if holdAll || (!changed[e.Path] && newFiles >= in.Remaining) {
			hold()
			continue
		}
		backup := filepath.Join(a.Cfg.BrainRoot, ".archive", "dream", in.RunID, "before", e.Path)
		if exists {
			if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
				if err := writeFile(backup, old); err != nil {
					return res, err
				}
			}
		}
		if err := writeFile(abs, []byte(e.Content)); err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, Applied{Path: e.Path, Created: !exists})
		if !changed[e.Path] {
			changed[e.Path] = true
			newFiles++
		}
	}
	return res, nil
}

// newFilesNeeded counts the files the edits would change that the run has not changed yet: refused and unchanged
// writes cost nothing.
func (a *Activities) newFilesNeeded(edits []Edit, changed map[string]bool) (int, error) {
	files := map[string]bool{}
	for _, e := range edits {
		if e.Action != "write" || changed[e.Path] || files[e.Path] || len(e.Content) > maxEditBytes {
			continue
		}
		abs, err := a.writablePath(e.Path)
		if err != nil {
			continue
		}
		old, err := os.ReadFile(abs)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		if err == nil && bytes.Equal(old, []byte(e.Content)) {
			continue
		}
		files[e.Path] = true
	}
	return len(files), nil
}

// writablePath resolves a skill-supplied path and refuses anything that could land outside the brain's knowledge
// folders: absolute paths, "..", dot-folders, non-markdown, symlinked files, and directories whose real location is
// outside the brain root or under a forbidden root (saahil-docs, ~/.claude) — e.g. a work/ that links to saahil-docs.
func (a *Activities) writablePath(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if rel == "" || filepath.IsAbs(rel) || clean != rel || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path must be a clean relative path inside the brain")
	}
	parts := strings.Split(clean, string(filepath.Separator))
	if !writableTops[parts[0]] || (len(parts) == 1 && !strings.HasSuffix(clean, ".md")) {
		return "", fmt.Errorf("the dream writes only under notes/ hubs/ journal/ memory/ work/, hot.md, index.md")
	}
	for _, p := range parts {
		if strings.HasPrefix(p, ".") {
			return "", fmt.Errorf("no dot-folders or dot-files")
		}
	}
	// the cut-over swaps saahil-docs in with one rename that needs work/ to be an empty folder
	if parts[0] == "work" && !a.live() {
		return "", fmt.Errorf("work/ stays empty until the cut-over (shadow mode: no .brain/live)")
	}
	if filepath.Ext(clean) != ".md" {
		return "", fmt.Errorf("only .md files")
	}
	abs := filepath.Join(a.Cfg.BrainRoot, clean)
	if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to write through a symlink")
	}
	realRoot, err := filepath.EvalSymlinks(a.Cfg.BrainRoot)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if !within(realDir, realRoot) {
		return "", fmt.Errorf("resolves outside the brain root")
	}
	for _, f := range a.Cfg.Forbidden {
		if realF, err := filepath.EvalSymlinks(f); err == nil && within(realDir, realF) {
			return "", fmt.Errorf("resolves into a read-only root")
		}
	}
	return abs, nil
}

// live is true once the cut-over has switched the brain out of shadow mode.
func (a *Activities) live() bool {
	_, err := os.Stat(filepath.Join(a.Cfg.BrainRoot, ".brain", "live"))
	return err == nil
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// Revert puts every file this run edited back as it was before the run; a file the run created is moved into the
// run's archive folder rather than deleted.
func (a *Activities) Revert(_ context.Context, in RevertInput) (int, error) {
	runDir := filepath.Join(a.Cfg.BrainRoot, ".archive", "dream", in.RunID)
	seen := map[string]bool{}
	n := 0
	for _, ap := range in.Applied {
		if seen[ap.Path] {
			continue
		}
		seen[ap.Path] = true
		abs := filepath.Join(a.Cfg.BrainRoot, ap.Path)
		if ap.Created {
			dst := filepath.Join(runDir, "reverted", ap.Path)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return n, err
			}
			if err := os.Rename(abs, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return n, err
			}
		} else {
			old, err := os.ReadFile(filepath.Join(runDir, "before", ap.Path))
			if err != nil {
				return n, err
			}
			if err := writeFile(abs, old); err != nil {
				return n, err
			}
		}
		n++
	}
	return n, nil
}

func writeFile(path string, b []byte) error {
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
