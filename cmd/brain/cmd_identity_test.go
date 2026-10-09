package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"saahil.local/brain/internal/config/identitytest"
)

// TestIdentityInitKeepsWhatTheFileSays catches --init clobbering a value the person already set (a second setup run,
// the laptop's run with the devbox's values), dropping a key only the scripts use (devbox_region), pinning
// work_repo_orca so it no longer follows work_repo, or --force failing to replace a value.
func TestIdentityInitKeepsWhatTheFileSays(t *testing.T) {
	home := identitytest.Write(t, `{"devbox_host": "box-old", "devbox_region": "us-west-2"}`)
	t.Setenv("USER", "test-user")
	t.Setenv("HOME", "/home/test-user")
	t.Setenv("BRAIN_MACHINE", "devbox")
	if code := cmdIdentity([]string{"--init", "--user", "test-laptop", "--devbox-host", "box-new", "--orca-env", "env-1"}); code != 0 {
		t.Fatalf("--init exit %d", code)
	}
	got := readIdentity(t, home)
	for key, want := range map[string]any{"user": "test-laptop", "laptop_home": "/Users/test-laptop", "devbox_home": "/home/test-user",
		"devbox_host": "box-old", "orca_env": "env-1", "devbox_region": "us-west-2", "shadow_docs": []any{}} {
		if raw, _ := json.Marshal(got[key]); string(raw) != mustJSON(want) {
			t.Errorf("%s = %s, want %s", key, raw, mustJSON(want))
		}
	}
	if _, pinned := got["work_repo_orca"]; pinned {
		t.Error("work_repo_orca was pinned; it must follow work_repo")
	}

	if code := cmdIdentity([]string{"--init", "--force", "--devbox-host", "box-new"}); code != 0 {
		t.Fatalf("--init --force exit %d", code)
	}
	if got := readIdentity(t, home); got["devbox_host"] != "box-new" || got["orca_env"] != "env-1" {
		t.Errorf("after --force: devbox_host %v, orca_env %v", got["devbox_host"], got["orca_env"])
	}
}

func readIdentity(t *testing.T, home string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".config", "brain", "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	return got
}

func mustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
