package main

import (
	"os"
	"path/filepath"
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
