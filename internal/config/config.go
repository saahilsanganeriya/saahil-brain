// Package config holds the brain's fixed places, caps and machine identity.
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Both machines see the owner's home at LaptopHome: the devbox bind-mounts DevboxHome there, so every stored path
// uses the laptop spelling and stays valid on either machine.
const (
	LaptopHome = "/Users/test-user"
	DevboxHome = "/home/test-user"
	DevboxHost = "devbox-host"
)

type Machine string

const (
	Laptop Machine = "laptop"
	Devbox Machine = "devbox"
)

// ThisMachine is decided by the OS; BRAIN_MACHINE overrides it in tests.
func ThisMachine() Machine {
	if m := os.Getenv("BRAIN_MACHINE"); m != "" {
		return Machine(m)
	}
	if runtime.GOOS == "darwin" {
		return Laptop
	}
	return Devbox
}

func (m Machine) Other() Machine {
	if m == Laptop {
		return Devbox
	}
	return Laptop
}

// Canonical maps either spelling of a home path to the laptop spelling and cleans it.
func Canonical(path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if clean == DevboxHome || strings.HasPrefix(clean, DevboxHome+"/") {
		return LaptopHome + strings.TrimPrefix(clean, DevboxHome)
	}
	return clean
}

// Home is the canonical home; fixed rather than $HOME so tests and hooks under odd environments agree.
func Home() string {
	if h := os.Getenv("BRAIN_HOME"); h != "" {
		return h
	}
	return LaptopHome
}

// BrainRoot is ~/brain unless BRAIN_ROOT points elsewhere (tests, shadow experiments).
func BrainRoot() string {
	if r := os.Getenv("BRAIN_ROOT"); r != "" {
		return r
	}
	return filepath.Join(Home(), "brain")
}

func StateDir() string  { return filepath.Join(Home(), ".local", "state", "brain") }
func ConfigDir() string { return filepath.Join(Home(), ".config", "brain") }
func AgentsDir() string { return filepath.Join(Home(), ".local", "state", "agents") }
func BinDir() string    { return filepath.Join(Home(), ".local", "bin") }

// InboxDir holds notes for one session, keyed by its id so a note never reaches another session in the same folder.
func InboxDir(sessionID string) string {
	return filepath.Join(AgentsDir(), "inbox", sessionID)
}

func ClaudeDir() string { return filepath.Join(Home(), ".claude") }

// DistDir on the devbox holds the binaries and the laptop bootstrap the laptop pulls over its own ssh.
func DistDir() string { return filepath.Join(Home(), ".local", "share", "brain", "dist") }

const (
	BridgePort      = 7722 // laptop 127.0.0.1, and the devbox end of the laptop's reverse forward
	BridgeTokenFile = "bridge.token"
)

func BridgeTokenPath() string { return filepath.Join(ConfigDir(), BridgeTokenFile) }

// Caps from the design; the guard and lint enforce them.
const (
	MemoryIndexMaxLines = 200
	MemoryIndexMaxBytes = 24 * 1024 // Claude Code loads 24.4 KB of MEMORY.md and cuts the rest
	DigestMaxChars      = 8000
	MaxPointers         = 3
	PointerMaxTokens    = 300
	HotMaxWords         = 500
	HubMaxLines         = 200
	StateMaxBytes       = 8 * 1024
	CardMaxBytes        = 3 * 1024
	NoteMaxBytes        = 4 * 1024
	SummaryMaxChars     = 160
	LessonMaxChars      = 300
	SectionWarnBytes    = 20 * 1024
	BigDocBytes         = 50 * 1024
)

const (
	ModelJudgment = "claude-opus-5-5"
	ModelBulk     = "claude-haiku-5-5"
)

// SyncBulkDirs are folder-name patterns (filepath.Match) the sync never carries, wherever they sit: bulk, caches and
// environments. Nothing under them is synced, so nothing under them can be a sync conflict copy either.
var SyncBulkDirs = []string{"artifacts", ".sync-backups", "ctx", "target*", "*.egg-info", "node_modules", "__pycache__", ".venv", "venv"}

// IsSyncBulkDir reports whether a folder name matches SyncBulkDirs.
func IsSyncBulkDir(name string) bool {
	for _, pat := range SyncBulkDirs {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// SyncWorkTextExts are the only file extensions the sync carries under work/ (bulk stays on the machine that made it).
var SyncWorkTextExts = []string{"md", "txt", "py", "sh", "js", "toml", "yml", "yaml"}

// SyncCarries reports whether the sync would bring a brain-relative path to the other machine (size aside): not under
// a bulk folder, and under work/ only text or an extensionless file.
func SyncCarries(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, part := range strings.Split(rel, "/") {
		if IsSyncBulkDir(part) {
			return false
		}
	}
	if !strings.HasPrefix(rel, "work/") {
		return true
	}
	ext := strings.TrimPrefix(filepath.Ext(strings.SplitN(rel, "#", 2)[0]), ".")
	return ext == "" || slices.Contains(SyncWorkTextExts, ext)
}

// IsSyncConflictCopy names a conflict copy left by a sync: Unison's "<file> (conflict…_on_<date>…)", the cut-over
// reconcile's "<file>.conflict-<machine>-<time>", or Unison's temporary ".unison.*" files.
func IsSyncConflictCopy(name string) bool {
	return strings.Contains(name, " (conflict") || strings.Contains(name, ".conflict-laptop-") ||
		strings.Contains(name, ".conflict-devbox-") || strings.HasPrefix(name, ".unison.")
}
