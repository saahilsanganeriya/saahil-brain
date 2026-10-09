package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"saahil.local/brain/internal/store"
	"sort"
	"syscall"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/hooks"
)

func init() {
	register("hook start", "SessionStart/SubagentStart hook: the start digest (shadow: logged, not injected)", hookRunner(300*time.Millisecond, hooks.Start))
	register("hook prompt", "UserPromptSubmit hook: ≤ 3 pointers or nothing (shadow: logged, not injected)", hookRunner(300*time.Millisecond, hooks.Prompt))
	register("hook post", "PostToolUse hook: count opened/followed for pointers this session was shown, and brain pulls", hookRunner(100*time.Millisecond,
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

// hookEnd counts the session's ignored pointers for `hook end` and `hook compact` (cmdHookCapture), failing open
// within 200 ms so the session note still starts inside the 300 ms budget.
func hookEnd(raw []byte) {
	var in hooks.Input
	if json.Unmarshal(raw, &in) != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_ = hooks.End(hooks.DefaultEnv(), in)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
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
	cold := fs.Bool("cold", false, "run each question in a fresh process, as a real hook runs, and split the time by phase")
	one := fs.String("one", "", "") // a --cold child: time this one prompt and print its phases as JSON
	if _, err := parseAll(fs, args); err != nil {
		return 2
	}
	env := hooks.DefaultEnv()
	if *one != "" {
		phases, _, err := hooks.BenchPhases(env, []string{*one}, config.BrainRoot())
		if err != nil || len(phases) != 1 {
			return fail("bench: %v", err)
		}
		raw, _ := json.Marshal(phases[0])
		fmt.Println(string(raw))
		return 0
	}
	prompts, err := hooks.BenchQuestions(config.BrainRoot(), *n)
	if err != nil {
		return fail("qa set: %v (brain eval build makes it)", err)
	}
	if *cold {
		if code := benchCold(env, prompts, ""); code != 0 {
			return code
		}
		fmt.Println("the same prompts with the index memory-mapped (BRAIN_SQLITE_MMAP=2147483648):")
		return benchCold(env, prompts, "2147483648")
	}
	took, abstained, err := hooks.Bench(env, prompts, config.BrainRoot())
	if err != nil || len(took) == 0 {
		return fail("bench: %v", err)
	}
	fmt.Printf("prompt hook on %s: n %d · p50 %s · p95 %s · max %s · abstained %d (budget p95 150ms)\n", config.ThisMachine(),
		len(took), hooks.Quantile(took, 0.5).Round(time.Millisecond), hooks.Quantile(took, 0.95).Round(time.Millisecond),
		hooks.Quantile(took, 1).Round(time.Millisecond), abstained)
	return 0
}

// benchCold runs each prompt in a fresh process of this binary, as a hook runs, and reports each phase's p50 and
// p95, the whole process's wall time, and the sizes of the files a hook opens: what a warm in-process bench hides.
func benchCold(env hooks.Env, prompts []string, mmap string) int {
	exe, err := os.Executable()
	if err != nil {
		return fail("%v", err)
	}
	var open, names, turn, read, digest, ptrs, total, wall []time.Duration
	type slow struct {
		prompt string
		ph     hooks.Phases
	}
	var slowest []slow
	for _, p := range prompts {
		started := time.Now()
		cmd := exec.Command(exe, "hook", "bench", "--one", p)
		cmd.Env = append(os.Environ(), "BRAIN_SQLITE_MMAP="+mmap)
		out, err := cmd.Output()
		if err != nil {
			return fail("child: %v", err)
		}
		wall = append(wall, time.Since(started))
		var ph hooks.Phases
		if err := json.Unmarshal(out, &ph); err != nil {
			return fail("child output: %v", err)
		}
		open, names, turn, total = append(open, ph.Open), append(names, ph.Names), append(turn, ph.Turn), append(total, ph.Total())
		read, digest, ptrs = append(read, ph.Read), append(digest, ph.Digest), append(ptrs, ph.Pointers)
		slowest = append(slowest, slow{p, ph})
	}
	sort.Slice(slowest, func(i, j int) bool { return slowest[i].ph.Turn > slowest[j].ph.Turn })
	row := func(name string, d []time.Duration) {
		fmt.Printf("  %-34s p50 %6s  p95 %6s\n", name, hooks.Quantile(d, 0.5).Round(time.Millisecond), hooks.Quantile(d, 0.95).Round(time.Millisecond))
	}
	fmt.Printf("prompt hook on %s, %d prompts, each in a fresh process (nothing logged):\n", config.ThisMachine(), len(prompts))
	row("open the index", open)
	row("load the names cache", names)
	row("turn (read prompt, digest, pointers)", turn)
	row("  read the prompt", read)
	row("  digest (first prompt)", digest)
	row("  pointers", ptrs)
	row("in-process total (what lookups.ms holds)", total)
	row("whole process (exec to exit)", wall)
	size := func(p string) string {
		fi, err := os.Stat(p)
		if err != nil {
			return "missing"
		}
		return fmt.Sprintf("%.1f MB", float64(fi.Size())/1e6)
	}
	for _, s := range slowest[:min(3, len(slowest))] {
		fmt.Printf("  slow: turn %s (read %s · digest %s · pointers %s): %.60q\n", s.ph.Turn.Round(time.Millisecond),
			s.ph.Read.Round(time.Millisecond), s.ph.Digest.Round(time.Millisecond), s.ph.Pointers.Round(time.Millisecond), s.prompt)
	}
	db := store.Path(config.BrainRoot())
	fmt.Printf("  files: index %s · wal %s · names cache %s · often cache %s\n", size(db), size(db+"-wal"),
		size(filepath.Join(env.StateDir, "names.tsv")), size(filepath.Join(env.StateDir, "often.tsv")))
	return 0
}
