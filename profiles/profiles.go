// Package profiles carries the team settings beside the code (one <team>.json each), built into the binary so
// `brain identity --init --profile <team>` works without a checkout. Git keeps only example.json; a team's own file
// stays in its checkout (.gitignore) and reaches colleagues inside the binaries of a release kit. A value may name <home>, <laptop_home>,
// <devbox_home> or <user>; --init fills them with the person's own.
package profiles

import "embed"

//go:embed *.json
var FS embed.FS
