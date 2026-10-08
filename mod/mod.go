// Package mod carries the Claude Code plugin inside the brain binary, so `brain install` lays it out on each machine
// without a checkout of this repo.
package mod

import "embed"

//go:embed .claude-plugin/plugin.json hooks/hooks.json hooks/register.js README.md
var Files embed.FS
