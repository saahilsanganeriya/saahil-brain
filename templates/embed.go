// Package templates holds what the installer and `brain file --seed-hubs` copy into ~/brain when missing.
package templates

import "embed"

//go:embed BRAIN.md hubs/*.md
var FS embed.FS
