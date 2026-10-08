package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeClaude is a stand-in binary: it fails unless the flags the cost guard depends on are present and the input
// arrived on stdin, then prints a result the way `claude --output-format json` does.
const fakeClaude = `#!/bin/sh
input=$(cat)
case "$*" in *"--bare -p --model claude-haiku-5-5 --output-format json --max-budget-usd 0.50 --no-session-persistence --append-system-prompt SKILL --json-schema {}"*) ;; *) echo "bad args: $*" >&2; exit 3;; esac
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
