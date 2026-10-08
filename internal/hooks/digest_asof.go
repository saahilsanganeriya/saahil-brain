package hooks

// DigestAsOf rebuilds the start digest a past session would have been given: visible says whether a brain path
// existed when it started (a session note, a work box folder) or, for a file the digest quotes (hot.md, STATE.md,
// CARD.md, a hub), whether its text then was today's text. CARD aliases are not used: the replay has no index as of
// that time.
func DigestAsOf(env Env, cwd string, visible func(brainPath string) bool) Digest {
	return buildDigest(env, cwd, nil, visible)
}
