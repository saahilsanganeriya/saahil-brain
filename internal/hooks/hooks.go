// Package hooks serves the read path's hooks: `hook start` (the start digest), `hook prompt` (≤ 3 pointers or
// nothing, and on a session's first prompt the work part of its digest), `hook post` (opened/followed counts for
// what a session was shown, and brain pulls) and `hook end` / `hook compact` (ignored counts).
//
// Until the cut-over (no <brain>/.brain/live) every hook runs in SHADOW: it computes what it would inject, logs
// that to the lookups table and injects nothing. A lookups row never holds prompt text: `candidates` and `chosen`
// are JSON arrays of node ids (for start, and for the digest a first prompt adds, logged as hook "digest": of the
// digest's sources, and `chosen` is {"chars": N, "sources": [...]}).
package hooks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/store"
)

// Input is the union of the hook payloads Claude Code, Codex, Cursor and the agent adapters send on stdin.
type Input struct {
	SessionID      string          `json:"session_id"`
	ConversationID string          `json:"conversation_id"` // Cursor
	Cwd            string          `json:"cwd"`
	WorkspaceRoots []string        `json:"workspace_roots"` // Cursor
	Event          string          `json:"hook_event_name"`
	Source         string          `json:"source"`
	AgentID        string          `json:"agent_id"`
	Prompt         string          `json:"prompt"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
}

func (in Input) session() string {
	if in.SessionID != "" {
		return in.SessionID
	}
	return in.ConversationID
}

func (in Input) cwd() string {
	if in.Cwd == "" && len(in.WorkspaceRoots) > 0 {
		return config.Canonical(in.WorkspaceRoots[0])
	}
	return config.Canonical(in.Cwd)
}

type Env struct {
	Root     string // the brain
	WorkRoot string // where work/<box> lives before a cut-over fills <brain>/work (shadow_docs); "" = none
	StateDir string // per-machine hook state: each session's work, hubs and what it was shown
	Now      func() time.Time
	Rebuild  func() // starts RebuildNames in the background; nil rebuilds nothing
}

func DefaultEnv() Env {
	return Env{
		Root:     config.BrainRoot(),
		WorkRoot: config.ShadowDocRoot(),
		StateDir: filepath.Join(config.StateDir(), "hooks"),
		Now:      time.Now,
	}
}

// Live is true once the cut-over created <brain>/.brain/live; until then hooks inject nothing.
func (e Env) Live() bool {
	_, err := os.Stat(filepath.Join(e.Root, ".brain", "live"))
	return err == nil
}

// openIndex returns nil when the brain has no index yet: a hook never creates one.
func (e Env) openIndex() (*index.Index, error) {
	if _, err := os.Stat(store.Path(e.Root)); err != nil {
		return nil, nil
	}
	ix, err := index.Open(e.Root)
	if err != nil {
		return nil, err
	}
	// one connection, and a short busy wait: an index update holding the write lock must not eat the hook budget
	ix.DB().SetMaxOpenConns(1)
	if _, err := ix.DB().Exec(`PRAGMA busy_timeout = 100`); err != nil {
		ix.Close()
		return nil, err
	}
	return ix, nil
}

// workDir is where a work box lives now: <brain>/work/<box>, else the shadow; "" when neither has it.
func (e Env) workDir(box string) string {
	if box == "" || strings.ContainsAny(box, `/\`) || box == "." || box == ".." {
		return ""
	}
	dirs := []string{filepath.Join(e.Root, "work", box)}
	if e.WorkRoot != "" {
		dirs = append(dirs, filepath.Join(e.WorkRoot, box))
	}
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
	}
	return ""
}

// absPath maps a node path to the file it was read from.
func (e Env) absPath(p string) string {
	switch {
	case strings.HasPrefix(p, "work/"):
		box, rest, _ := strings.Cut(strings.TrimPrefix(p, "work/"), "/")
		if dir := e.workDir(box); dir != "" {
			return config.Canonical(filepath.Join(dir, rest))
		}
	case strings.HasPrefix(p, "memory-import/"):
		slug, rest, _ := strings.Cut(strings.TrimPrefix(p, "memory-import/"), "/")
		return config.Canonical(filepath.Join(config.ClaudeDir(), "projects", slug, "memory", rest))
	}
	return config.Canonical(filepath.Join(e.Root, p))
}

// ---- per-session state ----

type state struct {
	Work    []string          `json:"work,omitempty"`
	Hubs    []string          `json:"hubs,omitempty"`
	Shown   map[string]string `json:"shown,omitempty"`   // node id → absolute path
	Counted map[string]bool   `json:"counted,omitempty"` // "opened:<id>", "followed:<id>": once per session
	// Until the first prompt: the boxes the cwd suggests, and the L0 chars start already gave.
	DigestOwed  bool     `json:"digest_owed,omitempty"`
	CwdWork     []string `json:"cwd_work,omitempty"`
	DigestChars int      `json:"digest_chars,omitempty"`
}

var sessionRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func (e Env) statePath(session string) string {
	if !sessionRE.MatchString(session) || strings.Trim(session, ".") == "" {
		return ""
	}
	return filepath.Join(e.StateDir, session+".json")
}

func (e Env) loadState(session string) (state, bool) {
	var st state
	p := e.statePath(session)
	if p == "" {
		return st, false
	}
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &st) != nil {
		return state{}, false
	}
	return st, true
}

func (e Env) saveState(session string, st state) error {
	p := e.statePath(session)
	if p == "" {
		return errors.New("no usable session id")
	}
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

const (
	stateTTL       = 7 * 24 * time.Hour
	maxStatePrunes = 200
)

// pruneStates drops state files of sessions idle for a week; bounded so a start hook stays cheap.
func (e Env) pruneStates() {
	entries, err := os.ReadDir(e.StateDir)
	if err != nil {
		return
	}
	cutoff, pruned := e.Now().Add(-stateTTL), 0
	for _, ent := range entries {
		if fi, err := ent.Info(); err == nil && fi.ModTime().Before(cutoff) && pruned < maxStatePrunes {
			if os.Remove(filepath.Join(e.StateDir, ent.Name())) == nil {
				pruned++
			}
		}
	}
}

// ---- output and logging ----

// Output is Claude Code's hook JSON; Codex, Cursor and the adapters read the same additionalContext.
func Output(event, text string) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}})
	return b
}

func logLookup(tx execer, now time.Time, session, hook string, candidates, chosen any, abstained bool, took time.Duration) error {
	c, _ := json.Marshal(candidates)
	ch, _ := json.Marshal(chosen)
	_, err := tx.Exec(`INSERT INTO lookups(time, session, hook, candidates, chosen, abstained, ms) VALUES (?,?,?,?,?,?,?)`,
		now.UTC().Format(time.RFC3339Nano), session, hook, string(c), string(ch), abstained, float64(took.Microseconds())/1000)
	return err
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
