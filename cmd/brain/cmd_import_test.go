package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/config/identitytest"
)

// TestInitMarksOnlyAFreshBrainLive catches a new person's brain staying in shadow for good (no cut-over ever writes
// .brain/live, so hooks inject nothing, install skips autoMemoryDirectory, backfill only stages CARDs), init
// re-stamping an existing brain's go-live time, or a brain with shadow doc roots going live before its cut-over.
func TestInitMarksOnlyAFreshBrainLive(t *testing.T) {
	cases := []struct {
		name, identity, live, want string
	}{
		{"fresh", `{"shadow_docs": []}`, "", "stamped"},
		{"already live", `{"shadow_docs": []}`, "2026-10-08T13:43:27Z\n", "2026-10-08T13:43:27Z\n"},
		{"before its cut-over", `{"shadow_docs": ["/elsewhere/docs"]}`, "", ""},
	}
	for _, tc := range cases {
		home := identitytest.Write(t, tc.identity)
		root := filepath.Join(home, "brain")
		t.Setenv("BRAIN_ROOT", root)
		live := filepath.Join(root, ".brain", "live")
		if tc.live != "" {
			_ = os.MkdirAll(filepath.Dir(live), 0o755)
			_ = os.WriteFile(live, []byte(tc.live), 0o644)
		}
		if code := cmdInit(nil); code != 0 {
			t.Fatalf("%s: init exit %d", tc.name, code)
		}
		raw, err := os.ReadFile(live)
		got := string(raw)
		if _, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(got)); tc.want == "stamped" && err == nil && parseErr == nil {
			continue
		}
		if got != tc.want {
			t.Errorf("%s: .brain/live = %q (%v), want %q", tc.name, got, err, tc.want)
		}
	}
}

// TestInitSeedsGenericHubsOnAFreshBrainOnly catches a new person's brain starting with the owner's work topics
// (image lane, identity resolution …) as hubs, a seeded home page linking hubs that were not seeded, and init adding
// hubs to a brain that already has its own.
func TestInitSeedsGenericHubsOnAFreshBrainOnly(t *testing.T) {
	home := identitytest.Write(t, `{}`)
	root := filepath.Join(home, "brain")
	t.Setenv("BRAIN_ROOT", root)
	if code := cmdInit(nil); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	seeded, _ := filepath.Glob(filepath.Join(root, "hubs", "*.md"))
	if len(seeded) < 3 {
		t.Fatalf("seeded %v", seeded)
	}
	for _, owners := range []string{"image-lane", "identity-resolution", "multimodal", "nas"} {
		if _, err := os.Stat(filepath.Join(root, "hubs", owners+".md")); err == nil {
			t.Errorf("seeded the owner's hub %s", owners)
		}
	}
	index, _ := os.ReadFile(filepath.Join(root, "hubs", "index.md"))
	for _, m := range regexp.MustCompile(`\]\(([^)]+\.md)\)`).FindAllStringSubmatch(string(index), -1) {
		if _, err := os.Stat(filepath.Join(root, "hubs", m[1])); err != nil {
			t.Errorf("index.md links %s, which was not seeded", m[1])
		}
	}

	home = identitytest.Write(t, `{}`)
	root = filepath.Join(home, "brain")
	t.Setenv("BRAIN_ROOT", root)
	_ = os.MkdirAll(filepath.Join(root, "hubs"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "hubs", "my-topic.md"), []byte("---\nid: my-topic\ntype: hub\n---\n"), 0o644)
	if code := cmdInit(nil); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	if hubs, _ := filepath.Glob(filepath.Join(root, "hubs", "*.md")); len(hubs) != 1 {
		t.Errorf("an existing brain got hubs: %v", hubs)
	}
}
