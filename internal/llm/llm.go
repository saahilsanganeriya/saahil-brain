// Package llm runs one skill over one input through `claude --bare -p` and accounts for what it cost.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"saahil.local/brain/internal/config"
	"strconv"
	"sync"
)

// Call is one model invocation: the skill's markdown becomes the appended system prompt, Input goes on stdin.
type Call struct {
	Skill        string
	Input        string
	Model        string
	MaxBudgetUSD float64
	JSONSchema   string // optional; when set the model must answer with JSON matching it
}

type Result struct {
	Text      string          `json:"text"`
	JSON      json.RawMessage `json:"json,omitempty"`
	CostUSD   float64         `json:"cost_usd"`
	TokensIn  int             `json:"tokens_in"`
	TokensOut int             `json:"tokens_out"`
}

type Runner interface {
	Run(ctx context.Context, call Call) (Result, error)
}

// ModelError is a call the model side refused or cut short (budget hit, error result). It still cost money, so the
// Result next to it carries the cost; retrying it would pay again, so callers must not retry.
type ModelError struct{ Subtype, Message string }

func (e *ModelError) Error() string { return "model error " + e.Subtype + ": " + e.Message }

// Linux caps one argv string at 128 KiB; the skill rides in --append-system-prompt.
const maxSkillBytes = 120 * 1024

// Claude runs the claude CLI. Prefix wraps it (on the devbox: devbox-exec, which loads the provider and its key the
// same way interactive sessions get them; a systemd worker has neither in its environment).
type Claude struct {
	Bin    string
	Prefix []string
	// Login uses the claude CLI's own sign-in (keychain) instead of --bare's API key, still loading no settings,
	// hooks, plugins or MCP servers: the laptop has no API key, only the owner's login (an interactive shell only;
	// launchd cannot read the keychain).
	Login bool
}

// DefaultClaude is ~/.local/bin/claude, behind devbox-exec when this machine has it.
func DefaultClaude() Claude {
	bin := filepath.Join(config.BinDir(), "claude")
	wrapper := filepath.Join(config.BinDir(), "devbox-exec")
	if _, err := os.Stat(wrapper); err == nil {
		return Claude{Bin: wrapper, Prefix: []string{bin}}
	}
	return Claude{Bin: bin, Login: os.Getenv("BRAIN_LLM_LOGIN") == "1"}
}

// isolation keeps a call free of the owner's setup: --bare, or for a signed-in call only the (empty) local settings
// of its empty working folder, no hooks, no MCP servers, no skills.
func (c Claude) isolation() []string {
	if !c.Login {
		return []string{"--bare"}
	}
	return []string{"--setting-sources", "local", "--settings", `{"disableAllHooks": true}`, "--strict-mcp-config",
		"--disable-slash-commands"}
}

func (c Claude) Run(ctx context.Context, call Call) (Result, error) {
	if len(call.Skill) > maxSkillBytes {
		return Result{}, fmt.Errorf("skill is %d bytes, over the %d argv limit", len(call.Skill), maxSkillBytes)
	}
	args := append(c.isolation(), "-p", "--model", call.Model, "--output-format", "json",
		"--max-budget-usd", strconv.FormatFloat(call.MaxBudgetUSD, 'f', 2, 64),
		"--no-session-persistence", "--append-system-prompt", call.Skill)
	if call.JSONSchema != "" {
		args = append(args, "--json-schema", call.JSONSchema)
	}
	// An empty working directory: the call must never see or touch the brain except through its input.
	dir, err := os.MkdirTemp("", "brain-llm-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, c.Bin, append(append([]string{}, c.Prefix...), args...)...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewBufferString(call.Input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	res, err := parseClaudeJSON(stdout.Bytes())
	var modelErr *ModelError
	if err != nil && !errors.As(err, &modelErr) && runErr != nil {
		return Result{}, fmt.Errorf("claude: %w (stderr %d bytes)", runErr, stderr.Len())
	}
	return res, err
}

// parseClaudeJSON returns a *ModelError next to a filled Result when the run cost money but did not succeed.
func parseClaudeJSON(out []byte) (Result, error) {
	var raw struct {
		Subtype          string          `json:"subtype"`
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		TotalCostUSD     float64         `json:"total_cost_usd"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		Usage            struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return Result{}, fmt.Errorf("claude output is not one JSON result: %w", err)
	}
	res := Result{
		Text:      raw.Result,
		CostUSD:   raw.TotalCostUSD,
		TokensIn:  raw.Usage.InputTokens + raw.Usage.CacheCreationInputTokens + raw.Usage.CacheReadInputTokens,
		TokensOut: raw.Usage.OutputTokens,
	}
	if len(raw.StructuredOutput) > 0 && string(raw.StructuredOutput) != "null" {
		res.JSON = raw.StructuredOutput
	}
	if raw.IsError || (raw.Subtype != "" && raw.Subtype != "success") {
		return res, &ModelError{Subtype: raw.Subtype, Message: truncate(raw.Result, 300)}
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Fake answers without a model: tests and the shadow end-to-end check use it so no agent is ever started.
type Fake struct {
	CostUSD float64
	Respond func(Call) Result // nil → an empty edit list

	mu    sync.Mutex
	calls int
}

func (f *Fake) Run(_ context.Context, call Call) (Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	res := Result{Text: "fake", JSON: json.RawMessage(`{"edits":[]}`)}
	if f.Respond != nil {
		res = f.Respond(call)
	}
	res.CostUSD = f.CostUSD
	res.TokensIn, res.TokensOut = len(call.Input)/4, len(res.JSON)/4
	return res, nil
}

func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
