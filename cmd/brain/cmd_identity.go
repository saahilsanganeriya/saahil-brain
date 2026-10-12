package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"saahil.local/brain/internal/config"
)

func init() {
	register("identity", "this person's settings and where each comes from (file | derived): "+
		"[--init [--devbox-host H] [--orca-env E] [--work-repo P] [--brain-repo P] [--devbox-home P] [--laptop-home P] "+
		"[--aws-sso-session S] [--github-cert P] [--pr-repo R] [--user U] [--profile <team>] [--force]]", cmdIdentity)
}

// identityFlags maps each --init flag to its identity.json key.
var identityFlags = [][2]string{{"user", "user"}, {"laptop-home", "laptop_home"}, {"devbox-home", "devbox_home"},
	{"devbox-host", "devbox_host"}, {"orca-env", "orca_env"}, {"work-repo", "work_repo"}, {"brain-repo", "brain_repo"},
	{"aws-sso-session", "aws_sso_session"}, {"github-cert", "github_cert"}, {"pr-repo", "default_pr_repo"}}

func cmdIdentity(args []string) int {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	initFile := fs.Bool("init", false, "write ~/.config/brain/identity.json: these flags, then the file's values, then derived ones")
	force := fs.Bool("force", false, "with --init, a flag replaces a value the file already holds")
	profileName := fs.String("profile", "", "with --init, team values under the person's own: a path to a .json, else "+
		"~/.config/brain/profiles/<name>.json, else the binary's built-in profiles/<name>.json")
	values := map[string]*string{}
	for _, f := range identityFlags {
		values[f[1]] = fs.String(f[0], "", "with --init: "+f[1])
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	set := map[string]string{}
	for key, v := range values {
		if *v != "" {
			set[key] = *v
		}
	}
	if !*initFile && (len(set) > 0 || *force || *profileName != "") {
		return fail("identity: the value flags, --profile and --force need --init")
	}
	if *initFile {
		var profile map[string]json.RawMessage
		if *profileName != "" {
			var from string
			var err error
			if profile, from, err = config.LoadProfile(*profileName); err != nil {
				return fail("identity --init: %v", err)
			}
			fmt.Println("profile:", from)
		}
		kept, err := config.InitIdentity(set, profile, *force)
		if err != nil {
			return fail("identity --init: %v", err)
		}
		for _, key := range kept {
			fmt.Printf("kept %s from the file (--force replaces it)\n", key)
		}
		fmt.Println("wrote", config.IdentityPath())
	}
	return printIdentity(config.ReadIdentityStatus())
}

// identityNeeds names what waits on each key that may stay empty.
var identityNeeds = map[string]string{"orca_env": "brain agents on the devbox from the laptop",
	"work_repo": "brain agents new without --repo", "aws_sso_session": "brain machine login aws",
	"github_cert": "the GitHub cert line of brain machine status", "default_pr_repo": "linking a bare 'PR #123' in notes"}

func printIdentity(st config.IdentityStatus) int {
	_, statErr := os.Stat(st.Path)
	switch {
	case st.Err != nil:
		fmt.Printf("%v\nevery value below is derived\n", st.Err)
	case errors.Is(statErr, os.ErrNotExist):
		fmt.Printf("%s: no file; every value is derived (write it: brain identity --init)\n", st.Path)
	default:
		fmt.Println(st.Path)
	}
	for _, kv := range st.Identity.Pairs() {
		source := "derived"
		if st.FromFile[kv[0]] {
			source = "file"
		}
		if need := identityNeeds[kv[0]]; kv[1] == "" && need != "" {
			source += " (empty: " + need + " waits on it)"
		}
		fmt.Printf("%-18s %-60s %s\n", kv[0], cmp.Or(kv[1], "-"), source)
	}
	extra := make([]string, 0, len(st.Extra))
	for key := range st.Extra {
		extra = append(extra, key)
	}
	slices.Sort(extra)
	for _, key := range extra {
		fmt.Printf("%-18s %-60s file (scripts)\n", key, strings.Trim(string(st.Extra[key]), `"`))
	}
	if st.Err != nil {
		return 1
	}
	return 0
}
