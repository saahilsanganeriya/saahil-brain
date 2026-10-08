package config

import "testing"

// Catches the two machines indexing one file under two paths (devbox /home/test-user vs the laptop spelling).
func TestCanonicalPath(t *testing.T) {
	cases := map[string]string{
		"/home/test-user/brain/notes/x.md":             "/Users/test-user/brain/notes/x.md",
		"/Users/test-user/brain/notes/x.md": "/Users/test-user/brain/notes/x.md",
		"/home/test-user":         "/Users/test-user",
		"/home/test-userx/y":      "/home/test-userx/y",
		"/home/test-user/a/../b/": "/Users/test-user/b",
	}
	for in, want := range cases {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSyncCarries: doctor must agree with the sync about what reaches the other machine, or the laptop reports bulk
// left on the devbox (a work/ .json, an artifacts/ file) as dangling links.
func TestSyncCarries(t *testing.T) {
	for rel, want := range map[string]bool{
		"notes/lesson-x.md": true, "work/box/plans/SPEC.md#goal": true, "work/box/tools/Makefile": true,
		"work/box/evidence/run.json": false, "work/box/artifacts/report.md": false, "work/box/tools/target/x.d": false,
	} {
		if got := SyncCarries(rel); got != want {
			t.Errorf("SyncCarries(%q) = %v, want %v", rel, got, want)
		}
	}
}

// TestLegacyDocsPath: history keeps the old saahil-docs spellings; once the brain went live they stopped being an
// index root, so every un-rewritten doc lost its links and the rewritten ones kept theirs, skewing pointer proximity.
func TestLegacyDocsPath(t *testing.T) {
	for abs, want := range map[string]string{
		"/Users/test-user/Documents/work-repo/saahil-docs/box/plans/SPEC.md":                                "work/box/plans/SPEC.md",
		"/home/test-user/Documents/worktrees/work-repo/image-unified/saahil-docs/box/recon/x.md":                   "work/box/recon/x.md",
		"/Users/test-user/Documents/team-monorepo/apps/server/work-repo/saahil-docs/appliance/a.md":                "work/team-monorepo-saahil-docs-2026-08/appliance/a.md",
		"/Users/test-user/Documents/anonymization/saahil-docs/ir/x.md":                                             "",
		"/Users/test-user/Documents/worktrees/work-repo/astral-knoll/work-repo/saahil-docs/engine/p.md": "work/engine/p.md",
	} {
		got, ok := LegacyDocsPath(abs)
		if got != want || ok != (want != "") {
			t.Errorf("LegacyDocsPath(%q) = %q, %v; want %q", abs, got, ok, want)
		}
	}
}
