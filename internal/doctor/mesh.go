package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	Register(Check{Name: "sync.conflicts", Run: checkConflicts, Timeout: 2 * time.Minute}) // walks all of ~/brain
	Register(Check{Name: "brain.work-root", Run: checkWorkRoot})
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
		Agent    string    `json:"agent"`    // the laptop's bridge adds it
		Reloaded string    `json:"reloaded"` // the same
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Fail, err.Error()
	}
	if config.ThisMachine() == config.Laptop {
		rec.Reloaded = rpc.LastSyncAgentReload(time.Now())
	}
	age := time.Since(rec.Ended).Round(time.Minute)
	detail := fmt.Sprintf("last sync %s ago, exit %d: %s", age, rec.ExitCode, rec.Summary)
	if rec.ExitCode < 2 && age <= 70*time.Minute {
		if rec.Reloaded != "" {
			return Warn, detail + "; " + rec.Reloaded
		}
		return OK, detail
	}
	if rec.Agent == "" && config.ThisMachine() == config.Laptop {
		rec.Agent = rpc.SyncAgentState(ctx)
	}
	for _, part := range []string{rec.Agent, rec.Reloaded} {
		if part != "" {
			detail += "; " + part
		}
	}
	return Fail, detail
}

// checkConflicts counts sync conflict copies left in the brain (Unison's and the cut-over reconcile's).
func checkConflicts(context.Context) (Status, string) {
	root := config.BrainRoot()
	found, inFlight := conflictCopies(root, conflictWalkers)
	if len(found) == 0 {
		detail := "0 unhandled conflict copies in the brain (losers are kept in .archive/conflicts)"
		if inFlight > 0 {
			detail += fmt.Sprintf("; %d unison half-copies under 2 h old (in flight)", inFlight)
		}
		return OK, detail
	}
	return Fail, fmt.Sprintf("%d conflict copies: %s", len(found), strings.Join(found[:min(len(found), 5)], ", "))
}

// conflictWalkers bounds the folders read at once: one serial walk of work/ (2.24 M entries on the devbox) outran the
// check's 2 minutes.
const conflictWalkers = 8

// conflictWalkForked is called when a folder is read on its own goroutine; tests count it.
var conflictWalkForked = func() {}

// conflictCopies lists the unhandled sync conflict copies below root in the serial walk's order, reading folders on
// up to workers goroutines (0: serially), and counts unison's half-copies young enough to be in flight.
func conflictCopies(root string, workers int) ([]string, int) {
	var (
		mu       sync.Mutex
		found    []string
		inFlight int
		sem      = make(chan struct{}, workers)
		walk     func(dir string)
	)
	walk = func(dir string) {
		list, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var wg sync.WaitGroup
		for _, d := range list {
			p := filepath.Join(dir, d.Name())
			if d.IsDir() {
				// .archive holds the losers already handled (brain sync --archive-conflicts); only live copies are unhandled
				if d.Name() == ".git" || d.Name() == ".index" || d.Name() == ".data" || d.Name() == ".archive" ||
					config.IsSyncBulkDir(d.Name()) {
					continue
				}
				select {
				case sem <- struct{}{}:
					wg.Add(1)
					go func() {
						defer func() { <-sem; wg.Done() }()
						conflictWalkForked()
						walk(p)
					}()
				default:
					walk(p)
				}
				continue
			}
			if !config.IsSyncConflictCopy(d.Name()) {
				continue
			}
			mu.Lock()
			// unison's half-copy of a file it is moving now, or just failed to: the next sync retries and cleans it
			if info, err := d.Info(); err == nil && strings.HasPrefix(d.Name(), ".unison.") && time.Since(info.ModTime()) < config.UnisonTempGrace {
				inFlight++
			} else {
				found = append(found, strings.TrimPrefix(p, root+"/"))
			}
			mu.Unlock()
		}
		wg.Wait()
	}
	walk(root)
	slices.SortFunc(found, func(a, b string) int { return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/")) })
	return found, inFlight
}

// checkWorkRoot guards work/'s top level: a .git there makes every git hook treat the brain as a worktree (and link
// saahil-docs into it), and a link resolving back into the brain is a loop for every tool that follows links.
func checkWorkRoot(context.Context) (Status, string) {
	work := filepath.Join(config.BrainRoot(), "work")
	entries, err := os.ReadDir(work)
	if err != nil {
		return Fail, err.Error()
	}
	brainReal, err := filepath.EvalSymlinks(config.BrainRoot())
	if err != nil {
		return Fail, err.Error()
	}
	var bad []string
	for _, e := range entries {
		p := filepath.Join(work, e.Name())
		if e.Name() == ".git" {
			bad = append(bad, "work/.git (git hooks would treat the brain as a worktree)")
			continue
		}
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && (real == brainReal || strings.HasPrefix(real, brainReal+"/")) {
			bad = append(bad, "work/"+e.Name()+" links back into the brain")
		}
	}
	if len(bad) == 0 {
		return OK, fmt.Sprintf("%d entries in work/, no .git and no link back into the brain", len(entries))
	}
	return Fail, strings.Join(bad, "; ")
}
