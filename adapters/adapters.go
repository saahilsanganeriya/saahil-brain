// Package adapters holds each host's wiring as data: the hook lists, MCP entries and extension files that
// `brain install` merges into Codex, Cursor, prime-agent and oh-my-pi. Embedded, so the binary installs without the repo.
package adapters

import "embed"

//go:embed codex/hooks.json codex/mcp.toml cursor/hooks.json cursor/mcp.json prime/brain.ts omp/brain.ts
var Files embed.FS
