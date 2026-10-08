package install

import (
	"io/fs"
	"os"
	"path/filepath"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/mod"
)

// ShareDir holds what the binary lays out for hosts; its mod/ is what the Claude Code marketplace links to.
func ShareDir() string { return filepath.Join(config.Home(), ".local", "share", "brain") }

// ExtractMod writes the embedded plugin into <dir>/mod, replacing files whose content changed (atomic per file).
func ExtractMod(dir string) (changed int, err error) {
	root := filepath.Join(dir, "mod")
	err = fs.WalkDir(mod.Files, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		data, err := mod.Files.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, path)
		if old, err := os.ReadFile(dst); err == nil && string(old) == string(data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".brain-tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		changed++
		return os.Rename(tmp, dst)
	})
	return changed, err
}
