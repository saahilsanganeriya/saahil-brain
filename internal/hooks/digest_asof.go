package hooks

import (
	"database/sql"

	"saahil.local/brain/internal/config"
)

// DigestAsOf rebuilds the digest a past session held once its first prompt was in: the work that prompt named, else
// its cwd's. visible says whether a brain path existed when it started (a session note, a work box folder) or, for a
// file the digest quotes (hot.md, STATE.md, CARD.md, a hub), whether its text then was today's text. CARD aliases are
// not used: the replay has no index as of that time.
func DigestAsOf(env Env, db *sql.DB, cwd, prompt string, visible func(brainPath string) bool) Digest {
	cwdBoxes := cwdWork(env, cwd, nil, visible)
	work := cwdBoxes
	if q, err := readPrompt(db, prompt, cwdBoxes, nil); err == nil {
		work = pickWork(env, q.namedBoxes(), cwdBoxes, visible)
	}
	return composeDigest(env, startHeader, true, work, config.DigestMaxChars, visible)
}
