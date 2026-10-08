package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"saahil.local/brain/internal/config"
)

func init() {
	register("init", "create the ~/brain skeleton on this machine (idempotent)", cmdInit)
	register("import memory", "copy (never move) every Claude memory folder into <brain>/.import/<machine>/ with a manifest", cmdImportMemory)
}

var brainDirs = []string{"work", "notes", "hubs", "memory", "sessions/devbox", "sessions/laptop", "journal", "inbox",
	".data", ".index", ".archive", ".brain/evals", ".import"}

func cmdInit([]string) int {
	root := config.BrainRoot()
	for _, d := range brainDirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return fail("%v", err)
		}
	}
	fmt.Println("brain skeleton ready at", root)
	return 0
}

// cmdImportMemory copies each ~/.claude/projects/<slug>/memory/*.md into .import/<machine>/<slug>/ and writes
// <machine>-manifest.tsv (path, bytes, sha256), so the filing step can reconcile counts against the sources.
func cmdImportMemory([]string) int {
	if code := cmdInit(nil); code != 0 {
		return code
	}
	machine := string(config.ThisMachine())
	dest := filepath.Join(config.BrainRoot(), ".import", machine)
	sources, _ := filepath.Glob(filepath.Join(config.ClaudeDir(), "projects", "*", "memory", "*.md"))
	sort.Strings(sources)
	var manifest strings.Builder
	copied := 0
	for _, src := range sources {
		slug := filepath.Base(filepath.Dir(filepath.Dir(src)))
		dst := filepath.Join(dest, slug, filepath.Base(src))
		sum, size, err := copyFile(src, dst)
		if err != nil {
			return fail("copy %s: %v", src, err)
		}
		fmt.Fprintf(&manifest, "%s/%s\t%d\t%s\n", slug, filepath.Base(src), size, sum)
		copied++
	}
	manifestPath := filepath.Join(config.BrainRoot(), ".import", machine+"-manifest.tsv")
	if err := os.WriteFile(manifestPath, []byte(manifest.String()), 0o644); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("imported %d memory files from %s into %s (manifest %s)\n", copied, machine, dest, manifestPath)
	return 0
}

func copyFile(src, dst string) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	out, err := os.Create(dst)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
