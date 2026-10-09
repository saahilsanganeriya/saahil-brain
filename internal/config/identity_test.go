package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"saahil.local/brain/internal/config/identitytest"
)

// TestOwnerIdentityKeepsTodaysValues catches a value the owner's identity.json sets being lost to a derived default
// (a json tag misspelled, a key skipped): his machines would move to /Users/saahil, devbox-host and an
// empty Orca environment.
func TestOwnerIdentityKeepsTodaysValues(t *testing.T) {
	identitytest.Owner(t)
	st := ReadIdentityStatus()
	if st.Err != nil {
		t.Fatal(st.Err)
	}
	for _, kv := range st.Identity.Pairs() {
		if !st.FromFile[kv[0]] {
			t.Errorf("%s = %q is derived, want it from the file", kv[0], kv[1])
		}
	}
	id := CurrentIdentity()
	want := Identity{User: "saahilsanganeriya", LaptopHome: "/Users/test-user", DevboxHome: "/home/test-user",
		DevboxHost: "devbox-host", OrcaEnv: "00000000-0000-4000-8000-0000000000e1",
		WorkRepo: "/Users/test-user/Documents/work-repo", WorkRepoOrca: "id:00000000-0000-4000-8000-0000000000a1",
		BrainRepo: "/Users/test-user/Documents/saahil-brain", AWSSSOSession: "Example-Session",
		GithubCert: "/Users/test-user/.ssh/work_github-cert.pub", DefaultPRRepo: "example-org/work-repo",
		ShadowDocs: []string{}}
	if !identical(id, want) {
		t.Errorf("identity = %+v\nwant       %+v", id, want)
	}
	if LaptopHome() != want.LaptopHome || DevboxHome() != want.DevboxHome || DevboxHost() != want.DevboxHost {
		t.Errorf("accessors = %s %s %s", LaptopHome(), DevboxHome(), DevboxHost())
	}
	if got := PersonalWords(); !slices.Equal(got, []string{"saahilsanganeriya", "saahil"}) {
		t.Errorf("PersonalWords = %v", got)
	}
}

// TestIdentityDerivedDefaults catches a new person's machines breaking before identity.json exists (or with a file
// holding only some keys, or a broken one): every value must still come out, each from this machine's own login and
// home, a test home (BRAIN_HOME) must keep every derived path inside it, and no team value (the work repo, an SSO
// session, a cert, a PR repo) may come from code.
func TestIdentityDerivedDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	t.Setenv("USER", "test-user")
	t.Setenv("HOME", "/home/test-user")
	t.Setenv("BRAIN_MACHINE", "devbox")
	want := Identity{User: "test-user", LaptopHome: "/Users/test-user", DevboxHome: "/home/test-user", DevboxHost: "test-user-devbox",
		BrainRepo: filepath.Join(home, "Documents", "saahil-brain")}
	if got := CurrentIdentity(); !identical(got, want) {
		t.Errorf("devbox, no file: %+v\nwant              %+v", got, want)
	}

	t.Setenv("BRAIN_MACHINE", "laptop")
	t.Setenv("HOME", "/Users/test-user")
	if LaptopHome() != "/Users/test-user" || DevboxHome() != "/home/test-user" {
		t.Errorf("laptop, no file: homes %s %s", LaptopHome(), DevboxHome())
	}

	writeIdentityFile(t, home, `{"devbox_host": "box-1", "laptop_home": "/Users/test-user/", "work_repo": "/w/repo", "someday": 1}`)
	if got := CurrentIdentity().WorkRepoOrca; got != "path:/w/repo" {
		t.Errorf("work_repo_orca = %q, want it to follow work_repo", got)
	}
	st := ReadIdentityStatus()
	if st.Err != nil || DevboxHost() != "box-1" || !st.FromFile["devbox_host"] || st.FromFile["devbox_home"] {
		t.Errorf("partial file: host %s, err %v, from file %v", DevboxHost(), st.Err, st.FromFile)
	}
	if Canonical("/home/test-user/brain/x.md") != "/Users/test-user/brain/x.md" {
		t.Errorf("a home with a trailing slash broke Canonical: %s", Canonical("/home/test-user/brain/x.md"))
	}

	writeIdentityFile(t, home, `{"devbox_host": `)
	if st := ReadIdentityStatus(); st.Err == nil || DevboxHost() != "test-user-devbox" {
		t.Errorf("broken file: err %v, host %s (want an error and the derived host)", st.Err, DevboxHost())
	}
}

func writeIdentityFile(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".config", "brain", "identity.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ReloadIdentity()
}

func identical(a, b Identity) bool { return slices.Equal(a.Pairs(), b.Pairs()) }
