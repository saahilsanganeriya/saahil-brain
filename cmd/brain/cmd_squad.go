package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"saahil.local/brain/internal/agents"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/squad"
	"saahil.local/brain/internal/machine"
	"saahil.local/brain/internal/rpc"
)

func init() {
	register("agents screen", "an agent's screen, and how many background agents it waits on: <name> [--lines N]", cmdAgentsScreen)
	register("agents attach", "a Happier agent: open this terminal onto it (Ctrl-b d detaches): [name]", cmdAgentsAttach)
	register("agents new", "start a fresh agent in its own Orca worktree: <name> [--base REF] [--dir D] [--repo PATH]", cmdAgentsNew)
	register("agents goal", "set an agent's /goal to a file's FULL text (≤ 4,000 chars), verified: <name> <file> [done]", cmdAgentsGoal)
	register("agents resume", "resume a saved devbox conversation in Orca: <name> <session> [--force]", cmdAgentsResume)
	register("agents move", "move a laptop session to the devbox (exit it first): <session> <name> [--force]", cmdAgentsMove)
	register("agents stop", "end a finished agent, transcript kept: <name> [--rm-worktree]", cmdAgentsStop)
	register("agents box", "copy a work/ box with its bulk to the devbox, never deleting: <box> [--exclude PATH]… [--with-venv]", cmdAgentsBox)
	register("machine login", "renew the devbox sign-ins: [github|aws|gcloud]… (all three by default)", cmdMachineLogin)
	for verb, summary := range map[string]string{
		"up":     "start the EC2 (~1–2 min) and sync laptop → devbox",
		"down":   "sync devbox → laptop and stop the EC2 (refuses while agents run; --force)",
		"status": "instance state, GitHub cert expiry, AWS SSO",
		"sync":   "sync notes and memory both ways (text only, newer wins)",
	} {
		register("machine "+verb, summary, cmdMachineVerb(verb))
	}
}

// squadTarget splits [devbox:|laptop:]<name>; a bare name is a devbox agent, as it always was for devbox-agent.
func squadTarget(arg string) (config.Machine, string) {
	if m, name, ok := strings.Cut(arg, ":"); ok && (m == string(config.Laptop) || m == string(config.Devbox)) {
		return config.Machine(m), name
	}
	return config.Devbox, arg
}

// squadFor drives agents on target from this machine; the devbox reaches laptop agents only through rpc ops.
func squadFor(target config.Machine) (*squad.Squad, error) {
	here := config.ThisMachine()
	host, err := squad.HostFor(here, target, agents.ExecRunner)
	if err != nil {
		return nil, err
	}
	f := squad.New(here, host)
	if host.Remote {
		f.Sessions = func(ctx context.Context) ([]agents.Session, error) {
			peer, err := rpc.PeerFor(target)
			if err != nil {
				return nil, err
			}
			resp, err := peer.Call(ctx, rpc.Request{Op: "sessions"})
			if err != nil {
				return nil, err
			}
			return rpc.Decode[[]agents.Session](resp)
		}
	}
	return f, nil
}

// runSquad parses flags, resolves the target's squad and runs one command; laptop agents seen from the devbox get a
// clear refusal rather than a remote shell.
func runSquad(command string, target config.Machine, run func(context.Context, *squad.Squad) error) int {
	f, err := squadFor(target)
	if errors.Is(err, squad.ErrViaBridge) {
		return fail("agents %s on a laptop agent runs on the laptop: from the devbox the bridge carries only screen", command)
	}
	if err != nil {
		return fail("%v", err)
	}
	if err := run(context.Background(), f); err != nil {
		return fail("%v", err)
	}
	return 0
}

// parseInterleaved lets flags sit before, between or after positional arguments (`stop audio --rm-worktree`), and
// knows which flags are booleans, so `move --force <session> <name>` keeps its order.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional, args = append(positional, args[0]), args[1:]
	}
}

func cmdAgentsScreen(args []string) int {
	fs := flag.NewFlagSet("agents screen", flag.ContinueOnError)
	lines := fs.Int("lines", 40, "lines of screen")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		return fail("agents screen [devbox:|laptop:]<name> [--lines N]")
	}
	target, name := squadTarget(pos[0])
	ctx := context.Background()
	var res squad.ScreenResult
	if f, err := squadFor(target); errors.Is(err, squad.ErrViaBridge) {
		peer, err := rpc.PeerFor(target)
		if err != nil {
			return fail("%v", err)
		}
		resp, err := peer.Call(ctx, rpc.Request{Op: "screen", To: name, Lines: *lines})
		if err != nil {
			return fail("%v", err)
		}
		if res, err = rpc.Decode[squad.ScreenResult](resp); err != nil {
			return fail("%v", err)
		}
	} else if err != nil {
		return fail("%v", err)
	} else if res, err = f.Screen(ctx, name, *lines); err != nil {
		return fail("%v", err)
	}
	fmt.Println(res.Text)
	if res.Waiting > 0 {
		fmt.Printf("\n%s waits on %d background agent(s)\n", name, res.Waiting)
	}
	return 0
}

func cmdAgentsAttach(args []string) int {
	if len(args) > 1 {
		return fail("agents attach [name]")
	}
	target, name := config.Devbox, ""
	if len(args) == 1 {
		target, name = squadTarget(args[0])
	}
	return runSquad("attach", target, func(ctx context.Context, f *squad.Squad) error { return f.Attach(ctx, name) })
}

func cmdAgentsNew(args []string) int {
	fs := flag.NewFlagSet("agents new", flag.ContinueOnError)
	base := fs.String("base", "origin/main", "commit to branch from")
	dir := fs.String("dir", "", "start in this existing devbox worktree instead of a new one")
	repo := fs.String("repo", "", "make the worktree in this repo checkout (default: work_repo in brain identity)")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		return fail("agents new <name> [--base REF] [--dir D] [--repo PATH]")
	}
	target, name := squadTarget(pos[0])
	return runSquad("new", target, func(ctx context.Context, f *squad.Squad) error { return f.NewAgent(ctx, name, *base, *dir, *repo) })
}

func cmdAgentsGoal(args []string) int {
	pos, err := parseInterleaved(flag.NewFlagSet("agents goal", flag.ContinueOnError), args)
	if err != nil || len(pos) < 2 || len(pos) > 3 {
		return fail(`agents goal <name> <goal file> ["done when …"]`)
	}
	target, name := squadTarget(pos[0])
	done := ""
	if len(pos) == 3 {
		done = pos[2]
	}
	return runSquad("goal", target, func(ctx context.Context, f *squad.Squad) error { return f.Goal(ctx, name, pos[1], done) })
}

func cmdAgentsResume(args []string) int {
	fs := flag.NewFlagSet("agents resume", flag.ContinueOnError)
	force := fs.Bool("force", false, "resume even if the transcript was written in the last 2 min")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 2 {
		return fail("agents resume <name> <session> [--force]")
	}
	target, name := squadTarget(pos[0])
	return runSquad("resume", target, func(ctx context.Context, f *squad.Squad) error { return f.Resume(ctx, name, pos[1], *force) })
}

func cmdAgentsMove(args []string) int {
	fs := flag.NewFlagSet("agents move", flag.ContinueOnError)
	force := fs.Bool("force", false, "move even if the transcript was written in the last 2 min")
	noBoxHint := fs.Bool("no-box-hint", false, "")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 2 {
		return fail("agents move <session> <name> [--force]")
	}
	target, name := squadTarget(pos[1])
	return runSquad("move", target, func(ctx context.Context, f *squad.Squad) error {
		return f.Move(ctx, pos[0], name, *force, *noBoxHint)
	})
}

func cmdAgentsStop(args []string) int {
	fs := flag.NewFlagSet("agents stop", flag.ContinueOnError)
	rm := fs.Bool("rm-worktree", false, "also remove its worktree, only if it is clean")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		return fail("agents stop <name> [--rm-worktree]")
	}
	target, name := squadTarget(pos[0])
	return runSquad("stop", target, func(ctx context.Context, f *squad.Squad) error { return f.Stop(ctx, name, *rm) })
}

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

func cmdAgentsBox(args []string) int {
	fs := flag.NewFlagSet("agents box", flag.ContinueOnError)
	var excludes repeated
	fs.Var(&excludes, "exclude", "skip this path inside the box (repeatable)")
	withVenv := fs.Bool("with-venv", false, "copy venv folders too (macOS venvs will not run on Linux)")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		return fail("agents box <box> [--exclude PATH]… [--with-venv]")
	}
	return runSquad("box", config.Devbox, func(ctx context.Context, f *squad.Squad) error { return f.Box(ctx, pos[0], excludes, *withVenv) })
}

func devboxMachine() (*machine.Machine, error) {
	host, err := squad.HostFor(config.ThisMachine(), config.Devbox, agents.ExecRunner)
	if err != nil {
		return nil, err
	}
	return machine.New(config.ThisMachine(), host), nil
}

func cmdMachineLogin(args []string) int {
	m, err := devboxMachine()
	if err != nil {
		return fail("%v", err)
	}
	if err := m.Login(context.Background(), args); err != nil {
		return fail("%v", err)
	}
	return 0
}

func cmdMachineVerb(verb string) func([]string) int {
	return func(args []string) int {
		m, err := devboxMachine()
		if err != nil {
			return fail("%v", err)
		}
		if err := m.Verb(context.Background(), verb, args); err != nil {
			return fail("%v", err)
		}
		return 0
	}
}
