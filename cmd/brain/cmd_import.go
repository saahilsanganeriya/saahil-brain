package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/templates"
)

func init() {
	register("init", "create the ~/brain skeleton on this machine (idempotent); a fresh brain gets the generic hubs and, with no shadow_docs, starts live", cmdInit)
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
	if n, err := seedFreshHubs(root); err != nil {
		return fail("seed hubs: %v", err)
	} else if n > 0 {
		fmt.Printf("seeded %d generic hubs in %s\n", n, filepath.Join(root, "hubs"))
	}
	// a brain with no shadow doc roots has no cut-over ahead of it: it is live from the start
	live := filepath.Join(root, ".brain", "live")
	if _, err := os.Stat(live); errors.Is(err, os.ErrNotExist) && len(config.CurrentIdentity().ShadowDocs) == 0 {
		if err := os.WriteFile(live, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
			return fail("%v", err)
		}
		fmt.Println("brain marked live:", live)
	}
	return 0
}

// freshHubs are the hubs a new brain starts with; the other templates are the owner's topics, which the skills'
// examples cite.
var freshHubs = []string{"brain", "git", "pr-shipping", "devbox-and-laptop", "orca-and-agents", "pii-and-security",
	"explaining-to-the-owner"}

var topHubsRE = regexp.MustCompile(`(?s)(## Top hubs\n\n)(?:- \[[^\n]*\n)+`)

// seedFreshHubs writes freshHubs and a home page listing only them, when hubs/ holds no hub yet.
func seedFreshHubs(root string) (int, error) {
	dir := filepath.Join(root, "hubs")
	if have, _ := filepath.Glob(filepath.Join(dir, "*.md")); len(have) > 0 {
		return 0, nil
	}
	pages := map[string][]byte{}
	list := ""
	for _, id := range freshHubs {
		b, err := fs.ReadFile(templates.FS, "hubs/"+id+".md")
		if err != nil {
			return 0, err
		}
		pages[id], list = b, list+"- ["+id+"]("+id+".md)\n"
	}
	index, err := fs.ReadFile(templates.FS, "hubs/index.md")
	if err != nil {
		return 0, err
	}
	if !topHubsRE.Match(index) {
		return 0, errors.New("templates/hubs/index.md has no Top hubs list to fill")
	}
	pages["index"] = topHubsRE.ReplaceAll(index, []byte("${1}"+list))
	for id, b := range pages {
		if err := os.WriteFile(filepath.Join(dir, id+".md"), b, 0o644); err != nil {
			return 0, err
		}
	}
	return len(pages), nil
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
