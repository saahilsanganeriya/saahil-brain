package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"
)

// Identity is everything per-person: ~/.config/brain/identity.json, the same file on both machines. A missing file
// or key falls back to a value derived from this machine, so the owner's file pins today's values and a new person
// runs on derived ones until `brain identity --init`.
type Identity struct {
	User          string   `json:"user,omitempty"` // the laptop login
	LaptopHome    string   `json:"laptop_home,omitempty"`
	DevboxHome    string   `json:"devbox_home,omitempty"`
	DevboxHost    string   `json:"devbox_host,omitempty"` // ssh alias
	OrcaEnv       string   `json:"orca_env,omitempty"`    // the devbox's orca serve as the laptop's Orca app knows it
	WorkRepo      string   `json:"work_repo,omitempty"`   // default checkout for brain agents new
	WorkRepoOrca  string   `json:"work_repo_orca,omitempty"`
	BrainRepo     string   `json:"brain_repo,omitempty"` // the brain code on the devbox (brain-care)
	AWSSSOSession string   `json:"aws_sso_session,omitempty"`
	GithubCert    string   `json:"github_cert,omitempty"`
	DefaultPRRepo string   `json:"default_pr_repo,omitempty"` // owner/repo of a bare "PR #123"
	ShadowDocs    []string `json:"shadow_docs"`               // read-only doc roots shown as work/ before a cut-over; none when empty
}

// Pairs are the settings as json key and display value, in the file's order.
func (id Identity) Pairs() [][2]string {
	return [][2]string{{"user", id.User}, {"laptop_home", id.LaptopHome}, {"devbox_home", id.DevboxHome},
		{"devbox_host", id.DevboxHost}, {"orca_env", id.OrcaEnv}, {"work_repo", id.WorkRepo},
		{"work_repo_orca", id.WorkRepoOrca}, {"brain_repo", id.BrainRepo}, {"aws_sso_session", id.AWSSSOSession},
		{"github_cert", id.GithubCert}, {"default_pr_repo", id.DefaultPRRepo},
		{"shadow_docs", "[" + strings.Join(id.ShadowDocs, ", ") + "]"}}
}

// IdentityStatus is the effective identity, the file it was read from, which keys that file set, the keys only
// scripts use (devbox_region …), and why the file could not be read (the values are then all derived).
type IdentityStatus struct {
	Path     string
	Err      error
	Identity Identity
	FromFile map[string]bool
	Extra    map[string]json.RawMessage
}

// IdentityPath is ~/.config/brain/identity.json: under BRAIN_HOME in tests, else under the login's own home (on the
// devbox that is the same folder as the laptop spelling, through the bind mount).
func IdentityPath() string {
	home := os.Getenv("BRAIN_HOME")
	if home == "" {
		home = osHome()
	}
	return filepath.Join(home, ".config", "brain", "identity.json")
}

var identityCache struct {
	sync.Mutex
	key    string
	status *IdentityStatus
}

// ReadIdentityStatus is cached per environment (tests switch homes); a broken file is reported once on stderr.
func ReadIdentityStatus() IdentityStatus {
	key := strings.Join([]string{os.Getenv("BRAIN_HOME"), os.Getenv("HOME"), os.Getenv("USER"), os.Getenv("BRAIN_MACHINE")}, "\x00")
	identityCache.Lock()
	defer identityCache.Unlock()
	if identityCache.status == nil || identityCache.key != key {
		st := resolveIdentity()
		if st.Err != nil {
			fmt.Fprintf(os.Stderr, "brain: %v; using derived settings (brain identity)\n", st.Err)
		}
		identityCache.key, identityCache.status = key, &st
	}
	return *identityCache.status
}

// ReloadIdentity drops the cached identity, so the next read sees the file as it is now.
func ReloadIdentity() {
	identityCache.Lock()
	identityCache.status = nil
	identityCache.Unlock()
}

func CurrentIdentity() Identity { return ReadIdentityStatus().Identity }

func LaptopHome() string { return CurrentIdentity().LaptopHome }
func DevboxHome() string { return CurrentIdentity().DevboxHome }
func DevboxHost() string { return CurrentIdentity().DevboxHost }

// ShadowDocRoot is the first shadow doc root, the one single-root readers use; "" when there is none.
func ShadowDocRoot() string {
	if docs := CurrentIdentity().ShadowDocs; len(docs) > 0 {
		return docs[0]
	}
	return ""
}

// PersonalWords are the lowercase words of the login and both home folders' names: in every path a prompt pastes, so
// they say nothing about the work.
func PersonalWords() []string {
	id := CurrentIdentity()
	var out []string
	for _, s := range []string{id.User, filepath.Base(id.LaptopHome), filepath.Base(id.DevboxHome)} {
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if len(w) > 1 && !slices.Contains(out, w) {
				out = append(out, w)
			}
		}
	}
	return out
}

// InitIdentity writes identity.json: each value in set (json key → value) unless the file already holds that key and
// force is false, every other value pinned as it is now, and the keys the brain does not use kept. work_repo_orca is
// written only when the file or set has it, so by default it follows work_repo. It returns the keys it kept.
func InitIdentity(set map[string]string, force bool) (kept []string, err error) {
	st := resolveIdentity()
	if st.Err != nil {
		return nil, fmt.Errorf("%w (fix or remove it first)", st.Err)
	}
	var fromFile Identity
	for _, kv := range st.Identity.Pairs() {
		if v, ok := set[kv[0]]; ok && v != "" && (!st.FromFile[kv[0]] || force) {
			setField(&fromFile, kv[0], v)
		} else if st.FromFile[kv[0]] {
			if v != "" && v != kv[1] {
				kept = append(kept, kv[0])
			}
			setField(&fromFile, kv[0], kv[1])
		}
	}
	fromFile.ShadowDocs = st.Identity.ShadowDocs
	next := withDerived(fromFile)
	if fromFile.WorkRepoOrca == "" {
		next.WorkRepoOrca = ""
	}
	if next.ShadowDocs == nil {
		next.ShadowDocs = []string{}
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("{")
	sep := "\n"
	write := func(key string, value json.RawMessage) {
		fmt.Fprintf(&b, "%s  %q: %s", sep, key, value)
		sep = ",\n"
	}
	for _, kv := range next.Pairs() {
		if v, ok := fields[kv[0]]; ok {
			write(kv[0], v)
		}
	}
	extra := make([]string, 0, len(st.Extra))
	for k := range st.Extra {
		extra = append(extra, k)
	}
	slices.Sort(extra)
	for _, k := range extra {
		write(k, st.Extra[k])
	}
	b.WriteString("\n}\n")
	if err := os.MkdirAll(filepath.Dir(st.Path), 0o755); err != nil {
		return nil, err
	}
	tmp := st.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	defer ReloadIdentity()
	return kept, os.Rename(tmp, st.Path)
}

// setField sets one string setting by its json key.
func setField(id *Identity, key, value string) {
	fields := map[string]*string{"user": &id.User, "laptop_home": &id.LaptopHome, "devbox_home": &id.DevboxHome,
		"devbox_host": &id.DevboxHost, "orca_env": &id.OrcaEnv, "work_repo": &id.WorkRepo, "work_repo_orca": &id.WorkRepoOrca,
		"brain_repo": &id.BrainRepo, "aws_sso_session": &id.AWSSSOSession, "github_cert": &id.GithubCert,
		"default_pr_repo": &id.DefaultPRRepo}
	if f, ok := fields[key]; ok {
		*f = value
	}
}

func resolveIdentity() IdentityStatus {
	st := IdentityStatus{Path: IdentityPath(), FromFile: map[string]bool{}, Extra: map[string]json.RawMessage{}}
	var file Identity
	if raw, err := os.ReadFile(st.Path); err == nil {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			st.Err = fmt.Errorf("%s: %w", st.Path, err)
		} else if err := json.Unmarshal(raw, &file); err != nil {
			st.Err = fmt.Errorf("%s: %w", st.Path, err)
			file = Identity{}
		} else {
			for _, kv := range file.Pairs() {
				_, present := keys[kv[0]]
				st.FromFile[kv[0]] = present && (kv[0] == "shadow_docs" || kv[1] != "")
				delete(keys, kv[0])
			}
			st.Extra = keys
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		st.Err = err
	}
	st.Identity = withDerived(file)
	return st
}

// withDerived fills each empty value from this machine. Paths under the home follow BRAIN_HOME, so a test's derived
// repos and certs stay inside its temp home.
func withDerived(id Identity) Identity {
	fill := func(v *string, derived func() string) {
		if *v == "" {
			*v = derived()
		} else if strings.HasPrefix(*v, "/") {
			*v = filepath.Clean(*v)
		}
	}
	fill(&id.User, loginName)
	fill(&id.LaptopHome, func() string {
		if ThisMachine() == Laptop {
			return osHome()
		}
		return "/Users/" + id.User
	})
	fill(&id.DevboxHome, func() string {
		if ThisMachine() == Devbox {
			return osHome()
		}
		return "/home/" + id.User
	})
	home := id.LaptopHome
	if h := os.Getenv("BRAIN_HOME"); h != "" {
		home = h
	}
	fill(&id.DevboxHost, func() string { return id.User + "-devbox" })
	fill(&id.WorkRepo, func() string { return filepath.Join(home, "Documents", "work-repo") })
	fill(&id.WorkRepoOrca, func() string { return "path:" + id.WorkRepo })
	fill(&id.BrainRepo, func() string { return filepath.Join(home, "Documents", "saahil-brain") })
	fill(&id.AWSSSOSession, func() string { return "Example-Session" })
	fill(&id.GithubCert, func() string { return filepath.Join(home, ".ssh", "work_github-cert.pub") })
	fill(&id.DefaultPRRepo, func() string { return "example-org/work-repo" })
	return id
}

func loginName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func osHome() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Clean(h)
	}
	return "/home/" + loginName()
}
