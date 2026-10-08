package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/session"
)

func init() {
	register("session note", "write the session note for a transcript: brain session note <transcript.jsonl>", cmdSessionNote)
	register("session backfill", "session notes for every transcript on this machine [--machine] [--since DATE] [--workers N]", cmdSessionBackfill)
	register("hook end", "SessionEnd hook: write this session's note in the background", cmdHookCapture)
	register("hook compact", "PostCompact hook: refresh this session's note in the background", cmdHookCapture)
}

func sessionFlags(name string) (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	machine := fs.String("machine", string(config.ThisMachine()), "sessions/<machine>/ to write under: devbox | laptop")
	force := fs.Bool("force", false, "rewrite even when the transcript is unchanged")
	return fs, machine, force
}

func sessionOptions(machine string, force bool) (session.Options, error) {
	if machine != string(config.Devbox) && machine != string(config.Laptop) {
		return session.Options{}, fmt.Errorf("--machine must be devbox or laptop, not %q", machine)
	}
	return session.Options{BrainRoot: config.BrainRoot(), Machine: config.Machine(machine), Force: force}, nil
}

func cmdSessionNote(args []string) int {
	fs, machine, force := sessionFlags("session note")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return fail("session note <transcript.jsonl> [--machine devbox|laptop] [--force]")
	}
	opt, err := sessionOptions(*machine, *force)
	if err != nil {
		return fail("%v", err)
	}
	res, err := session.Note(fs.Arg(0), opt)
	if err != nil {
		return fail("%v", err)
	}
	if res.Status == session.Skipped {
		fmt.Fprintf(os.Stderr, "skipped (%s): %s\n", res.Reason, fs.Arg(0))
		return 0
	}
	fmt.Println(res.Path)
	return 0
}

func cmdSessionBackfill(args []string) int {
	fs, machine, force := sessionFlags("session backfill")
	since := fs.String("since", "", "only transcripts modified on or after this date (YYYY-MM-DD, UTC)")
	workers := fs.Int("workers", 8, "transcripts parsed at once")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 0 || *workers < 1 || *workers > 64 {
		return fail("session backfill [--machine devbox|laptop] [--since YYYY-MM-DD] [--workers 1..64] [--force]")
	}
	opt, err := sessionOptions(*machine, *force)
	if err != nil {
		return fail("%v", err)
	}
	bopt := session.BackfillOptions{
		Options:     opt,
		ProjectsDir: filepath.Join(config.ClaudeDir(), "projects"),
		Workers:     *workers,
		OnError:     func(path string, err error) { fmt.Fprintf(os.Stderr, "brain: %s: %v\n", path, err) },
	}
	if *since != "" {
		if bopt.Since, err = time.Parse("2006-01-02", *since); err != nil {
			return fail("--since: %v", err)
		}
	}
	c, err := session.Backfill(bopt)
	if err != nil {
		return fail("%v", err)
	}
	skipped := 0
	reasons := make([]string, 0, len(c.Skipped))
	for reason, n := range c.Skipped {
		skipped += n
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	fmt.Printf("transcripts        %d\n", c.Transcripts)
	fmt.Printf("notes written      %d\n", c.Written)
	fmt.Printf("unchanged          %d\n", c.Unchanged)
	fmt.Printf("skipped            %d\n", skipped)
	for _, reason := range reasons {
		fmt.Printf("  %-16s %d\n", reason, c.Skipped[reason])
	}
	if c.Older > 0 {
		fmt.Printf("older than --since %d (not counted)\n", c.Older)
	}
	fmt.Printf("subagents folded   %d\n", c.Subagents)
	fmt.Printf("lines over 8 MB    %d (skipped; the rest of each transcript was read)\n", c.SkippedLines)
	fmt.Printf("took               %s\n", c.Took.Round(time.Millisecond))
	return 0
}

// cmdHookCapture serves SessionEnd and PostCompact: it must return within the 300 ms hook budget, so it only starts
// `brain session note` in its own session (Setsid, so it outlives Claude Code) and fails open on any problem.
func cmdHookCapture([]string) int {
	var in struct {
		TranscriptPath string `json:"transcript_path"`
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in) != nil || in.TranscriptPath == "" {
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		return 0
	}
	cmd := exec.Command(exe, "session", "note", in.TranscriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if os.MkdirAll(config.StateDir(), 0o700) == nil {
		if log, err := os.OpenFile(filepath.Join(config.StateDir(), "session-notes.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			defer log.Close()
			cmd.Stdout, cmd.Stderr = log, log
		}
	}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
	return 0
}
