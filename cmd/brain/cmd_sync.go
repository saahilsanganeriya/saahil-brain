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
// work/ stays out until the cut-over's sync step (C12) replaces this with bulk excludes: the devbox box tree is
// 528 GB and must never stream to the laptop.
var syncIgnores = []string{
	"Path work",
	"Path .index", "Path .data", "Path .git", "Path .archive", "Path .brain/dream.lock",
	"Path .brain/dream-state.json", "Name .consolidate-lock", "Name .DS_Store", "Name *.tmp", "Name .brain-tmp",
	"Name *.db", "Name *.db-wal", "Name *.db-shm", "Name node_modules", "Name __pycache__", "Name .venv", "Name venv",
}

type syncRecord struct {
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended"`
	ExitCode int       `json:"exitCode"`
	Summary  string    `json:"summary"`
}

func syncRecordPath() string { return filepath.Join(config.StateDir(), "sync-last.json") }

func unisonArgs(root string) []string {
	args := []string{root, "ssh://" + config.DevboxHost + "/" + root,
		"-batch", "-auto", "-times", "-perms", "0", "-copyonconflict", "-prefer", "newer",
		"-servercmd", "/usr/bin/unison", "-sshargs", "-o BatchMode=yes -o ControlPath=none -o ConnectTimeout=10",
		"-logfile", filepath.Join(config.StateDir(), "unison.log")}
	for _, ig := range syncIgnores {
		args = append(args, "-ignore", ig)
	}
	return args
}

func cmdSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "list what would change, change nothing")
	setup := fs.Bool("setup", false, "laptop: install unison (Homebrew) if missing")
	enable := fs.Bool("enable", false, "laptop: load the hourly sync LaunchAgent")
	if err := fs.Parse(args); err != nil {
		return 2
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
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	rec.Summary = lines[len(lines)-1]
	fmt.Println(strings.TrimSpace(string(out)))
	if !*dry {
		raw, _ := json.Marshal(rec)
		_ = os.WriteFile(syncRecordPath(), raw, 0o600)
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
