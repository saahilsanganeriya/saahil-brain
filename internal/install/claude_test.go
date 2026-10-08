package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Catches the installer clobbering the owner's other hooks/settings, adding a hook twice on re-run, or editing a
// settings file it cannot parse.
func TestAddClaudeHooksKeepsOthersAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/agent-inbox.sh"}]}]}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)
	added, err := AddClaudeHooks(path, MeshHooks(), now)
	if err != nil || len(added) != 3 {
		t.Fatalf("first run added %v, %v", added, err)
	}
	again, err := AddClaudeHooks(path, MeshHooks(), now.Add(time.Minute))
	if err != nil || len(again) != 0 {
		t.Fatalf("second run added %v, %v", again, err)
	}
	var got map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "opus" {
		t.Fatal("other settings lost")
	}
	stop := got["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 2 {
		t.Fatalf("Stop groups = %d, want the old one + ours", len(stop))
	}
	if backups, _ := filepath.Glob(path + ".bak-brain-*"); len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}

	broken := filepath.Join(dir, "broken.json")
	_ = os.WriteFile(broken, []byte(`{"hooks":`), 0o600)
	if _, err := AddClaudeHooks(broken, MeshHooks(), now); err == nil {
		t.Fatal("edited a settings file it could not parse")
	}
}
