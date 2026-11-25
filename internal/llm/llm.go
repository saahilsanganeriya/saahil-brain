// Package llm runs one skill over one input through `claude --bare -p` and accounts for what it cost.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"saahil.local/brain/internal/config"
	"strconv"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
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
	// stream-json, not json: when a turn passes the ceiling, the answer it wrote is only in the stream (keptAnswer).
	args := append(c.isolation(), "-p", "--model", call.Model, "--output-format", "stream-json", "--verbose",
		"--max-budget-usd", strconv.FormatFloat(call.MaxBudgetUSD, 'f', 2, 64),
		"--no-session-persistence", "--append-system-prompt", call.Skill)
	if call.JSONSchema != "" {
		args = append(args, "--json-schema", call.JSONSchema)
	}
	// No built-in tools: --bare still reads the user's permission mode, and with bypassPermissions a call ran Bash
	// and read any file. An empty working directory: the call must never see or touch the brain except through its input.
	args = append(args, "--tools", "")
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
	res, err := parseClaudeStream(stdout.Bytes(), call.JSONSchema)
	var modelErr *ModelError
	if err != nil && !errors.As(err, &modelErr) && runErr != nil {
		return Result{}, fmt.Errorf("claude: %w (stderr %d bytes)", runErr, stderr.Len())
	}
	return res, err
}

// streamLine is one line of the CLI's stream-json output: an assistant message or the final result.
type streamLine struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type    string          `json:"type"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			IsError bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	StopReason       string          `json:"stop_reason"`
	Result           string          `json:"result"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		InputTokens              int `json:"inputTokens"`
		CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
		CacheReadInputTokens     int `json:"cacheReadInputTokens"`
		OutputTokens             int `json:"outputTokens"`
	} `json:"modelUsage"`
}

// parseClaudeStream returns a *ModelError next to a filled Result when the run cost money but did not succeed.
func parseClaudeStream(out []byte, schema string) (Result, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var final *streamLine
	var answer json.RawMessage
	for {
		var line streamLine
		err := dec.Decode(&line)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("claude output is not a JSON stream: %w", err)
		}
		switch line.Type {
		case "assistant":
			for _, c := range line.Message.Content {
				if c.Type == "tool_use" && c.Name == "StructuredOutput" {
					answer = c.Input
				}
			}
		case "user": // the CLI ran the tool; an answer it refused is never kept
			for _, c := range line.Message.Content {
				if c.Type == "tool_result" && c.IsError {
					answer = nil
				}
			}
		case "result":
			final = &line
		}
	}
	if final == nil {
		return Result{}, errors.New("claude output has no result line")
	}
	res := Result{
		Text:      final.Result,
		CostUSD:   final.TotalCostUSD,
		TokensIn:  final.Usage.InputTokens + final.Usage.CacheCreationInputTokens + final.Usage.CacheReadInputTokens,
		TokensOut: final.Usage.OutputTokens,
	}
	if res.TokensIn == 0 && res.TokensOut == 0 { // an error result reports zero usage; the per-model counts keep it
		for _, m := range final.ModelUsage {
			res.TokensIn += m.InputTokens + m.CacheCreationInputTokens + m.CacheReadInputTokens
			res.TokensOut += m.OutputTokens
		}
	}
	if len(final.StructuredOutput) > 0 && string(final.StructuredOutput) != "null" {
		res.JSON = final.StructuredOutput
	}
	if final.IsError || (final.Subtype != "" && final.Subtype != "success") {
		if keptAnswer(final, answer, schema) {
			res.Text, res.JSON = string(answer), answer
			return res, nil
		}
		return res, &ModelError{Subtype: final.Subtype, Message: truncate(final.Result, 300)}
	}
	return res, nil
}

// keptAnswer: the CLI checks --max-budget-usd only after a turn, and drops the answer even when it has already taken
// it, so a turn that ended on its StructuredOutput call holds a whole answer that was paid for. It is kept when it
// matches the schema, which the CLI may not have checked yet.
func keptAnswer(final *streamLine, answer json.RawMessage, schema string) bool {
	if final.Subtype != "error_max_budget_usd" || final.StopReason != "tool_use" || len(answer) == 0 || schema == "" {
		return false
	}
	var s jsonschema.Schema
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		return false
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return false
	}
	var instance any
	if err := json.Unmarshal(answer, &instance); err != nil {
		return false
	}
	return resolved.Validate(instance) == nil
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
