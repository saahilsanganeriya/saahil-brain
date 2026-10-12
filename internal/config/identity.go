package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"saahil.local/brain/profiles"
)

// Identity is everything per-person: ~/.config/brain/identity.json, the same file on both machines. A missing file
// or key falls back to a value derived from this machine (or none, for team values), so the owner's file pins today's
// values and a new person runs on derived ones until `brain identity --init [--profile <team>]`.
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
	// TicketPrefixes are the tracker's team keys: "ABC" makes ABC-123 a ticket mention. None: no ticket mentions.
	TicketPrefixes []string `json:"ticket_prefixes,omitempty"`
	// LegacyDocs are old doc-root spellings that history keeps, as path fragments mapped under work/ (LegacyDocsPath).
	LegacyDocs []string `json:"legacy_docs,omitempty"`
}

// Pairs are the settings as json key and display value, in the file's order.
func (id Identity) Pairs() [][2]string {
	return [][2]string{{"user", id.User}, {"laptop_home", id.LaptopHome}, {"devbox_home", id.DevboxHome},
		{"devbox_host", id.DevboxHost}, {"orca_env", id.OrcaEnv}, {"work_repo", id.WorkRepo},
		{"work_repo_orca", id.WorkRepoOrca}, {"brain_repo", id.BrainRepo}, {"aws_sso_session", id.AWSSSOSession},
		{"github_cert", id.GithubCert}, {"default_pr_repo", id.DefaultPRRepo},
		{"shadow_docs", "[" + strings.Join(id.ShadowDocs, ", ") + "]"},
		{"ticket_prefixes", "[" + strings.Join(id.TicketPrefixes, ", ") + "]"},
		{"legacy_docs", "[" + strings.Join(id.LegacyDocs, ", ") + "]"}}
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

// LoadProfile finds a team profile: name as a path to a .json file; else <config>/profiles/<name>.json (the person's
// own copy wins); else the profiles built into this binary from the repo's profiles/. It returns the values and where
// they came from.
func LoadProfile(name string) (map[string]json.RawMessage, string, error) {
	var raw []byte
	var from string
	own := filepath.Join(filepath.Dir(IdentityPath()), "profiles", name+".json")
	if strings.HasSuffix(name, ".json") || strings.Contains(name, "/") {
		own = name
	}
	if b, err := os.ReadFile(own); err == nil {
		raw, from = b, own
	} else if b, err := profiles.FS.ReadFile(name + ".json"); err == nil && !strings.Contains(name, "/") {
		raw, from = b, "built-in profiles/"+name+".json"
	} else {
		builtIn, _ := fs.Glob(profiles.FS, "*.json")
		return nil, "", fmt.Errorf("no profile %q: not at %s, and this binary carries %s", name, own, strings.Join(builtIn, ", "))
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, "", fmt.Errorf("%s: %w", from, err)
	}
	return values, from, nil
}

// InitIdentity writes identity.json: each value in set (json key → value) unless the file already holds that key and
// force is false, then the file's values, then the profile's (its <home> … filled with this person's), then derived
// ones; every value is pinned as it is now and the keys the brain does not use are kept. work_repo_orca is written
// only when the file, set or profile has it, so by default it follows work_repo. It returns the keys it kept.
func InitIdentity(set map[string]string, profile map[string]json.RawMessage, force bool) (kept []string, err error) {
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
	fromFile.TicketPrefixes, fromFile.LegacyDocs = st.Identity.TicketPrefixes, st.Identity.LegacyDocs
	homes := withDerived(fromFile)
	fill := strings.NewReplacer("<home>", homes.LaptopHome, "<laptop_home>", homes.LaptopHome,
		"<devbox_home>", homes.DevboxHome, "<user>", homes.User)
	extras, fields := maps.Clone(st.Extra), stringFields(&fromFile)
	for key, raw := range profile {
		var value string
		_ = json.Unmarshal(raw, &value)
		switch {
		case key == "shadow_docs":
			var docs []string
			if !st.FromFile[key] && json.Unmarshal(raw, &docs) == nil {
				for i := range docs {
					docs[i] = fill.Replace(docs[i])
				}
				fromFile.ShadowDocs = docs
			}
		case key == "ticket_prefixes":
			if !st.FromFile[key] {
				_ = json.Unmarshal(raw, &fromFile.TicketPrefixes)
			}
		case key == "legacy_docs":
			if !st.FromFile[key] {
				_ = json.Unmarshal(raw, &fromFile.LegacyDocs)
			}
		case fields[key] != nil:
			if *fields[key] == "" && value != "" {
				*fields[key] = fill.Replace(value)
			}
		case extras[key] == nil:
			if value != "" {
				raw, _ = json.Marshal(fill.Replace(value))
			}
			extras[key] = raw
		}
	}
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
	var written map[string]json.RawMessage
	if err := json.Unmarshal(raw, &written); err != nil {
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
		if v, ok := written[kv[0]]; ok {
			write(kv[0], v)
		}
	}
	extra := slices.Sorted(maps.Keys(extras))
	for _, k := range extra {
		write(k, extras[k])
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

// stringFields are id's string settings by json key.
func stringFields(id *Identity) map[string]*string {
	return map[string]*string{"user": &id.User, "laptop_home": &id.LaptopHome, "devbox_home": &id.DevboxHome,
		"devbox_host": &id.DevboxHost, "orca_env": &id.OrcaEnv, "work_repo": &id.WorkRepo, "work_repo_orca": &id.WorkRepoOrca,
		"brain_repo": &id.BrainRepo, "aws_sso_session": &id.AWSSSOSession, "github_cert": &id.GithubCert,
		"default_pr_repo": &id.DefaultPRRepo}
}

func setField(id *Identity, key, value string) {
	if f := stringFields(id)[key]; f != nil {
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

// withDerived fills each empty value this machine can answer; team values (work_repo, aws_sso_session, github_cert,
// default_pr_repo) stay empty until a profile or the person sets them. Paths under the home follow BRAIN_HOME, so a
// test's derived paths stay inside its temp home.
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
	fill(&id.BrainRepo, func() string { return filepath.Join(home, "Documents", "saahil-brain") })
	if id.WorkRepo != "" {
		fill(&id.WorkRepoOrca, func() string { return "path:" + id.WorkRepo })
	}
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
