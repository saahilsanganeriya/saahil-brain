package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"saahil.local/brain/internal/bridge"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/rpc"
)

func init() {
	register("sync", "two-way sync of ~/brain between the machines (Unison, laptop-initiated; conflicts kept as copies)", cmdSync)
	register("sync status", "when the last sync ran and how it ended", cmdSyncStatus)
}

// Never synced: each machine's own index, bulk outputs and history (.archive: cut-over manifests of ~1 GB, rewrite
// backups, dream before-copies), git history (devbox only), locks, temp files.
// work/ carries what the old hourly rsync carried — text (md txt py sh js toml yml yaml and small extensionless files)
// up to 2 MB — never the bulk: the boxes hold ~600 GB on the devbox and ~220 GB on the laptop.
var syncIgnores = append(prefixed("Name ", config.SyncBulkDirs),
	`Regex work/.*\.[A-Za-z0-9]+`,
	"Path .index", "Path .data", "Name .git", "Path .archive", "Path .brain/dream.lock",
	"Path .brain/dream-state.json", "Name .consolidate-lock", "Name .DS_Store", "Name *.tmp", "Name .brain-tmp",
	"Name *.db", "Name *.db-wal", "Name *.db-shm",
)

func prefixed(prefix string, items []string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = prefix + it
	}
	return out
}

type syncRecord struct {
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended"`
	ExitCode int       `json:"exitCode"`
	Summary  string    `json:"summary"`
}

func syncRecordPath() string { return filepath.Join(config.StateDir(), "sync-last.json") }

// workText un-ignores the text types inside work/ that the extension rule above ignores.
var workText = `Regex work/.*\.(` + strings.Join(config.SyncWorkTextExts, "|") + `)`

func unisonArgs(root string) []string {
	args := []string{root, "ssh://" + config.DevboxHost + "/" + root,
		"-batch", "-auto", "-times", "-perms", "0", "-copyonconflict", "-prefer", "newer",
		"-servercmd", "/usr/bin/unison", "-sshargs", "-o BatchMode=yes -o ControlPath=none -o ConnectTimeout=10",
		"-logfile", filepath.Join(config.StateDir(), "unison.log")}
	for _, ig := range syncIgnores {
		args = append(args, "-ignore", ig)
	}
	return append(args, "-ignorenot", workText, "-maxsizethreshold", "2048")
}

func cmdSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "list what would change, change nothing")
	setup := fs.Bool("setup", false, "laptop: install unison (Homebrew) if missing")
	enable := fs.Bool("enable", false, "laptop: load the hourly sync LaunchAgent")
	archive := fs.Bool("archive-conflicts", false, "move sync conflict copies out of the live tree into .archive/conflicts/<machine>/ (newer stays live)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *archive {
		n, err := archiveConflicts(config.BrainRoot(), time.Now())
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("archived %d conflict copies into .archive/conflicts/%s/\n", n, config.ThisMachine())
		return 0
	}
	if *setup || *enable {
		if config.ThisMachine() != config.Laptop {
			return fail("--setup/--enable run on the laptop (brain peer step sync-setup|sync-enable)")
		}
		if err := bridge.EnsureUnison(); err != nil {
			return fail("%v", err)
		}
		if *enable {
			if err := bridge.EnableSync(); err != nil {
				return fail("%v", err)
			}
		}
		return 0
	}
	if config.ThisMachine() == config.Devbox {
		// the laptop has no sshd: ask it to run the sync through the bridge
		peer, err := rpc.PeerFor(config.Laptop)
		if err != nil {
			return fail("%v", err)
		}
		resp, err := peer.Call(context.Background(), rpc.Request{Op: "step", Step: "sync"})
		if err != nil {
			return fail("laptop unreachable, sync queued for its hourly run: %v", err)
		}
		job, err := rpc.Decode[rpc.Job](resp)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Println("sync started on the laptop as job", job.ID, "(brain peer job", job.ID+")")
		return 0
	}
	unison, err := exec.LookPath("unison")
	if err != nil {
		return fail("unison not installed on the laptop (brew install unison)")
	}
	root := config.BrainRoot()
	_ = os.MkdirAll(config.StateDir(), 0o700)
	uargs := unisonArgs(root)
	if *dry {
		// -noupdate alone still CREATES new files (learned 2026-10-08); block creation, update and deletion on
		// both replicas so unison only prints its plan
		for _, root := range []string{uargs[0], uargs[1]} {
			uargs = append(uargs, "-nocreation", root, "-noupdate", root, "-nodeletion", root)
		}
	}
	rec := syncRecord{Started: time.Now().UTC()}
	cmd := exec.Command(unison, uargs...)
	out, _ := cmd.CombinedOutput()
	rec.Ended, rec.ExitCode = time.Now().UTC(), cmd.ProcessState.ExitCode()
	// exit 2 is a file that failed to transfer, usually one a live agent wrote during the run: one more pass carries it
	if rec.ExitCode == 2 && !*dry {
		fmt.Println("unison exit 2 (a file failed to transfer, often one written during the run): one more pass")
		cmd = exec.Command(unison, uargs...)
		out, _ = cmd.CombinedOutput()
		rec.Ended, rec.ExitCode = time.Now().UTC(), cmd.ProcessState.ExitCode()
	}
	_ = os.WriteFile(filepath.Join(config.StateDir(), "unison-last.out"), out, 0o600)
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r", "\n")), "\n")
	rec.Summary = lines[len(lines)-1]
	// the full run is in unison-last.out; print the summary, not one line per file
	skipped := 0
	kinds := map[string]int{}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		// with -prefer newer unison settles a both-changed file itself (the loser is copied), so "<=?=>" marks only
		// items it skipped, and those are counted below by their reason
		switch {
		case strings.Contains(t, "---->"):
			kinds["laptop → devbox"]++
		case strings.Contains(t, "<----"):
			kinds["devbox → laptop"]++
		}
		switch {
		case strings.HasPrefix(t, "skipped:"):
			skipped++
			reason := "other"
			if i := strings.LastIndex(t, "("); i > 0 {
				reason = strings.TrimSuffix(t[i+1:], ")")
			}
			kinds["skipped: "+reason]++
		case strings.Contains(t, "will be synced") || strings.Contains(t, "to be synced") ||
			strings.HasPrefix(t, "Synchronization") || strings.Contains(t, "items transferred") ||
			strings.HasPrefix(t, "Nothing to do") || strings.Contains(t, "conflict"):
			fmt.Println(t)
		}
	}
	for k, v := range kinds {
		fmt.Printf("%-24s %d\n", k, v)
	}
	fmt.Printf("%d per-file skip lines (full output: %s)\n", skipped, filepath.Join(config.StateDir(), "unison-last.out"))
	if !*dry {
		if n, err := archiveStaleTemps(root, out, rec.Started); n > 0 || err != nil {
			fmt.Printf("moved %d stale unison half-copies into .archive/conflicts (err %v)\n", n, err)
		}
		raw, _ := json.Marshal(rec)
		_ = os.WriteFile(syncRecordPath(), raw, 0o600)
		// each machine keeps its own index (.index never syncs): fold in what the sync just brought
		if code := cmdIndex(nil); code != 0 {
			return code
		}
		refreshView()
	}
	// unison: 0 all done, 1 some files skipped, 2 non-fatal failures, 3 fatal
	if rec.ExitCode >= 2 {
		return 1
	}
	return 0
}

func cmdSyncStatus([]string) int {
	if config.ThisMachine() == config.Devbox {
		peer, err := rpc.PeerFor(config.Laptop)
		if err != nil {
			return fail("%v", err)
		}
		resp, err := peer.Call(context.Background(), rpc.Request{Op: "sync-status"})
		if err != nil {
			return fail("%v", err)
		}
		return printJSON(resp)
	}
	raw, err := os.ReadFile(syncRecordPath())
	if err != nil {
		return fail("no sync has run on this laptop yet")
	}
	fmt.Println(string(raw))
	return 0
}

// archiveConflicts handles sync conflicts the way the brain handles every replaced version: the newer file stays live,
// the losing copy moves into .archive/conflicts/<machine>/<same relative path>, and a log line records it.
func archiveConflicts(root string, now time.Time) (int, error) {
	dest := filepath.Join(root, ".archive", "conflicts", string(config.ThisMachine()))
	var moves [][2]string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && (d.Name() == ".git" || d.Name() == ".index" || d.Name() == ".data" || d.Name() == ".archive") {
			return filepath.SkipDir
		}
		if !d.IsDir() && config.IsSyncConflictCopy(d.Name()) {
			rel, _ := filepath.Rel(root, p)
			moves = append(moves, [2]string{p, filepath.Join(dest, rel)})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return archiveMoves(dest, root, moves, now)
}

// archiveMoves renames each file into the archive (same disk) and appends what moved to its log.
func archiveMoves(dest, root string, moves [][2]string, now time.Time) (int, error) {
	var log strings.Builder
	for _, m := range moves {
		if err := os.MkdirAll(filepath.Dir(m[1]), 0o755); err != nil {
			return 0, err
		}
		if err := os.Rename(m[0], m[1]); err != nil {
			return 0, err
		}
		rel, _ := filepath.Rel(root, m[0])
		fmt.Fprintf(&log, "%s\t%s\n", now.UTC().Format(time.RFC3339), rel)
	}
	if len(moves) > 0 {
		f, err := os.OpenFile(filepath.Join(dest, "log.tsv"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return len(moves), err
		}
		defer f.Close()
		_, _ = f.WriteString(log.String())
	}
	return len(moves), nil
}

// archiveStaleTemps moves unison's half-copies (.unison.<name>.<hash>.unison.tmp) that predate this run from the
// folders of files that failed to transfer (an aborted transfer of a file a live agent was writing leaves them), and
// any half-copy past config.UnisonTempGrace anywhere in the synced tree.
func archiveStaleTemps(root string, unisonOut []byte, started time.Time) (int, error) {
	dest := filepath.Join(root, ".archive", "conflicts", string(config.ThisMachine()))
	dirs := map[string]bool{}
	for _, line := range strings.Split(string(unisonOut), "\n") {
		if rel, ok := strings.CutPrefix(strings.TrimSpace(line), "failed: "); ok {
			dirs[filepath.Dir(filepath.Clean(rel))] = true
		}
	}
	var moves [][2]string
	for dir := range dirs {
		entries, _ := os.ReadDir(filepath.Join(root, dir))
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !strings.HasPrefix(e.Name(), ".unison.") || !info.ModTime().Before(started) {
				continue
			}
			moves = append(moves, [2]string{filepath.Join(root, dir, e.Name()), filepath.Join(dest, dir, e.Name())})
		}
	}
	// half-copies an earlier run abandoned, wherever they are: past the grace no transfer is still writing them
	queued := map[string]bool{}
	for _, m := range moves {
		queued[m[0]] = true
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && (d.Name() == ".git" || d.Name() == ".index" || d.Name() == ".data" || d.Name() == ".archive" ||
			config.IsSyncBulkDir(d.Name())) {
			return filepath.SkipDir
		}
		if info, err := d.Info(); err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), ".unison.") && !queued[p] &&
			time.Since(info.ModTime()) > config.UnisonTempGrace {
			rel, _ := filepath.Rel(root, p)
			moves = append(moves, [2]string{p, filepath.Join(dest, rel)})
		}
		return nil
	})
	return archiveMoves(dest, root, moves, time.Now())
}
