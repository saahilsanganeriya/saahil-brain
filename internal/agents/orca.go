package agents

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	"saahil.local/brain/internal/config"
)

// Runner runs an external command; tests replace it to record calls instead.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// OrcaCLI is `orca` inside an Orca terminal; elsewhere on Linux `orca-ide`, since a bare `orca` can be the GNOME
// screen reader there.
func OrcaCLI() string {
	if config.ThisMachine() == config.Devbox && os.Getenv("ORCA_PANE_KEY") == "" {
		return "orca-ide"
	}
	return "orca"
}

// ShouldNudge: a busy session takes the note at its next tool call or turn end; an idle one only wakes on a prompt,
// so it gets a one-line prompt typed into its Orca terminal.
func ShouldNudge(s Session) bool {
	return s.Status == "idle" && s.OrcaHandle != ""
}

const NudgeText = "(brain inbox: a note for you is attached to this prompt — read it and act on it)"

// Nudge types the wake line into the session's terminal and presses Enter.
func Nudge(ctx context.Context, run Runner, s Session) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err := run(ctx, OrcaCLI(), "terminal", "send", "--terminal", s.OrcaHandle, "--text", NudgeText, "--enter", "--json")
	return err
}

type orcaTerminal struct {
	Handle        string `json:"handle"`
	WorktreePath  string `json:"worktreePath"`
	AgentIdentity string `json:"agentIdentity"`
}

// FillOrcaHandles maps sessions without a known handle (the laptop) to the only Claude terminal in their folder.
func FillOrcaHandles(ctx context.Context, run Runner, sessions []Session) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := run(ctx, OrcaCLI(), "terminal", "list", "--json")
	if err != nil {
		return
	}
	var reply struct {
		Result struct {
			Terminals []orcaTerminal `json:"terminals"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &reply) != nil {
		return
	}
	byFolder := map[string][]string{}
	for _, t := range reply.Result.Terminals {
		if t.AgentIdentity == "claude" {
			folder := config.Canonical(t.WorktreePath)
			byFolder[folder] = append(byFolder[folder], t.Handle)
		}
	}
	sameFolder := map[string]int{}
	for _, s := range sessions {
		sameFolder[s.CWD]++
	}
	for i := range sessions {
		if sessions[i].OrcaHandle == "" && len(byFolder[sessions[i].CWD]) == 1 && sameFolder[sessions[i].CWD] == 1 {
			sessions[i].OrcaHandle = byFolder[sessions[i].CWD][0]
		}
	}
}
