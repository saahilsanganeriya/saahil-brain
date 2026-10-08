package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/config"
)

const fixtureID = "11111111-2222-4333-8444-555555555555"

// projectsFixture lays the synthetic transcripts out the way Claude Code does: <slug>/<id>.jsonl plus
// <slug>/<id>/subagents/agent-*.jsonl. The fixtures are stored as .ndjson because the repo ignores *.jsonl.
func projectsFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "projects", "-demo")
	copyFile(t, "../../testdata/session/main.ndjson", filepath.Join(dir, fixtureID+".jsonl"))
	copyFile(t, "../../testdata/session/subagent.ndjson", filepath.Join(dir, fixtureID, "subagents", "agent-a1.jsonl"))
	return filepath.Join(dir, fixtureID+".jsonl")
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeNote(t *testing.T, transcript, brain string) (Result, string) {
	t.Helper()
	res, err := Note(transcript, Options{BrainRoot: brain, Machine: config.Devbox})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(raw)
}

func section(note, heading string) string {
	_, rest, ok := strings.Cut(note, "\n## "+heading+"\n")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "\n## ")
	return body
}

// Catches a secret pasted into an owner prompt reaching the session note (the note is indexed and synced), and any
// one secret shape the redactor misses.
func TestSessionNoteRedactsSecrets(t *testing.T) {
	_, note := writeNote(t, projectsFixture(t), t.TempDir())
	for _, secret := range []string{"eyJhbGciOiJIUzI1NiJ9", "AKIAABCDEFGHIJKLMNOP", "ghp_0123456789abcdef"} {
		if strings.Contains(note, secret) {
			t.Errorf("note contains secret %q", secret)
		}
	}
	if !strings.Contains(section(note, "Owner prompts"), "don't touch the plan") {
		t.Errorf("redaction dropped the prompt instead of the secret:\n%s", note)
	}

	shapes := map[string]string{
		"bearer":      "curl -H 'Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789'",
		"anthropic":   "export KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz012345",
		"openai":      "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789",
		"password":    "psql password=hunter2hunter2 host=db",
		"private key": "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAsecretsecret\n-----END RSA PRIVATE KEY-----",
		"cut key":     "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA",
		"blob":        "signature: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"jwt bare":    "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4eHh4eHh4In0.",
		"slack":       "xoxb-123456789012-abcdefghijkl",
	}
	leaks := map[string]string{
		"bearer": "abcdefghijklmnop", "anthropic": "api03-abcdef", "openai": "proj-abcdef", "password": "hunter2",
		"private key": "MIIEow", "cut key": "b3BlbnNz", "blob": "9f86d081884c", "jwt bare": "eyJhbGciOiJub25lIn0",
		"slack": "123456789012-abc",
	}
	for name, text := range shapes {
		if got := Redact(text); strings.Contains(got, leaks[name]) {
			t.Errorf("%s: Redact(%q) = %q still holds the secret", name, text, got)
		}
	}
}

// Catches tool results, hook injections, system reminders, task notifications, compact summaries and subagent
// prompts being recorded as things the owner said — the dream treats owner prompts as ground truth.
func TestOwnerPromptsOnly(t *testing.T) {
	_, note := writeNote(t, projectsFixture(t), t.TempDir())
	prompts := section(note, "Owner prompts")
	for _, want := range []string{"OWNER-PROMPT-ONE", "OWNER-PROMPT-QUEUED", "don't touch the plan"} {
		if !strings.Contains(prompts, want) {
			t.Errorf("owner prompts miss %q:\n%s", want, prompts)
		}
	}
	for _, not := range []string{"TOOL-RESULT-TEXT", "SYSTEM-REMINDER-TEXT", "HOOK-INJECTION-TEXT", "TASK-NOTIFICATION",
		"SIDECHAIN-PROMPT", "SUBAGENT-PROMPT", "COMPACT-SUMMARY", "NOTE-BODY-TEXT", "FINAL-ASSISTANT"} {
		if strings.Contains(prompts, not) {
			t.Errorf("owner prompts contain %q:\n%s", not, prompts)
		}
	}
	if strings.Contains(note, "TOOL-RESULT-TEXT") {
		t.Errorf("a tool output reached the note")
	}
	did := section(note, "What it did")
	if !strings.Contains(did, "COMPACT-SUMMARY-TEXT") || !strings.Contains(did, "FINAL-ASSISTANT-TEXT") {
		t.Errorf("What it did misses the compact summary or final message:\n%s", did)
	}
	if !strings.Contains(note, "\ncorrections: 1\n") {
		t.Errorf("want exactly one correction (the 'no, don't …' prompt):\n%s", note)
	}
}

// Catches traces lost from Bash commands, subagents or the saahil-docs → work/<box> mapping, and messages/PRs
// counted twice when Claude Code records them in two places.
func TestTracesFromEveryToolInput(t *testing.T) {
	res, note := writeNote(t, projectsFixture(t), t.TempDir())
	wantLines := []string{
		"touched: [work/demo-box/STATE.md, ",
		"works: [demo-box, other-box]",
		"prs: [example-org/demo-repo#42]",
		`messages: [{from: "laptop:orchestrator", to: "devbox:demo-worker", id: n-20261008T010500Z-0a1b2c3d}]`,
		"subagents: 1",
		"cwd: /Users/test-user/Documents/worktrees/demo/repo",
		"started: 2026-10-08T01:00:00Z",
		"ended: 2026-10-08T02:00:00Z",
	}
	for _, want := range wantLines {
		if !strings.Contains(note, "\n"+want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	for _, path := range []string{"work/demo-box/tools/run.py", "work/other-box/notes", "work/demo-box/.data/run-1/result.json"} {
		if !strings.Contains(frontmatterLine(note, "touched"), path) {
			t.Errorf("touched misses %s", path)
		}
	}
	produced := frontmatterLine(note, "produced")
	if !strings.Contains(produced, "work/demo-box/.data/run-1/result.json") || !strings.Contains(produced, "work/demo-box/STATE.md") ||
		strings.Contains(produced, "run.py") {
		t.Errorf("produced = %s", produced)
	}
	if strings.Contains(note, "example.com") || strings.Contains(note, "/dev/null") || strings.Contains(note, "/tmp/demo/out") {
		t.Errorf("a URL, /dev/null or a path with a shell variable was taken for a file")
	}
	if !strings.Contains(frontmatterLine(note, "touched"), "/Users/test-user/scratch/runs,") {
		t.Errorf("a glob should leave its literal directory in touched: %s", frontmatterLine(note, "touched"))
	}
	wantPath := filepath.Join("sessions", "devbox", "2026", "10", "2026-10-08-11111111-demo-worker.md")
	if !strings.HasSuffix(res.Path, wantPath) {
		t.Errorf("path = %s, want …/%s", res.Path, wantPath)
	}
}

func frontmatterLine(note, key string) string {
	for _, line := range strings.Split(note, "\n") {
		if strings.HasPrefix(line, key+": ") {
			return line
		}
	}
	return ""
}

// Catches nondeterministic output (map order) and a rewrite on every hook run when nothing changed, and a renamed
// session leaving its old note behind.
func TestNoteRewrittenOnlyWhenTranscriptChanges(t *testing.T) {
	transcript, brain := projectsFixture(t), t.TempDir()
	first, note1 := writeNote(t, transcript, brain)
	second, note2 := writeNote(t, transcript, brain)
	if first.Status != Written || second.Status != Unchanged || note1 != note2 {
		t.Fatalf("statuses %s, %s; identical %v", first.Status, second.Status, note1 == note2)
	}
	other, err := Note(transcript, Options{BrainRoot: t.TempDir(), Machine: config.Devbox})
	if raw, _ := os.ReadFile(other.Path); err != nil || string(raw) != note1 {
		t.Fatalf("same transcript, different bytes")
	}

	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, `{"type":"custom-title","customTitle":"renamed-worker","sessionId":"%s"}`+"\n", fixtureID)
	f.Close()
	third, _ := writeNote(t, transcript, brain)
	if third.Status != Written || !strings.HasSuffix(third.Path, "-renamed-worker.md") {
		t.Fatalf("after change: %s %s", third.Status, third.Path)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Errorf("stale note %s kept after rename", first.Path)
	}
}

// Catches a session moved to another worktree (same id under two project dirs) flip-flopping its note between the two
// copies on every run instead of keeping the newer, larger one.
func TestMovedSessionKeepsOneNote(t *testing.T) {
	transcript, brain := projectsFixture(t), t.TempDir()
	moved := filepath.Join(filepath.Dir(filepath.Dir(transcript)), "-moved", fixtureID+".jsonl")
	copyFile(t, transcript, moved)
	f, err := os.OpenFile(moved, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, `{"type":"assistant","timestamp":"2026-10-08T03:00:00.000Z","message":{"content":[{"type":"text","text":"MOVED-COPY-TEXT"}]}}`+"\n")
	f.Close()

	for range 2 {
		for _, path := range []string{moved, transcript} {
			if _, err := Note(path, Options{BrainRoot: brain, Machine: config.Devbox}); err != nil {
				t.Fatal(err)
			}
		}
	}
	old, err := Note(transcript, Options{BrainRoot: brain, Machine: config.Devbox})
	if err != nil || old.Status != Skipped {
		t.Fatalf("older copy: %+v %v", old, err)
	}
	newer, note := writeNote(t, moved, brain)
	if newer.Status != Unchanged || !strings.Contains(note, "MOVED-COPY-TEXT") {
		t.Fatalf("newer copy: %s, has its text %v", newer.Status, strings.Contains(note, "MOVED-COPY-TEXT"))
	}
}

// Catches a long session producing a note over the 8 KB cap, or the cap trimming the most-used file instead of the
// least-used ones.
func TestNoteFitsSizeCap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects", "-big")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	start := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	for i := range 300 {
		ts := start.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		fmt.Fprintf(&b, `{"type":"user","origin":{"kind":"human"},"timestamp":"%s","message":{"content":"prompt %d %s"}}`+"\n",
			ts, i, strings.Repeat("words ", 120))
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":"%s","message":{"content":[{"type":"tool_use","id":"t%d","name":"Read","input":{"file_path":"/Users/test-user/src/file-%03d-with-a-long-name.go"}},{"type":"tool_use","id":"u%d","name":"Read","input":{"file_path":"/Users/test-user/src/hot.go"}}]}}`+"\n",
			ts, i, i, i)
	}
	transcript := filepath.Join(dir, "22222222-2222-4333-8444-555555555555.jsonl")
	if err := os.WriteFile(transcript, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, note := writeNote(t, transcript, t.TempDir())
	if len(note) > MaxNoteBytes {
		t.Errorf("note is %d bytes, cap %d", len(note), MaxNoteBytes)
	}
	if !strings.HasPrefix(frontmatterLine(note, "touched"), "touched: [/Users/test-user/src/hot.go") {
		t.Errorf("most-used file not kept first: %s", frontmatterLine(note, "touched"))
	}
}

// Catches one oversized line (a huge tool output) aborting the whole transcript instead of being skipped and
// counted.
func TestOversizedLineSkipped(t *testing.T) {
	input := "short-1\n" + strings.Repeat("x", 5000) + "\nshort-2\n" + strings.Repeat("y", 5000)
	var got []string
	skipped, err := readLines(strings.NewReader(input), 64, func(line []byte) error {
		got = append(got, strings.TrimSpace(string(line)))
		return nil
	})
	if err != nil || skipped != 2 || strings.Join(got, ",") != "short-1,short-2" {
		t.Fatalf("skipped=%d err=%v lines=%v", skipped, err, got)
	}
}

// Catches a bare token pasted after a URL (no "Bearer", no "key=") surviving into a session note: a laptop MCP token
// did on 2026-09-29. A sha, a UUID, a session id and a kebab-case path must survive.
func TestRedactsBareHighEntropyToken(t *testing.T) {
	fake := "Qx7Lm2Np9Rt4Vw8Yz3Ab6Cd1Ef5Gh0Jk-Ls_Mn2Op7" // synthetic, 43 chars, base64url
	in := "added the mcp server http://127.0.0.1:65535/mcp " + fake + " ok"
	if out := Redact(in); strings.Contains(out, fake) || !strings.Contains(out, "REDACTED-token") {
		t.Fatalf("token survived: %q", out)
	}
	keep := []string{
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", // sha256 hex
		"483fc905-54af-44d2-81f8-2a642b9f5cac",                             // uuid
		"anti-pattern-relayed-instruction-is-a-post-for-the-owner",         // kebab id
		"/Users/test-user/brain/work/box/plans/SPEC-UNIFY.md",      // path
	}
	for _, k := range keep {
		if out := Redact("x " + k + " y"); !strings.Contains(out, k) {
			t.Errorf("redacted a non-secret: %q -> %q", k, out)
		}
	}
}
