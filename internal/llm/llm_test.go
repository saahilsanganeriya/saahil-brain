package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeClaude is a stand-in binary: it fails unless the flags the cost guard depends on are present and the input
// arrived on stdin, then prints a result line the way `claude --output-format stream-json` ends.
const fakeClaude = `#!/bin/sh
input=$(cat)
case "$*" in *"--bare -p --model claude-haiku-5-5 --output-format stream-json --verbose --max-budget-usd 0.50 --no-session-persistence --append-system-prompt SKILL --json-schema {}"*) ;; *) echo "bad args: $*" >&2; exit 3;; esac
[ "$input" = "the input" ] || { echo "bad stdin" >&2; exit 4; }
subtype=${FAKE_SUBTYPE:-success}
cat <<EOF
{"type":"result","subtype":"$subtype","is_error":false,"result":"ok","total_cost_usd":0.0123,
 "usage":{"input_tokens":10,"cache_creation_input_tokens":5,"cache_read_input_tokens":100,"output_tokens":7},
 "structured_output":{"edits":[]}}
EOF
`

// TestClaudeRunnerParsesCostAndUsage catches a runner that loses the cost or the cached-token counts (the cap would
// then never trip), drops the budget flag, or reports a budget-exceeded run as a success that can be retried.
func TestClaudeRunnerParsesCostAndUsage(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	call := Call{Skill: "SKILL", Input: "the input", Model: "claude-haiku-5-5", MaxBudgetUSD: 0.5, JSONSchema: "{}"}

	res, err := Claude{Bin: bin}.Run(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if res.CostUSD != 0.0123 || res.TokensIn != 115 || res.TokensOut != 7 || string(res.JSON) != `{"edits":[]}` {
		t.Fatalf("parsed %+v", res)
	}

	t.Setenv("FAKE_SUBTYPE", "error_max_budget_usd")
	res, err = Claude{Bin: bin}.Run(context.Background(), call)
	var modelErr *ModelError
	if !errors.As(err, &modelErr) || modelErr.Subtype != "error_max_budget_usd" || res.CostUSD != 0.0123 {
		t.Fatalf("budget hit: err=%v res=%+v", err, res)
	}
}

// budgetCutStream is what the CLI prints when a call's one answer turn costs more than --max-budget-usd: the
// StructuredOutput tool call holds the whole answer, the CLI may or may not run the tool (FAKE_TOOL_RESULT), and the
// result line reports error_max_budget_usd with no answer.
const budgetCutStream = `#!/bin/sh
cat >/dev/null
cat <<EOF
{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"StructuredOutput","input":$FAKE_ANSWER}]}}
$FAKE_TOOL_RESULT
{"type":"result","subtype":"error_max_budget_usd","is_error":true,"stop_reason":"$FAKE_STOP","total_cost_usd":2.65,
 "usage":{"input_tokens":0,"output_tokens":0},"modelUsage":{"m":{"inputTokens":100,"cacheReadInputTokens":20,"outputTokens":40}},
 "errors":["Reached maximum budget"]}
EOF
`

// TestBudgetCutKeepsACompleteAnswer catches a runner that throws away an answer it paid for: the CLI checks the
// ceiling only after a turn, so 9 dream steps since 2026-10-17 paid $2.50-$3.67 each for an answer the model had
// written and got "model error error_max_budget_usd", whether or not the CLI had already taken the answer. An answer
// is kept only when its turn ended on the tool call, the CLI did not refuse it and it matches the schema, so a turn
// cut by max_tokens or a refused answer still fails.
func TestBudgetCutKeepsACompleteAnswer(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte(budgetCutStream), 0o755); err != nil {
		t.Fatal(err)
	}
	schema := `{"type":"object","properties":{"edits":{"type":"array"}},"required":["edits"]}`
	call := Call{Skill: "S", Input: "in", Model: "m", MaxBudgetUSD: 2.5, JSONSchema: schema}
	took := `{"type":"user","message":{"content":[{"type":"tool_result","content":"Structured output provided successfully"}]}}`
	refused := `{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"does not match the schema"}]}}`
	for _, tc := range []struct {
		name, answer, toolResult, stop string
		kept                           bool
	}{
		{"complete answer, tool not run", `{"edits":[{"op":"x"}]}`, "", "tool_use", true},
		{"complete answer the CLI took", `{"edits":[{"op":"x"}]}`, took, "tool_use", true},
		{"answer the CLI refused", `{"edits":[{"op":"x"}]}`, refused, "tool_use", false},
		{"turn cut by max_tokens", `{"edits":[{"op":"x"}]}`, "", "max_tokens", false},
		{"answer outside the schema", `{"edit":[]}`, "", "tool_use", false},
	} {
		t.Setenv("FAKE_ANSWER", tc.answer)
		t.Setenv("FAKE_TOOL_RESULT", tc.toolResult)
		t.Setenv("FAKE_STOP", tc.stop)
		res, err := Claude{Bin: bin}.Run(context.Background(), call)
		if res.CostUSD != 2.65 || res.TokensIn != 120 || res.TokensOut != 40 {
			t.Errorf("%s: cost or tokens lost: %+v", tc.name, res)
		}
		var modelErr *ModelError
		if tc.kept && (err != nil || string(res.JSON) != tc.answer) {
			t.Errorf("%s: want the answer kept, got err=%v json=%s", tc.name, err, res.JSON)
		}
		if !tc.kept && (!errors.As(err, &modelErr) || modelErr.Subtype != "error_max_budget_usd") {
			t.Errorf("%s: want error_max_budget_usd, got err=%v json=%s", tc.name, err, res.JSON)
		}
	}
}

// TestLoginModeStaysIsolated catches a signed-in call (the laptop has no API key, only the owner's login) that drops
// --bare without its replacement isolation: the owner's hooks, plugins, MCP servers and skills would run inside it.
func TestLoginModeStaysIsolated(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
cat >/dev/null
case "$*" in *--bare*) echo "bare in login mode" >&2; exit 3;; esac
case "$*" in "--setting-sources local --settings {\"disableAllHooks\": true} --strict-mcp-config --disable-slash-commands -p --model "*) ;; *) echo "bad args: $*" >&2; exit 3;; esac
echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.01,"usage":{}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Claude{Bin: bin, Login: true}).Run(context.Background(), Call{Skill: "S", Input: "in", Model: "m", MaxBudgetUSD: 1}); err != nil {
		t.Fatal(err)
	}
}

// TestCallsHaveNoTools catches a call that keeps Claude Code's built-in tools: under a bypassPermissions default in
// the user's settings (which --bare still reads) a dream call ran Bash and read the live brain, and reflect calls
// over short session notes looped to 2.6M input tokens.
func TestCallsHaveNoTools(t *testing.T) {
	dir := t.TempDir()
	bin, argsFile := filepath.Join(dir, "claude"), filepath.Join(dir, "args")
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' "$@" > "` + argsFile + `"
echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.01,"usage":{}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Claude{{Bin: bin}, {Bin: bin, Login: true}} {
		if _, err := c.Run(context.Background(), Call{Skill: "S", Input: "in", Model: "m", MaxBudgetUSD: 1, JSONSchema: "{}"}); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		i := slices.Index(args, "--tools")
		if i < 0 || i+1 >= len(args) || args[i+1] != "" {
			t.Errorf("login=%v: want --tools \"\" (no built-in tools), args %q", c.Login, args)
		}
	}
}

// TestRetryOfFailedKeyUnderStoreSchema catches the laptop's failed eval build: the store creates llm_calls with
// ended NOT NULL, and retrying a key whose first attempt failed reset ended to NULL ('NOT NULL constraint failed').
func TestRetryOfFailedKeyUnderStoreSchema(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "brain.db")
	l, err := OpenLedger(db, filepath.Join(dir, "llm"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`DROP TABLE llm_calls`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`CREATE TABLE llm_calls(key TEXT PRIMARY KEY, workflow TEXT NOT NULL, step TEXT NOT NULL, model TEXT NOT NULL,
		cost_usd REAL NOT NULL DEFAULT 0, tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
		started TEXT NOT NULL DEFAULT '', ended TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := l.Begin("k", "eval", "human-judge", "m", now); err != nil {
		t.Fatal(err)
	}
	if err := l.Finish("k", Result{}, "failed", now); err != nil {
		t.Fatal(err)
	}
	if err := l.Begin("k", "eval", "human-judge", "m", now.Add(time.Minute)); err != nil {
		t.Fatalf("retrying a failed key: %v", err)
	}
	l.Close()
}
