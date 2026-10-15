// Package rpc is the one request handler both machines answer: the laptop over the token-checked bridge, the devbox
// over the laptop's own ssh. Only the operations listed in Handle exist; there is no shell.
package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"saahil.local/brain/internal/agents"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/squad"
)

type Request struct {
	Op string `json:"op"`
	// note
	To   string `json:"to,omitempty"`
	From string `json:"from,omitempty"`
	Text string `json:"text,omitempty"`
	// receipt, job
	ID        string `json:"id,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	// step
	Step string `json:"step,omitempty"`
	// screen (To names the agent)
	Lines int `json:"lines,omitempty"`
}

type Response struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type Status struct {
	Machine  string    `json:"machine"`
	Hostname string    `json:"hostname"`
	Version  string    `json:"version"`
	OS       string    `json:"os"`
	Now      time.Time `json:"now"`
	Started  time.Time `json:"started,omitempty"`
}

type NoteResult struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Target    string `json:"target"`
	Nudged    bool   `json:"nudged"`
	NudgeErr  string `json:"nudgeError,omitempty"`
}

// Version is set at build time (-ldflags "-X saahil.local/brain/internal/rpc.Version=...").
var Version = "dev"

var processStarted = time.Now()

// Handler carries what tests replace: the session registry folder and the command runner (Orca nudges, steps).
type Handler struct {
	RegistryDir string
	Run         agents.Runner
	Steps       *Steps
}

func NewHandler() *Handler {
	return &Handler{RegistryDir: agents.RegistryDir(), Run: agents.ExecRunner, Steps: NewSteps()}
}

func (h *Handler) Handle(ctx context.Context, req Request) Response {
	switch req.Op {
	case "status":
		host, _ := os.Hostname()
		return ok(Status{Machine: string(config.ThisMachine()), Hostname: host, Version: Version,
			OS: runtime.GOOS + "/" + runtime.GOARCH, Now: time.Now().UTC(), Started: processStarted.UTC()})
	case "sessions":
		sessions, err := agents.LocalSessions(h.RegistryDir)
		if err != nil {
			return fail(err)
		}
		agents.FillOrcaHandles(ctx, h.Run, sessions)
		return ok(sessions)
	case "note":
		return h.note(ctx, req)
	case "receipt":
		if req.SessionID == "" || req.ID == "" {
			return failf("receipt needs sessionId and id")
		}
		r, err := agents.ReceiptOf(req.SessionID, req.ID)
		if err != nil {
			return fail(err)
		}
		return ok(r)
	case "step":
		job, err := h.Steps.Start(req.Step)
		if err != nil {
			return fail(err)
		}
		return ok(job)
	case "sync-status":
		raw, err := os.ReadFile(filepath.Join(config.StateDir(), "sync-last.json"))
		if err != nil {
			return failf("no sync has run on the %s yet", config.ThisMachine())
		}
		return Response{OK: true, Result: withSyncAgent(ctx, raw)}
	case "job":
		job, err := h.Steps.Job(req.ID)
		if err != nil {
			return fail(err)
		}
		return ok(job)

	// ---- squad: read-only views of this machine's agents for the other machine ----
	case "screen":
		return h.screen(ctx, req)

	default:
		return failf("unknown op %q", req.Op)
	}
}

// screen reads an agent's terminal tail on this machine; it types nothing and starts nothing.
func (h *Handler) screen(ctx context.Context, req Request) Response {
	if req.To == "" {
		return failf("screen needs to")
	}
	lines := req.Lines
	if lines <= 0 {
		lines = 40
	}
	host, err := squad.HostFor(config.ThisMachine(), config.ThisMachine(), h.Run)
	if err != nil {
		return fail(err)
	}
	f := squad.New(config.ThisMachine(), host)
	f.Sessions = func(ctx context.Context) ([]agents.Session, error) {
		sessions, err := agents.LocalSessions(h.RegistryDir)
		agents.FillOrcaHandles(ctx, h.Run, sessions)
		return sessions, err
	}
	_, name, found := strings.Cut(req.To, ":")
	if !found {
		name = req.To
	}
	res, err := f.Screen(ctx, name, min(lines, 2000))
	if err != nil {
		return fail(err)
	}
	return ok(res)
}

func (h *Handler) note(ctx context.Context, req Request) Response {
	if strings.TrimSpace(req.Text) == "" || req.To == "" {
		return failf("note needs to and text")
	}
	sessions, err := agents.LocalSessions(h.RegistryDir)
	if err != nil {
		return fail(err)
	}
	agents.FillOrcaHandles(ctx, h.Run, sessions)
	target, found := agents.Resolve(sessions, req.To)
	if !found {
		return failf("no live session %q on %s", req.To, config.ThisMachine())
	}
	now := time.Now()
	n := agents.Note{ID: agents.NewNoteID(now), From: req.From, To: target.Address(), SessionID: target.SessionID,
		Text: req.Text, Created: now.UTC()}
	if err := agents.Enqueue(n); err != nil {
		return fail(err)
	}
	res := NoteResult{ID: n.ID, SessionID: n.SessionID, Target: target.Address()}
	if agents.ShouldNudge(target) {
		if err := agents.Nudge(ctx, h.Run, target); err != nil {
			res.NudgeErr = err.Error()
		} else {
			res.Nudged = true
		}
	}
	return ok(res)
}

func ok(v any) Response {
	raw, err := json.Marshal(v)
	if err != nil {
		return fail(err)
	}
	return Response{OK: true, Result: raw}
}

func fail(err error) Response { return Response{OK: false, Error: err.Error()} }

func failf(format string, args ...any) Response { return fail(fmt.Errorf(format, args...)) }
