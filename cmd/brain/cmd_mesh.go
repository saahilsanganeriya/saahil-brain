package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"saahil.local/brain/internal/agents"
	"saahil.local/brain/internal/bridge"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/rpc"
)

func init() {
	register("version", "print the version and machine", cmdVersion)
	register("rpc", "answer one request on stdin (used by the other machine over ssh)", cmdRPC)
	register("_step-run", "run one allowed step as a job (internal)", cmdStepRun)
	register("bridge serve", "laptop: serve the bridge on 127.0.0.1:7722 (launchd runs this)", cmdBridgeServe)
	register("bridge install", "laptop: install the bridge + tunnel LaunchAgents and check them", cmdBridgeInstall)
	register("self-update", "laptop: pull the newest binary from the devbox and restart the bridge", cmdSelfUpdate)
	register("peer status", "status of the other machine", peerOp("status"))
	register("peer step", "run an allowed step on the other machine: brain peer step <name> [--wait]", cmdPeerStep)
	register("peer job", "a step's state and log tail on the other machine: brain peer job <id>", cmdPeerJob)
	register("agents list", "live sessions on both machines as <machine>:<name>", cmdAgentsList)
	register("agents note", "message a session on either machine: brain agents note <machine>:<name> <text…>", cmdAgentsNote)
	register("hook inbox", "deliver queued notes into this session (Claude Code / Codex hook)", cmdHookInbox)
}

func cmdVersion([]string) int {
	fmt.Printf("brain %s (%s)\n", rpc.Version, config.ThisMachine())
	return 0
}

func cmdRPC([]string) int {
	var req rpc.Request
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&req); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(rpc.Response{Error: err.Error()})
		return 1
	}
	_ = json.NewEncoder(os.Stdout).Encode(rpc.NewHandler().Handle(context.Background(), req))
	return 0
}

func cmdStepRun(args []string) int {
	if len(args) != 2 {
		return fail("_step-run <job-id> <step>")
	}
	return rpc.NewSteps().RunJob(args[0], args[1])
}

func cmdBridgeServe([]string) int {
	if config.ThisMachine() != config.Laptop {
		return fail("the bridge runs on the laptop; the devbox is reached over ssh")
	}
	token, err := os.ReadFile(config.BridgeTokenPath())
	if err != nil || len(strings.TrimSpace(string(token))) < 32 {
		return fail("bridge token missing or short at %s", config.BridgeTokenPath())
	}
	srv := &rpc.Server{Handler: rpc.NewHandler(), Token: strings.TrimSpace(string(token)),
		LogPath: config.StateDir() + "/bridge.log"}
	_ = os.MkdirAll(config.StateDir(), 0o700)
	fmt.Fprintf(os.Stderr, "brain bridge %s listening on 127.0.0.1:%d\n", rpc.Version, config.BridgePort)
	go bridge.WatchSyncAgent(context.Background())
	return fail("%v", srv.ListenAndServe(config.BridgePort))
}

func cmdBridgeInstall([]string) int {
	if err := bridge.Install(); err != nil {
		return fail("%v", err)
	}
	return 0
}

func cmdSelfUpdate([]string) int {
	if err := bridge.SelfUpdate(); err != nil {
		return fail("%v", err)
	}
	return 0
}

func otherPeer() (rpc.Peer, error) { return rpc.PeerFor(config.ThisMachine().Other()) }

func peerOp(op string) func([]string) int {
	return func([]string) int {
		peer, err := otherPeer()
		if err != nil {
			return fail("%v", err)
		}
		resp, err := peer.Call(context.Background(), rpc.Request{Op: op})
		if err != nil {
			return fail("%v", err)
		}
		return printJSON(resp)
	}
}

func cmdPeerStep(args []string) int {
	fs := flag.NewFlagSet("peer step", flag.ContinueOnError)
	wait := fs.Duration("wait", 0, "wait this long for the step to finish")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() != 1 {
		return fail("peer step <name> [--wait 5m]")
	}
	peer, err := otherPeer()
	if err != nil {
		return fail("%v", err)
	}
	deadline := time.Now().Add(*wait)
	job, err := startStep(context.Background(), peer, fs.Arg(0), deadline, time.Now, time.Sleep)
	if err != nil {
		return fail("%v", err)
	}
	for job.State == "running" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if resp, err := peer.Call(context.Background(), rpc.Request{Op: "job", ID: job.ID}); err == nil {
			if j, err := rpc.Decode[rpc.Job](resp); err == nil {
				job = j
			}
		}
	}
	out, _ := json.MarshalIndent(job, "", "  ")
	fmt.Println(string(out))
	if job.State == "failed" {
		return 1
	}
	return 0
}

const peerRetry = 10 * time.Second

// startStep sends a step once the other machine answers: a laptop that sleeps or changes networks comes back within
// minutes, so --wait covers reaching it too. Only the read-only status op is retried, so a step never starts twice.
func startStep(ctx context.Context, peer rpc.Peer, step string, deadline time.Time, now func() time.Time,
	sleep func(time.Duration)) (rpc.Job, error) {
	for {
		_, err := peer.Call(ctx, rpc.Request{Op: "status"})
		if err == nil {
			break
		}
		if !errors.Is(err, rpc.ErrUnreachable) || !now().Before(deadline) {
			return rpc.Job{}, err
		}
		sleep(peerRetry)
	}
	resp, err := peer.Call(ctx, rpc.Request{Op: "step", Step: step})
	if err != nil {
		return rpc.Job{}, err
	}
	return rpc.Decode[rpc.Job](resp)
}

func cmdPeerJob(args []string) int {
	if len(args) != 1 {
		return fail("peer job <id>")
	}
	peer, err := otherPeer()
	if err != nil {
		return fail("%v", err)
	}
	resp, err := peer.Call(context.Background(), rpc.Request{Op: "job", ID: args[0]})
	if err != nil {
		return fail("%v", err)
	}
	return printJSON(resp)
}

func cmdAgentsList(args []string) int {
	fs := flag.NewFlagSet("agents list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	localOnly := fs.Bool("local", false, "this machine only")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	h := rpc.NewHandler()
	local, err := rpc.Decode[[]agents.Session](h.Handle(ctx, rpc.Request{Op: "sessions"}))
	if err != nil {
		return fail("%v", err)
	}
	all := local
	var remoteErr error
	if !*localOnly {
		var peer rpc.Peer
		if peer, remoteErr = otherPeer(); remoteErr == nil {
			var resp rpc.Response
			if resp, remoteErr = peer.Call(ctx, rpc.Request{Op: "sessions"}); remoteErr == nil {
				var remote []agents.Session
				if remote, remoteErr = rpc.Decode[[]agents.Session](resp); remoteErr == nil {
					all = append(all, remote...)
				}
			}
		}
	}
	if *asJSON {
		out, _ := json.MarshalIndent(map[string]any{"sessions": all, "remoteError": errString(remoteErr)}, "", "  ")
		fmt.Println(string(out))
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ADDRESS\tSTATUS\tSESSION\tCWD")
	for _, s := range all {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Address(), s.Status, agents.Short(s.SessionID), s.CWD)
	}
	tw.Flush()
	if remoteErr != nil {
		fmt.Printf("\n%s: unreachable (%v)\n", config.ThisMachine().Other(), remoteErr)
	}
	return 0
}

// selfAddress is this session's own address, so the receiver knows whom to answer.
func selfAddress() string {
	machine := string(config.ThisMachine())
	if id := os.Getenv("CLAUDE_CODE_SESSION_ID"); id != "" {
		if s, ok := agents.SessionByID(id); ok {
			return s.Address()
		}
		return machine + ":" + agents.Short(id)
	}
	return machine + ":owner"
}

func cmdAgentsNote(args []string) int {
	fs := flag.NewFlagSet("agents note", flag.ContinueOnError)
	wait := fs.Duration("wait", 90*time.Second, "how long to wait for the delivery receipt")
	from := fs.String("from", "", "sender address (default: this session)")
	if err := fs.Parse(reorder(args)); err != nil || fs.NArg() < 2 {
		return fail("agents note <machine>:<name> <text…> [--wait 90s]")
	}
	target, text := fs.Arg(0), strings.Join(fs.Args()[1:], " ")
	if text == "-" {
		raw, _ := io.ReadAll(bufio.NewReader(os.Stdin))
		text = string(raw)
	}
	sender := *from
	if sender == "" {
		sender = selfAddress()
	}
	machine := config.ThisMachine()
	if m, _, ok := strings.Cut(target, ":"); ok && (m == string(config.Laptop) || m == string(config.Devbox)) {
		machine = config.Machine(m)
	}
	call := func(req rpc.Request) (rpc.Response, error) {
		if machine == config.ThisMachine() {
			return rpc.NewHandler().Handle(context.Background(), req), nil
		}
		peer, err := rpc.PeerFor(machine)
		if err != nil {
			return rpc.Response{}, err
		}
		return peer.Call(context.Background(), req)
	}
	resp, err := call(rpc.Request{Op: "note", To: target, From: sender, Text: text})
	if err != nil {
		if errors.Is(err, rpc.ErrUnreachable) {
			return fail("NOT SENT: %s is unreachable (%v)", machine, err)
		}
		return fail("%v", err)
	}
	res, err := rpc.Decode[rpc.NoteResult](resp)
	if err != nil {
		return fail("%v", err)
	}
	deadline := time.Now().Add(*wait)
	for {
		if resp, err := call(rpc.Request{Op: "receipt", SessionID: res.SessionID, ID: res.ID}); err == nil {
			if r, err := rpc.Decode[*agents.Receipt](resp); err == nil && r != nil {
				fmt.Printf("DELIVERED to %s at %s via %s (note %s)\n", res.Target, r.DeliveredAt.Format(time.RFC3339), r.Event, res.ID)
				return 0
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	how := "its inbox hook takes it at its next tool call, prompt or turn end"
	if res.Nudged {
		how = "its idle Orca terminal was nudged"
	} else if res.NudgeErr != "" {
		how += " (Orca nudge failed: " + res.NudgeErr + ")"
	}
	fmt.Printf("QUEUED for %s (note %s): %s\n", res.Target, res.ID, how)
	return 3
}

type hookInput struct {
	SessionID string `json:"session_id"`
	Event     string `json:"hook_event_name"`
	AgentID   string `json:"agent_id"`
}

// cmdHookInbox must stay fast and fail open: any problem prints nothing and exits 0.
func cmdHookInbox([]string) int {
	var in hookInput
	if json.NewDecoder(io.LimitReader(os.Stdin, 4<<20)).Decode(&in) != nil || in.SessionID == "" || in.AgentID != "" {
		return 0
	}
	if !agents.HasPending(in.SessionID) {
		return 0
	}
	notes, err := agents.Deliver(in.SessionID, in.Event, time.Now())
	if err != nil || len(notes) == 0 {
		return 0
	}
	text := agents.Render(notes)
	var out any
	switch in.Event {
	case "Stop", "SubagentStop":
		out = map[string]any{"decision": "block", "reason": text}
	default:
		out = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": in.Event, "additionalContext": text}}
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
	return 0
}

func printJSON(resp rpc.Response) int {
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	if !resp.OK {
		return 1
	}
	return 0
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// reorder moves flags before positional arguments, so `note devbox:x hello --wait 5s` parses like the stdlib expects.
func reorder(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") && len(a) > 2 {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}
