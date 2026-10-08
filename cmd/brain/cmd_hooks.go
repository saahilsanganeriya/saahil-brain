package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/hooks"
)

func init() {
	register("hook start", "SessionStart/SubagentStart hook: the start digest (shadow: logged, not injected)", hookRunner(300*time.Millisecond, hooks.Start))
	register("hook prompt", "UserPromptSubmit hook: ≤ 3 pointers or nothing (shadow: logged, not injected)", hookRunner(300*time.Millisecond, hooks.Prompt))
	register("hook post", "PostToolUse hook: count opened/followed for pointers this session was shown", hookRunner(100*time.Millisecond,
		func(env hooks.Env, in hooks.Input) ([]byte, error) { return nil, hooks.Post(env, in) }))
	register("hook bench", "time the prompt hook on questions from the QA eval set, nothing logged: [--n 40]", cmdHookBench)
	register("_hook-names", "rebuild the prompt hook's name dictionary (started in the background)", cmdHookNames)
}

// maxHookInput bounds stdin: a prompt with a pasted brief is tens of KB, never megabytes.
const maxHookInput = 4 << 20

// hookRunner fails open: bad input, any error, or running past the budget prints nothing and exits 0.
func hookRunner(budget time.Duration, run func(hooks.Env, hooks.Input) ([]byte, error)) func([]string) int {
	return func([]string) int {
		var in hooks.Input
		if json.NewDecoder(io.LimitReader(os.Stdin, maxHookInput)).Decode(&in) != nil {
			return 0
		}
		env := hooks.DefaultEnv()
		env.Rebuild = startNamesRebuild
		done := make(chan []byte, 1)
		go func() {
			out, err := run(env, in)
			if err != nil {
				out = nil
			}
			done <- out
		}()
		select {
		case out := <-done:
			if len(out) > 0 {
				os.Stdout.Write(append(out, '\n'))
			}
		case <-time.After(budget):
		}
		return 0
	}
}

// startNamesRebuild runs `brain _hook-names` in its own session so it outlives the hook.
func startNamesRebuild() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "_hook-names")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

func cmdHookNames([]string) int {
	if err := hooks.RebuildNames(hooks.DefaultEnv()); err != nil {
		return fail("hook names: %v", err)
	}
	return 0
}

func cmdHookBench(args []string) int {
	fs := flag.NewFlagSet("hook bench", flag.ContinueOnError)
	n := fs.Int("n", 40, "how many questions (spread over the QA set)")
	if _, err := parseAll(fs, args); err != nil {
		return 2
	}
	raw, err := os.ReadFile(filepath.Join(config.BrainRoot(), ".brain", "evals", "sets", "qa.jsonl"))
	if err != nil {
		return fail("qa set: %v (brain eval build makes it)", err)
	}
	var questions []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var item struct{ Question string }
		if json.Unmarshal([]byte(line), &item) == nil && item.Question != "" {
			questions = append(questions, item.Question)
		}
	}
	if len(questions) == 0 || *n <= 0 {
		return fail("no questions in the qa set")
	}
	var prompts []string
	for i := 0; i < *n; i++ {
		prompts = append(prompts, questions[i*len(questions) / *n])
	}
	env := hooks.DefaultEnv()
	took, abstained, err := hooks.Bench(env, prompts, config.BrainRoot())
	if err != nil {
		return fail("%v", err)
	}
	slices.Sort(took)
	at := func(q float64) time.Duration { return took[min(len(took)-1, int(q*float64(len(took))))] }
	fmt.Printf("prompt hook on %s: n %d · p50 %s · p95 %s · max %s · abstained %d (budget p95 150ms)\n", config.ThisMachine(),
		len(took), at(0.5).Round(time.Millisecond), at(0.95).Round(time.Millisecond), took[len(took)-1].Round(time.Millisecond), abstained)
	return 0
}
