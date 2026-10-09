package bridge

import (
	"os"
	"path/filepath"
	"testing"

	"saahil.local/brain/internal/config"
)

// TestSelfUpdateBringsTheDevboxIdentityOnce catches a laptop that never gets the person's settings (self-update is
// the devbox's only way to hand them over), the pull going to the derived <user>-devbox instead of the host the
// bridge's tunnel already reaches, an existing identity.json replaced, and a broken download installed.
func TestSelfUpdateBringsTheDevboxIdentityOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	t.Setenv("BRAIN_MACHINE", "laptop")
	t.Setenv("USER", "test-user")
	plist := filepath.Join(home, "Library", "LaunchAgents", TunnelLabel+".plist")
	_ = os.MkdirAll(filepath.Dir(plist), 0o755)
	f, _ := os.Create(plist)
	_ = plistTemplate.Execute(f, agentSpec{Label: TunnelLabel, Args: []string{"/usr/bin/ssh", "-N", "box-test"}, Home: home})
	f.Close()
	if got := updateHost(); got != "box-test" {
		t.Errorf("updateHost = %q, want the tunnel's box-test", got)
	}

	broken := func(src, dst string) error { return os.WriteFile(dst, []byte(`{"devbox_host": `), 0o644) }
	if copied, err := copyDevboxIdentity(broken); err == nil || copied {
		t.Errorf("a broken download: copied %v, err %v", copied, err)
	}
	var asked string
	fetch := func(src, dst string) error {
		asked = src
		return os.WriteFile(dst, []byte(`{"devbox_host": "box-test", "devbox_home": "/home/test-user"}`), 0o644)
	}
	if copied, err := copyDevboxIdentity(fetch); err != nil || !copied || asked != ".config/brain/identity.json" {
		t.Fatalf("copied %v, err %v, asked for %q", copied, err, asked)
	}
	if config.DevboxHost() != "box-test" || updateHost() != "box-test" {
		t.Errorf("after the copy: devbox_host %q", config.DevboxHost())
	}
	never := func(string, string) error { t.Error("fetched over an existing identity.json"); return nil }
	if copied, err := copyDevboxIdentity(never); copied || err != nil {
		t.Errorf("existing file: copied %v, err %v", copied, err)
	}
}
