// Package skills carries the brain's skills (markdown prompts) inside the binary, so `brain file` needs no checkout.
package skills

import "embed"

//go:embed *.md
var FS embed.FS
