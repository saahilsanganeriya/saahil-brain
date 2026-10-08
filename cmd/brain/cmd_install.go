package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"saahil.local/brain/internal/bridge"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/install"
)

func init() {
	register("install", "wire the brain into the agent hosts on this machine (idempotent, backups kept)", cmdInstall)
}

func cmdInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	settings := filepath.Join(config.ClaudeDir(), "settings.json")
	added, err := install.AddClaudeHooks(settings, install.MeshHooks(), time.Now())
	if err != nil {
		return fail("claude hooks: %v", err)
	}
	for _, a := range added {
		fmt.Println("added", a)
	}
	if len(added) == 0 {
		fmt.Println("claude hooks: already installed")
	}
	if err := bridge.InstallSync(); err != nil {
		return fail("sync: %v", err)
	}
	return 0
}
