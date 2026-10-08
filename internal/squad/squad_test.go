package squad

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/agents"
	"saahil.local/brain/internal/config"
)

type sent struct {
	text  string
	enter bool
}

// fakeOrca answers the orca CLI from a fixed terminal list and records what is typed; every other command (bash,
// git) runs for real against the test's temp folders.
type fakeOrca struct {
	terminals []Terminal
	sends     []sent
	created   [][]string
	closed    []string
	onEnter   func()
}

func (f *fakeOrca) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "orca" {
		return agents.ExecRunner(ctx, name, args...)
	}
	reply := func(v any) ([]byte, error) { return json.Marshal(map[string]any{"ok": true, "result": v}) }
	switch args[0] + " " + args[1] {
	case "terminal list":
		return reply(map[string]any{"terminals": f.terminals})
	case "terminal send":
		s := sent{text: flagValue(args, "--text"), enter: hasFlag(args, "--enter")}
		f.sends = append(f.sends, s)
		if s.enter && s.text == "" && f.onEnter != nil {
			f.onEnter()
		}
		return reply(map[string]any{"send": map[string]any{}})
	case "terminal create":
		f.created = append(f.created, args)
		t := Terminal{Handle: "term_new", TabID: "tab-new", WorktreePath: strings.TrimPrefix(flagValue(args, "--worktree"), "path:")}
		f.terminals = append(f.terminals, t)
		return reply(map[string]any{"terminal": t})
	case "terminal wait":
		return reply(map[string]any{"wait": map[string]any{"satisfied": false}})
	case "terminal close":
		f.closed = append(f.closed, flagValue(args, "--terminal"))
		return reply(map[string]any{})
	}
	return []byte(`{"ok":false,"error":{"message":"fake orca: unexpected call"}}`), nil
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func testSquad(t *testing.T, orca *fakeOrca) (*Squad, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BRAIN_HOME", t.TempDir())
	out := &bytes.Buffer{}
	f := New(config.Devbox, Host{Machine: config.Devbox, OrcaCLI: []string{"orca"}, Run: orca.run})
	f.Out, f.Sleep = out, func(time.Duration) {}
	f.Sessions = func(context.Context) ([]agents.Session, error) { return nil, nil }
	return f, out
}

func register(t *testing.T, f *Squad, names map[string]RegistryEntry) {
	t.Helper()
	raw, _ := json.Marshal(names)
	if err := os.MkdirAll(filepath.Dir(f.Registry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.Registry, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// Catches a /goal reported as set when the agent never registered it (an older goal's "Goal set" line counted as
// proof), a goal sent as one pasted message (which Claude treats as plain text, not the command), and an over-long
// goal typed into the prompt instead of refused.
func TestGoalSetsFullTextVerified(t *testing.T) {
	setup := func(t *testing.T, registers bool, body string) (*Squad, *fakeOrca, error) {
		orca := &fakeOrca{}
		f, _ := testSquad(t, orca)
		folder := filepath.Join(config.Home(), "wt", "audio")
		transcriptDir := filepath.Join(f.Projects, projectSlug(folder))
		for _, dir := range []string{transcriptDir, folder} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(folder+"-goal.md"), []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		transcript := filepath.Join(transcriptDir, "s1.jsonl")
		appendLine(t, transcript, `{"type":"system","content":"Goal set: Ship the synthetic parser."}`)
		orca.terminals = []Terminal{{Handle: "term_a", TabID: "tab-a", WorktreePath: folder, AgentIdentity: "claude"}}
		register(t, f, map[string]RegistryEntry{"audio": {Tab: "tab-a", Folder: folder}})
		if registers {
			orca.onEnter = func() {
				appendLine(t, transcript, `{"type":"system","content":"Goal set: Ship the synthetic parser."}`)
			}
		}
		return f, orca, f.Goal(context.Background(), "audio", folder+"-goal.md", "tests pass")
	}

	body := "Ship the synthetic parser.\nKeep the fixtures synthetic."
	t.Run("new Goal set line", func(t *testing.T) {
		_, orca, err := setup(t, true, body)
		if err != nil {
			t.Fatal(err)
		}
		want := []sent{{"/goal ", false}, {body + "\nDONE WHEN: tests pass", false}, {"", true}}
		if len(orca.sends) != len(want) {
			t.Fatalf("sends %+v, want %+v", orca.sends, want)
		}
		for i := range want {
			if orca.sends[i] != want[i] {
				t.Errorf("send %d = %+v, want %+v", i, orca.sends[i], want[i])
			}
		}
	})
	t.Run("only an older Goal set line", func(t *testing.T) {
		_, _, err := setup(t, false, body)
		if err == nil || !strings.Contains(err.Error(), `no "Goal set: Ship the synthetic parser."`) {
			t.Fatalf("err = %v, want the missing-probe refusal", err)
		}
	})
	t.Run("over 4000 characters", func(t *testing.T) {
		_, orca, err := setup(t, true, strings.Repeat("g", 4001))
		if err == nil || !strings.Contains(err.Error(), "at most 4,000") {
			t.Fatalf("err = %v, want the 4,000-character refusal", err)
		}
		if len(orca.sends) != 0 {
			t.Fatalf("typed %d sends for a refused goal", len(orca.sends))
		}
	})
}

// Catches resuming a conversation that another Claude process may still be appending to (two writers corrupt one
// transcript): a transcript written under 120 s ago is refused unless --force.
func TestResumeRefusesFreshTranscript(t *testing.T) {
	const sid = "0f0e0d0c-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		age     time.Duration
		force   bool
		started bool
	}{{119 * time.Second, false, false}, {119 * time.Second, true, true}, {121 * time.Second, false, true}} {
		orca := &fakeOrca{}
		f, _ := testSquad(t, orca)
		cwd := filepath.Join(config.Home(), "wt", "resume")
		path := filepath.Join(f.Projects, projectSlug(cwd), sid+".jsonl")
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		appendLine(t, path, `{"type":"user","cwd":"`+cwd+`"}`)
		now := time.Now().Truncate(time.Second)
		if err := os.Chtimes(path, now.Add(-tc.age), now.Add(-tc.age)); err != nil {
			t.Fatal(err)
		}
		f.Now = func() time.Time { return now }

		err := f.Resume(context.Background(), "resumed", sid[:8], tc.force)
		if tc.started != (len(orca.created) == 1) {
			t.Fatalf("age %s force %v: started=%v (err %v)", tc.age, tc.force, len(orca.created) == 1, err)
		}
		if !tc.started && (err == nil || !strings.Contains(err.Error(), "--force")) {
			t.Fatalf("age %s: err = %v, want a refusal naming --force", tc.age, err)
		}
		if tc.started && (err != nil || !strings.Contains(flagValue(orca.created[0], "--command"), "--resume "+sid)) {
			t.Fatalf("age %s force %v: err %v, create %v", tc.age, tc.force, err, orca.created)
		}
	}
}

func git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test User", "-c", "user.email=user@example.com"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Catches stop --rm-worktree deleting a checkout outside the worktrees root: a sibling "<root>-old", or a path
// spelled "<root>/../…" that passes a plain prefix check; and a dirty worktree being force-removed.
func TestStopRemovesOnlyWorktreesUnderRoot(t *testing.T) {
	orca := &fakeOrca{}
	f, _ := testSquad(t, orca)
	git(t, "init", "-q", "-b", "main", f.Repo)
	git(t, "-C", f.Repo, "commit", "-q", "--allow-empty", "-m", "init")
	inside := filepath.Join(f.Worktrees, "clean")
	dirty := filepath.Join(f.Worktrees, "dirty")
	outside := filepath.Join(filepath.Dir(f.Worktrees), filepath.Base(f.Worktrees)+"-old", "sibling")
	for i, dir := range []string{inside, dirty, outside} {
		git(t, "-C", f.Repo, "worktree", "add", "-q", "-b", "b"+string(rune('0'+i)), dir)
	}
	if err := os.WriteFile(filepath.Join(dirty, "unsaved.txt"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	sneaky := f.Worktrees + "/../" + filepath.Base(f.Worktrees) + "-old/sibling" // passes HasPrefix(root + "/")
	orca.terminals = []Terminal{
		{Handle: "term_in", TabID: "tab-in", WorktreePath: inside, AgentIdentity: "claude"},
		{Handle: "term_dirty", TabID: "tab-dirty", WorktreePath: dirty, AgentIdentity: "claude"},
		{Handle: "term_out", TabID: "tab-out", WorktreePath: sneaky, AgentIdentity: "claude"},
	}
	register(t, f, map[string]RegistryEntry{"in": {Tab: "tab-in"}, "dirty": {Tab: "tab-dirty"}, "out": {Tab: "tab-out"}})

	for _, name := range []string{"in", "dirty", "out"} {
		if err := f.Stop(context.Background(), name, true); err != nil {
			t.Fatalf("stop %s: %v", name, err)
		}
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Errorf("clean worktree under the root was not removed: %v", err)
	}
	for _, kept := range []string{dirty, outside} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed: %v", kept, err)
		}
	}
	if len(orca.closed) != 3 {
		t.Errorf("closed %v, want all three terminals", orca.closed)
	}
}

// Catches box deleting files on the devbox: rsync must never get a --delete flag, even when an --exclude value is
// itself "--delete".
func TestBoxNeverDeletes(t *testing.T) {
	orca := &fakeOrca{}
	f, _ := testSquad(t, orca)
	f.Here = config.Laptop
	f.Host = Host{Machine: config.Devbox, Remote: true, OrcaCLI: []string{"orca"},
		Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
	var argv [][]string
	f.Stream = func(_ context.Context, name string, args ...string) error {
		argv = append(argv, append([]string{name}, args...))
		return nil
	}
	box := t.TempDir()
	if err := f.Box(context.Background(), box, []string{"--delete", "artifacts/big/"}, false); err != nil {
		t.Fatal(err)
	}
	if len(argv) != 1 || !strings.HasSuffix(argv[0][0], "rsync") {
		t.Fatalf("calls %v, want one rsync", argv)
	}
	for _, a := range argv[0] {
		if strings.HasPrefix(a, "--del") {
			t.Fatalf("rsync got %q: %v", a, argv[0])
		}
	}
	if !hasFlag(argv[0], "--update") || !hasFlag(argv[0], "--exclude=artifacts/big/") {
		t.Fatalf("rsync lost --update or the exclude: %v", argv[0])
	}
}
