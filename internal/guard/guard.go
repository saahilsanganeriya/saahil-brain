// Package guard decides whether an agent's Write, Edit or Read of a brain file goes ahead (DESIGN §4.8): block
// baselines past their caps and invalid notes, hubs, cards or MEMORY.md; warn on docs without the brain-ready
// header; point a Read of a big doc at brain_sections. Used by the Claude Code mod and Codex/Cursor PreToolUse.
package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/graph"
	"saahil.local/brain/internal/lint"
	"saahil.local/brain/internal/parse"
)

type Verdict string

const (
	Allow Verdict = "allow"
	Warn  Verdict = "warn"
	Block Verdict = "block"
)

type Edit struct {
	Old, New string
	All      bool
}

type Request struct {
	Tool       string // Write | Edit | MultiEdit | Read
	Path       string // absolute, either home spelling
	Content    []byte // Write: the whole new file
	HasContent bool
	Edits      []Edit // Edit, MultiEdit: applied in order to the file as it is now
}

type Decision struct {
	Decision Verdict `json:"decision"`
	Reason   string  `json:"reason"`
	Append   string  `json:"append,omitempty"`
}

// Decide fails open: anything it cannot read or apply is allowed, and the tool reports its own error.
func Decide(root string, req Request) Decision {
	l := lint.New(root)
	rel, ok := under(root, req.Path)
	if !ok {
		if req.Tool != "Read" {
			return Decision{Decision: Allow}
		}
		// before the cut-over, work docs are read from the saahil-docs shadow; reads there get the hint too
		if rel, ok = under(l.WorkRoot, req.Path); !ok {
			return Decision{Decision: Allow}
		}
		rel = "work/" + rel
	}
	switch req.Tool {
	case "Read":
		return readHint(rel, req.Path)
	case "Write", "Edit", "MultiEdit":
	default:
		return Decision{Decision: Allow}
	}
	old, err := os.ReadFile(req.Path)
	existed := err == nil
	next := req.Content
	if req.Tool != "Write" {
		if !existed {
			return Decision{Decision: Allow}
		}
		if next, ok = apply(string(old), req.Edits); !ok {
			return Decision{Decision: Allow}
		}
	} else if !req.HasContent {
		return Decision{Decision: Allow}
	}
	return judge(l, rel, old, existed, next)
}

func judge(l *lint.Linter, rel string, old []byte, existed bool, next []byte) Decision {
	kind := lint.Kind(rel)
	var blocks, warns []string
	for _, is := range l.Check(rel, next) {
		msg := rel + ": " + is.Msg
		switch {
		case is.Sev == lint.Warn:
			warns = append(warns, msg)
		case is.Rule == "cap" && kind != "state" && existed && len(next) <= len(old):
			// an oversized baseline being cut down is progress, not a ledger
			warns = append(warns, msg+" (still over its cap)")
		case is.Rule == "cap" && kind != "state":
			blocks = append(blocks, msg+" — ledger-style growth: overwrite the baseline, move history to log/")
		case is.Rule == "secret" || is.Rule == "cap" || kind == "note" || kind == "hub" || kind == "card" || kind == "memory-index":
			blocks = append(blocks, msg)
		default:
			warns = append(warns, msg)
		}
	}
	switch {
	case len(blocks) > 0:
		return Decision{Decision: Block, Reason: join(blocks)}
	case len(warns) > 0:
		return Decision{Decision: Warn, Reason: join(warns)}
	}
	return Decision{Decision: Allow}
}

func join(msgs []string) string {
	if len(msgs) > 3 {
		msgs = append(msgs[:3], fmt.Sprintf("and %d more", len(msgs)-3))
	}
	return strings.Join(msgs, "; ")
}

func readHint(rel, abs string) Decision {
	fi, err := os.Stat(abs)
	if err != nil || fi.Size() <= config.BigDocBytes || filepath.Ext(abs) != ".md" {
		return Decision{Decision: Allow}
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Decision{Decision: Allow}
	}
	n := len(parse.Markdown(data).Sections())
	if n < 2 {
		return Decision{Decision: Allow}
	}
	return Decision{Decision: Allow, Append: fmt.Sprintf("%d sections — use brain_sections %s to read one", n, graph.ID(rel))}
}

// apply replays Edit/MultiEdit on the current text; ok is false when an old string is missing.
func apply(text string, edits []Edit) ([]byte, bool) {
	for _, e := range edits {
		if e.Old == "" || !strings.Contains(text, e.Old) {
			return nil, false
		}
		if e.All {
			text = strings.ReplaceAll(text, e.Old, e.New)
		} else {
			text = strings.Replace(text, e.Old, e.New, 1)
		}
	}
	return []byte(text), true
}

// under returns p relative to root when p lies inside it (either home spelling).
func under(root, p string) (string, bool) {
	if root == "" || p == "" {
		return "", false
	}
	r, a := config.Canonical(root), config.Canonical(p)
	if !strings.HasPrefix(a, r+"/") {
		return "", false
	}
	return filepath.ToSlash(strings.TrimPrefix(a, r+"/")), true
}
