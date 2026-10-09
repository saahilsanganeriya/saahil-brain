// Package scripts holds tests for the setup kit's shell scripts, run with fake aws, ssh and brain commands.
package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, env []string, name string, args ...string) (string, int) {
	t.Helper()
	// go test's cache sees only files the test process touches, not what bash reads: stat the script into the key
	if _, err := os.Stat(name); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(name, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// devboxEnv is a laptop HOME with identity.json, a fake brain, and fake aws/ssh that log every call to log.
func devboxEnv(t *testing.T, state, busy string) (env []string, log string) {
	t.Helper()
	home := t.TempDir()
	log = filepath.Join(home, "calls.log")
	fake := filepath.Join(home, "fakebin")
	writeExec(t, filepath.Join(fake, "aws"), `echo "aws $*" >>"$LOG"
case "$*" in
  *describe-instances*State*) if [ "$STATE" = fail ]; then echo "token expired" >&2; exit 255; fi; echo "$STATE" ;;
  *describe-instances*) echo m7i.large ;;
  *describe-instance-attribute*) echo False ;;
esac`)
	writeExec(t, filepath.Join(fake, "ssh"), `echo "ssh $*" >>"$LOG"
case "$*" in *pgrep*) printf '%b\n' "$BUSY" ;; esac`)
	writeExec(t, filepath.Join(home, ".local", "bin", "brain"), `echo "brain $*" >>"$LOG"`)
	if err := os.MkdirAll(filepath.Join(home, ".config", "brain"), 0o700); err != nil {
		t.Fatal(err)
	}
	identity := `{"devbox_instance_id": "i-0test", "devbox_host": "test-devbox"}`
	if err := os.WriteFile(filepath.Join(home, ".config", "brain", "identity.json"), []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"HOME=" + home, "PATH=" + fake + ":/usr/bin:/bin:/opt/homebrew/bin", "LOG=" + log, "STATE=" + state, "BUSY=" + busy}, log
}

func calls(t *testing.T, log string) string {
	raw, _ := os.ReadFile(log)
	return string(raw)
}

// The laptop's old devbox script read the state inside `[ "$(state)" = running ] || echo "already $(state)"`, so an
// expired AWS sign-in printed "already" and exited 0 with the instance still running.
func TestDevboxDownFailsWhenStateUnreadable(t *testing.T) {
	env, log := devboxEnv(t, "fail", "0\n0\n0.1")
	out, code := run(t, env, "./devbox", "down")
	if code == 0 || strings.Contains(out, "already") {
		t.Fatalf("down with an unreadable state: exit %d, output %q; want a failure that never says already", code, out)
	}
	if strings.Contains(calls(t, log), "stop-instances") {
		t.Fatalf("stopped the instance without knowing its state:\n%s", calls(t, log))
	}
}

// A stop ends every process on the devbox: down must refuse while Claude sessions run, and stop only with --force.
func TestDevboxDownRefusesWhileClaudeSessionsRun(t *testing.T) {
	env, log := devboxEnv(t, "running", "0\n2\n0.4")
	out, code := run(t, env, "./devbox", "down")
	if code == 0 || !strings.Contains(out, "devbox down --force") || strings.Contains(calls(t, log), "stop-instances") {
		t.Fatalf("down with 2 Claude sessions: exit %d, output %q, calls:\n%s", code, out, calls(t, log))
	}
	if out, code := run(t, env, "./devbox", "down", "--force"); code != 0 || !strings.Contains(calls(t, log), "stop-instances") {
		t.Fatalf("down --force did not stop: exit %d, output %q", code, out)
	}
}

// A dry run that creates a file (a mkdir outside act, a token written early) changes a colleague's machine while it
// claims to change nothing.
func TestSetupDevboxDryRunWritesNothing(t *testing.T) {
	kit := t.TempDir()
	for _, dir := range []string{"bin", "scripts", "deploy/systemd"} {
		if err := os.MkdirAll(filepath.Join(kit, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var sums strings.Builder
	for _, name := range []string{"bin/brain-linux-amd64", "bin/brain-worker-linux-amd64", "bin/brain-darwin-arm64"} {
		body := "binary " + name
		writeExec(t, filepath.Join(kit, name), body)
		sum := sha256.Sum256([]byte("#!/bin/bash\n" + body))
		sums.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(kit, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"setup-devbox.sh", "setup-laptop.sh", "devbox", "brain-care"} {
		raw, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		writeExec(t, filepath.Join(kit, "scripts", script), strings.TrimPrefix(string(raw), "#!/bin/bash\n"))
	}
	units, _ := filepath.Glob("../deploy/systemd/*")
	for _, unit := range units {
		raw, _ := os.ReadFile(unit)
		if err := os.WriteFile(filepath.Join(kit, "deploy", "systemd", filepath.Base(unit)), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin:/opt/homebrew/bin"}
	out, code := run(t, env, filepath.Join(kit, "scripts", "setup-devbox.sh"), "--dry-run", "--laptop-home", filepath.Join(home, "laptop"))
	if code != 0 {
		t.Fatalf("dry run exit %d:\n%s", code, out)
	}
	for _, step := range []string{"bind mount", "binaries", "settings", "brain skeleton", "Temporal CLI", "systemd --user",
		"dream schedule", "bridge token", "agent hosts", "laptop files", "daily care", "next, on the laptop"} {
		if !strings.Contains(out, "== "+step) {
			t.Errorf("dry run never printed the step %q", step)
		}
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatalf("dry run wrote into HOME: %v\n%s", entries, out)
	}
}

// buildKit writes a complete synthetic kit with its source tarball and SHA256SUMS; plant edits files first, so the
// sums cover the edit.
func buildKit(t *testing.T, plant func(kit, src string)) string {
	t.Helper()
	for _, tool := range []string{"zip", "unzip", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	kit := filepath.Join(dir, "brain-setup-v1")
	files := []string{"README.md", "AGENT-SETUP.md", "identity.example.json", "docs/ARCHITECTURE.md", "docs/DEVBOX-AND-ORCA.md",
		"docs/BACKFILL.md", "docs/OPERATIONS.md", "docs/LESSONS.md", "docs/CONFIG.md", "deploy/systemd/brain-temporal.service",
		"deploy/systemd/brain-worker.service", "deploy/systemd/brain-care.service", "deploy/systemd/brain-care.timer",
		"deploy/systemd/orca-serve.service", "templates/brain-care-goal.md", "templates/brain-care-playbook.md"}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(kit, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(kit, f), []byte("text of "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"bin/brain-darwin-arm64", "bin/brain-linux-amd64", "bin/brain-worker-linux-amd64",
		"scripts/setup-devbox.sh", "scripts/setup-laptop.sh", "scripts/devbox", "scripts/brain-care"} {
		writeExec(t, filepath.Join(kit, f), "echo "+f+"\n")
	}
	src := filepath.Join(dir, "saahil-brain-v1")
	writeExec(t, filepath.Join(src, "cmd", "main.go"), "package main\n")
	if plant != nil {
		plant(kit, src)
	}
	if err := os.MkdirAll(filepath.Join(kit, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tar", "-czf", filepath.Join(kit, "source", "saahil-brain-v1.tar.gz"), "-C", dir, "saahil-brain-v1").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, out)
	}
	sums := exec.Command("bash", "-c", `find . -type f | sed 's|^\./||' | LC_ALL=C sort | xargs shasum -a 256`)
	sums.Dir = kit
	listing, err := sums.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kit, "SHA256SUMS"), listing, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func zipKit(t *testing.T, dir string) string {
	t.Helper()
	zip := filepath.Join(dir, "kit.zip")
	cmd := exec.Command("zip", "-qr", zip, "brain-setup-v1")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zip: %v %s", err, out)
	}
	return zip
}

// check-kit must see inside source/ (a gzip a plain grep of the zip cannot read) and must notice a binary swapped
// after the checksums were written; a clean kit passes, so a failure means the planted defect, not the fixture.
func TestCheckKitCatchesOwnerPathInSourceAndTamperedBinary(t *testing.T) {
	env := []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin:/opt/homebrew/bin"}
	owner := "/Users/saahilsangan" + "eriya/brain"
	for _, tc := range []struct {
		name, want string
		before     func(kit, src string)
		after      func(kit string)
	}{
		{name: "clean", want: "PASS"},
		{name: "owner path in source", want: "FAIL  owner laptop home in src/saahil-brain-v1/cmd/main.go",
			before: func(_, src string) {
				_ = os.WriteFile(filepath.Join(src, "cmd", "main.go"), []byte("package main\nconst root = \""+owner+"\"\n"), 0o644)
			}},
		{name: "binary swapped after the sums", want: "FAIL  checksum: bin/brain-linux-amd64",
			after: func(kit string) { writeExec(t, filepath.Join(kit, "bin", "brain-linux-amd64"), "echo swapped\n") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := buildKit(t, tc.before)
			if tc.after != nil {
				tc.after(filepath.Join(dir, "brain-setup-v1"))
			}
			out, code := run(t, env, "./check-kit.sh", zipKit(t, dir))
			if !strings.Contains(out, tc.want) || (tc.want == "PASS") != (code == 0) {
				t.Fatalf("exit %d, want %q in:\n%s", code, tc.want, out)
			}
		})
	}
}
