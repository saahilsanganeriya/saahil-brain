// Package agents finds live agent sessions on this machine and moves notes between them.
package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"saahil.local/brain/internal/config"
)

// Session is one live Claude Code session, as its own registry file describes it.
type Session struct {
	Machine    string `json:"machine"`
	Name       string `json:"name"`
	SessionID  string `json:"sessionId"`
	PID        int    `json:"pid"`
	CWD        string `json:"cwd"`
	Status     string `json:"status"`
	Kind       string `json:"kind,omitempty"`
	UpdatedAt  int64  `json:"updatedAt,omitempty"`
	OrcaHandle string `json:"orcaHandle,omitempty"`
}

// Address is how other agents name this session: <machine>:<name>.
func (s Session) Address() string { return s.Machine + ":" + s.Name }

type registryFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	ProcStart string `json:"procStart"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
}

func RegistryDir() string { return filepath.Join(config.ClaudeDir(), "sessions") }

// LocalSessions lists sessions whose process is still the one that wrote the registry file: a dead pid, or a pid
// reused by another process since, is skipped.
func LocalSessions(dir string) ([]Session, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	machine := string(config.ThisMachine())
	seen := map[string]bool{}
	var sessions []Session
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rf registryFile
		if json.Unmarshal(raw, &rf) != nil || rf.SessionID == "" || rf.PID <= 0 {
			continue
		}
		if !processIsSame(rf.PID, rf.ProcStart) || seen[rf.SessionID] {
			continue
		}
		seen[rf.SessionID] = true
		name := rf.Name
		if name == "" {
			name = Short(rf.SessionID)
		}
		sessions = append(sessions, Session{
			Machine: machine, Name: name, SessionID: rf.SessionID, PID: rf.PID, CWD: config.Canonical(rf.CWD),
			Status: rf.Status, Kind: rf.Kind, UpdatedAt: rf.UpdatedAt, OrcaHandle: handleLookup(rf.PID),
		})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
	return sessions, nil
}

// Resolve finds one session by name, address or session-id prefix.
func Resolve(sessions []Session, target string) (Session, bool) {
	if _, name, ok := strings.Cut(target, ":"); ok {
		target = name
	}
	var prefixMatch []Session
	for _, s := range sessions {
		if s.Name == target || s.SessionID == target {
			return s, true
		}
		if len(target) >= 6 && strings.HasPrefix(s.SessionID, target) {
			prefixMatch = append(prefixMatch, s)
		}
	}
	if len(prefixMatch) == 1 {
		return prefixMatch[0], true
	}
	return Session{}, false
}

// SessionByID finds the registry entry of a live session id on this machine.
func SessionByID(id string) (Session, bool) {
	sessions, _ := LocalSessions(RegistryDir())
	for _, s := range sessions {
		if s.SessionID == id {
			return s, true
		}
	}
	return Session{}, false
}

// handleLookup is replaced in tests: a test process run inside Orca carries its own terminal handle.
var handleLookup = orcaHandleOf

// Short is the 8-character prefix people read; ids shorter than that are returned whole.
func Short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
