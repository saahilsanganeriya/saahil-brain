package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"saahil.local/brain/internal/agents"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/install"
	"saahil.local/brain/internal/rpc"
)

func init() {
	Register(Check{Name: "mesh.peer", Run: checkPeer})
	Register(Check{Name: "mesh.inbox-hook", Run: checkInboxHook})
	Register(Check{Name: "mesh.sessions", Run: checkSessions})
	Register(Check{Name: "brain.skeleton", Run: checkSkeleton})
	Register(Check{Name: "sync.last", Run: checkSync})
	Register(Check{Name: "sync.conflicts", Run: checkConflicts})
}

func checkPeer(ctx context.Context) (Status, string) {
	peer, err := rpc.PeerFor(config.ThisMachine().Other())
	if err != nil {
		return Fail, err.Error()
	}
	resp, err := peer.Call(ctx, rpc.Request{Op: "status"})
	if err != nil {
		return Fail, err.Error()
	}
	st, err := rpc.Decode[rpc.Status](resp)
	if err != nil {
		return Fail, err.Error()
	}
	if st.Version != rpc.Version {
		return Warn, fmt.Sprintf("%s runs %s, this machine %s (publish + brain peer step update)", st.Machine, st.Version, rpc.Version)
	}
	return OK, fmt.Sprintf("%s reachable, same version %s", st.Machine, st.Version)
}

func checkInboxHook(context.Context) (Status, string) {
	raw, err := os.ReadFile(filepath.Join(config.ClaudeDir(), "settings.json"))
	if err != nil {
		return Fail, err.Error()
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return Fail, "settings.json: " + err.Error()
	}
	var missing []string
	for _, h := range install.MeshHooks() {
		found := false
		for _, group := range settings.Hooks[h.Event] {
			for _, e := range group.Hooks {
				found = found || e.Command == h.Command
			}
		}
		if !found {
			missing = append(missing, h.Event)
		}
	}
	if len(missing) > 0 {
		return Fail, "inbox hook missing on " + strings.Join(missing, ", ") + " (brain install)"
	}
	return OK, "PostToolUse, UserPromptSubmit, Stop"
}

func checkSessions(context.Context) (Status, string) {
	sessions, err := agents.LocalSessions(agents.RegistryDir())
	if err != nil {
		return Fail, err.Error()
	}
	return OK, fmt.Sprintf("%d live sessions on this machine", len(sessions))
}

func checkSkeleton(context.Context) (Status, string) {
	root := config.BrainRoot()
	var missing []string
	for _, d := range []string{"work", "notes", "hubs", "memory", "sessions", "journal", "inbox", ".index", ".archive"} {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		return Fail, "missing in " + root + ": " + strings.Join(missing, " ") + " (brain init)"
	}
	return OK, root
}

// checkSync reads the laptop's last Unison run (through the bridge from the devbox): < 70 min old and clean.
func checkSync(ctx context.Context) (Status, string) {
	var raw []byte
	if config.ThisMachine() == config.Laptop {
		var err error
		if raw, err = os.ReadFile(filepath.Join(config.StateDir(), "sync-last.json")); err != nil {
			return Fail, "no sync has run yet"
		}
	} else {
		peer, err := rpc.PeerFor(config.Laptop)
		if err != nil {
			return Fail, err.Error()
		}
		resp, err := peer.Call(ctx, rpc.Request{Op: "sync-status"})
		if err != nil {
			return Fail, err.Error()
		}
		if !resp.OK {
			return Fail, resp.Error
		}
		raw = resp.Result
	}
	var rec struct {
		Ended    time.Time `json:"ended"`
		ExitCode int       `json:"exitCode"`
		Summary  string    `json:"summary"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Fail, err.Error()
	}
	age := time.Since(rec.Ended).Round(time.Minute)
	detail := fmt.Sprintf("last sync %s ago, exit %d: %s", age, rec.ExitCode, rec.Summary)
	if rec.ExitCode >= 2 {
		return Fail, detail
	}
	if age > 70*time.Minute {
		return Fail, detail
	}
	return OK, detail
}

// checkConflicts counts sync conflict copies left in the brain (Unison's and the cut-over reconcile's).
func checkConflicts(context.Context) (Status, string) {
	var found []string
	root := config.BrainRoot()
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".index" || d.Name() == ".data") {
			return filepath.SkipDir
		}
		if !d.IsDir() && config.IsSyncConflictCopy(d.Name()) {
			found = append(found, strings.TrimPrefix(p, root+"/"))
		}
		return nil
	})
	if len(found) == 0 {
		return OK, "0 conflict copies in the brain"
	}
	return Fail, fmt.Sprintf("%d conflict copies: %s", len(found), strings.Join(found[:min(len(found), 5)], ", "))
}
