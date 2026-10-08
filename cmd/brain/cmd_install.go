package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/install"
)

func init() {
	register("install", "wire the brain into every agent host on this machine: [--dry-run] [--verify] [--off <host>] [--host h,…]", cmdInstall)
}

func cmdInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "change nothing; print the manifest the install would write")
	verify := fs.Bool("verify", false, "re-check each host; exit 1 when anything is missing or in the way")
	off := fs.String("off", "", "remove what the manifest says the installer added to this host ("+strings.Join(install.HostNames(), "|")+")")
	hosts := fs.String("host", "", "only these hosts, comma-separated")
	repo := fs.String("repo", install.ShareDir(), "where the mod/ the Claude Code marketplace links to lives (default: laid out from this binary)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	*repo = config.Canonical(*repo)
	if !*dryRun && *off == "" {
		// ~/.claude/rules/brain.md @-imports BRAIN.md: keep it at this binary's version
		if changed, err := install.WriteBrainDoc(config.BrainRoot()); err != nil {
			return fail("BRAIN.md: %v", err)
		} else if changed {
			fmt.Println("BRAIN.md: laid out in", config.BrainRoot())
		}
	}
	if *repo == install.ShareDir() && !*dryRun && *off == "" {
		if n, err := install.ExtractMod(*repo); err != nil {
			return fail("lay out the mod: %v", err)
		} else if n > 0 {
			fmt.Printf("mod: %d files laid out in %s/mod\n", n, *repo)
		}
	}
	var only []string
	if *hosts != "" {
		only = strings.Split(*hosts, ",")
	}
	if *verify {
		return printVerify(install.Verify(*repo, only))
	}
	manifestPath := install.ManifestPath()
	m, err := install.LoadManifest(manifestPath)
	if err != nil {
		return fail("manifest %s: %v", manifestPath, err)
	}
	now := time.Now()
	var reports []install.Report
	if *off != "" {
		if !slices.Contains(install.HostNames(), *off) {
			return fail("unknown host %q (one of %s)", *off, strings.Join(install.HostNames(), ", "))
		}
		rep, err := install.Off(m, *off, now, *dryRun)
		if err != nil {
			return fail("off %s: %v", *off, err)
		}
		reports = []install.Report{rep}
	} else if reports, err = install.Install(m, *repo, only, now, *dryRun); err != nil {
		return fail("%v", err)
	}
	for _, rep := range reports {
		fmt.Println(summarize(rep))
		for _, c := range rep.Conflicts {
			fmt.Println("  left alone:", c)
		}
	}
	if *dryRun {
		out, _ := json.MarshalIndent(m, "", "  ")
		fmt.Printf("\ndry run, nothing written; the manifest would be (%s):\n%s\n", manifestPath, out)
		return 0
	}
	if err := m.Save(manifestPath); err != nil {
		return fail("manifest: %v", err)
	}
	// after the manifest is saved: a failing claude CLI must not lose the record of what install already changed
	if *off == "" {
		if msg, err := install.EnsureClaudePlugin(); err != nil {
			return fail("claude plugin: %v", err)
		} else if msg != "" {
			fmt.Println(msg)
		}
	}
	fmt.Println("manifest:", manifestPath)
	return 0
}

func summarize(rep install.Report) string {
	switch {
	case !rep.Present:
		return rep.Host + ": not installed here, skipped"
	case len(rep.Removed) > 0:
		return fmt.Sprintf("%s: removed %d entries, %d files changed", rep.Host, len(rep.Removed), len(rep.Changes))
	case len(rep.Added) == 0 && len(rep.Changes) == 0:
		return rep.Host + ": already in place"
	}
	var lines []string
	for _, e := range rep.Added {
		lines = append(lines, "  + "+describe(e))
	}
	return fmt.Sprintf("%s: %d added, %d files changed\n%s", rep.Host, len(rep.Added), len(rep.Changes), strings.Join(lines, "\n"))
}

func describe(e install.Entry) string {
	switch e.Kind {
	case "hook", "cursor-hook":
		return fmt.Sprintf("%s %s %s → %s", e.Path, e.Event, e.Matcher, e.Command)
	case "json-key", "json-item":
		return fmt.Sprintf("%s %s %s", e.Path, strings.Join(e.Key, "."), e.Value)
	case "symlink":
		return e.Path + " → " + e.Target
	}
	return strings.TrimSpace(e.Path + " " + e.Text)
}

func printVerify(reports []install.Report) int {
	code := 0
	for _, rep := range reports {
		switch {
		case !rep.Present:
			fmt.Printf("skip  %s: not installed here\n", rep.Host)
		case len(rep.Added) == 0 && len(rep.Conflicts) == 0:
			fmt.Printf("ok    %s: %s\n", rep.Host, strings.Join(rep.Labels, ", "))
		default:
			code = 1
			fmt.Printf("fail  %s: missing %s\n", rep.Host, strings.Join(rep.Missing, ", "))
			for _, e := range rep.Added {
				fmt.Println("        missing", describe(e))
			}
			for _, c := range rep.Conflicts {
				fmt.Println("        ", c)
			}
		}
	}
	if os.Getenv("BRAIN_OFF") == "1" {
		fmt.Println("BRAIN_OFF=1 is set: every brain hook is a no-op in this environment")
	}
	return code
}
