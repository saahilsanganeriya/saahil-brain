package scan

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
)

// unisonCopyMark is the part Unison adds to a file's name when it keeps the losing side of a conflict.
var unisonCopyMark = regexp.MustCompile(` \(conflict[^)]*\)`)

// settledGitTimeout bounds one git call on the brain's history.
const settledGitTimeout = 20 * time.Second

// ArchiveSettledConflicts moves into .archive/conflicts/<machine>/ each Unison conflict copy that holds nothing its
// live file lacks: the same bytes, lines the live file still has in the same order, or (where the brain keeps git
// history) an earlier committed version of that file. Such a copy is the other machine's older replica, not an edit.
// A copy with content of its own, or with no live file, stays for the owner.
func ArchiveSettledConflicts(root string, now time.Time) (int, error) {
	dest := filepath.Join(root, ".archive", "conflicts", string(config.ThisMachine()))
	found, _ := ConflictCopies(root, ConflictWalkers)
	var moves [][2]string
	for _, rel := range found {
		liveRel := filepath.Join(filepath.Dir(rel), unisonCopyMark.ReplaceAllString(filepath.Base(rel), ""))
		if liveRel == rel {
			continue
		}
		copyBody, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		liveBody, err := os.ReadFile(filepath.Join(root, liveRel))
		if err != nil {
			continue
		}
		if linesWithin(copyBody, liveBody) || committedVersion(root, rel, liveRel) {
			moves = append(moves, [2]string{filepath.Join(root, rel), filepath.Join(dest, rel)})
		}
	}
	return moveEntries(dest, root, moves, now)
}

// linesWithin reports whether every line of part is in whole, in the same order.
func linesWithin(part, whole []byte) bool {
	rest := bytes.Split(whole, []byte("\n"))
	for _, line := range bytes.Split(part, []byte("\n")) {
		i := 0
		for i < len(rest) && !bytes.Equal(rest[i], line) {
			i++
		}
		if i == len(rest) {
			return false
		}
		rest = rest[i+1:]
	}
	return true
}

// committedVersion reports whether the copy's bytes are a version of the live path in the brain's git history.
func committedVersion(root, copyRel, liveRel string) bool {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), settledGitTimeout)
	defer cancel()
	blob, err := exec.CommandContext(ctx, "git", "-C", root, "hash-object", "--", copyRel).Output()
	if err != nil {
		return false
	}
	raw, err := exec.CommandContext(ctx, "git", "-C", root, "log", "--format=", "--raw", "--no-abbrev", "--", liveRel).Output()
	if err != nil {
		return false
	}
	want := strings.TrimSpace(string(blob))
	for _, line := range strings.Split(string(raw), "\n") {
		// ":<mode> <mode> <old blob> <new blob> <status>\t<path>"
		if f := strings.Fields(line); len(f) >= 4 && (f[2] == want || f[3] == want) {
			return true
		}
	}
	return false
}
