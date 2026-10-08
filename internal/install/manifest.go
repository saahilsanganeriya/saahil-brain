package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"saahil.local/brain/internal/config"
)

// Entry is one thing the installer added; `--off` removes exactly these and nothing else.
type Entry struct {
	Kind    string          `json:"kind"` // hook | cursor-hook | json-key | json-item | toml-table | file | symlink | ignore-line
	Path    string          `json:"path"`
	Event   string          `json:"event,omitempty"`
	Matcher string          `json:"matcher,omitempty"`
	Command string          `json:"command,omitempty"`
	Key     []string        `json:"key,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	Text    string          `json:"text,omitempty"` // the TOML table or the ignore line
	Target  string          `json:"target,omitempty"`
	SHA256  string          `json:"sha256,omitempty"` // a file's content as written
}

// FileChange is one write: what the file was before, where that copy is kept, and what it became.
type FileChange struct {
	Path         string    `json:"path"`
	Action       string    `json:"action"` // install | off
	Backup       string    `json:"backup,omitempty"`
	SHA256Before string    `json:"sha256Before,omitempty"`
	SHA256After  string    `json:"sha256After,omitempty"`
	At           time.Time `json:"at"`
}

type HostRecord struct {
	Added   []Entry      `json:"added"`
	Changes []FileChange `json:"changes"`
}

type Manifest struct {
	Version int                    `json:"version"`
	Updated time.Time              `json:"updated"`
	Hosts   map[string]*HostRecord `json:"hosts"`
}

func ManifestPath() string { return filepath.Join(config.StateDir(), "install-manifest.json") }

// LoadManifest reads the manifest; a missing file is an empty one.
func LoadManifest(path string) (*Manifest, error) {
	m := &Manifest{Version: 1, Hosts: map[string]*HostRecord{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, err
	}
	if m.Hosts == nil {
		m.Hosts = map[string]*HostRecord{}
	}
	return m, nil
}

func (m *Manifest) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := marshalJSON(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Manifest) record(host string) *HostRecord {
	if m.Hosts[host] == nil {
		m.Hosts[host] = &HostRecord{}
	}
	return m.Hosts[host]
}

// merge adds new entries; a file entry replaces the older one for the same path (its sha moved on).
func (r *HostRecord) merge(added []Entry) {
	for _, e := range added {
		replaced := false
		for i, old := range r.Added {
			if e.Kind == "file" && old.Kind == "file" && old.Path == e.Path {
				r.Added[i], replaced = e, true
			}
		}
		if !replaced {
			r.Added = append(r.Added, e)
		}
	}
}

func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(v)
	return buf.Bytes(), err
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
