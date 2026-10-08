package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"saahil.local/brain/internal/hooks"
)

func init() {
	register("hook start", "SessionStart/SubagentStart hook: the start digest (shadow: logged, not injected)", hookRunner(300*time.Millisecond, hooks.Start))
	register("hook prompt", "UserPromptSubmit hook: ≤ 3 pointers or nothing (shadow: logged, not injected)", hookRunner(300*time.Millisecond, hooks.Prompt))
	register("hook post", "PostToolUse hook: count opened/followed for pointers this session was shown", hookRunner(100*time.Millisecond,
		func(env hooks.Env, in hooks.Input) ([]byte, error) { return nil, hooks.Post(env, in) }))
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
