package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/guard"
	"saahil.local/brain/internal/lint"
)

func init() {
	register("lint", "check the brain against FORMAT.md: brain lint [paths…] [--json]; exit 0 clean, 1 warnings, 2 errors", cmdLint)
	register("guard", "decide a Write/Edit/Read under the brain: --tool T --path P [--content-file F | stdin], or a PreToolUse hook's JSON on stdin", cmdGuard)
}

func cmdLint(args []string) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON report")
	paths, err := parseAll(fs, args)
	if err != nil {
		return 2
	}
	if len(paths) == 0 {
		paths = []string{config.BrainRoot()}
	}
	var r lint.Report
	for _, p := range paths {
		one, err := lintPath(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brain: lint %s: %v\n", p, err)
			return 2
		}
		r.Merge(one)
	}
	if *asJSON {
		out, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(out))
		return r.ExitCode()
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, is := range r.Issues {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", is.Sev, is.Rule, is.Path, is.Msg)
	}
	tw.Flush()
	fmt.Printf("\n%d files: %d errors, %d warnings, %d dangling\n", r.Files, r.Errors, r.Warnings, r.Dangling)
	return r.ExitCode()
}

// lintPath lints a whole brain (a folder) or one file of it; a file outside the default brain is judged against
// the nearest folder above it that holds notes/ or hubs/.
func lintPath(p string) (lint.Report, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return lint.Report{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return lint.Report{}, err
	}
	if fi.IsDir() {
		return lint.Root(abs)
	}
	root := config.BrainRoot()
	if !strings.HasPrefix(config.Canonical(abs), config.Canonical(root)+"/") {
		root = filepath.Dir(abs)
		for dir := root; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			if isDir(filepath.Join(dir, "notes")) || isDir(filepath.Join(dir, "hubs")) {
				root = dir
				break
			}
		}
	}
	rel, _ := filepath.Rel(config.Canonical(root), config.Canonical(abs))
	return lint.New(root).File(filepath.ToSlash(rel))
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// guardInput is the PreToolUse payload of Claude Code, Codex and the mod: the tool and its input.
type guardInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath     string  `json:"file_path"`
		NotebookPath string  `json:"notebook_path"`
		Content      *string `json:"content"`
		OldString    string  `json:"old_string"`
		NewString    string  `json:"new_string"`
		ReplaceAll   bool    `json:"replace_all"`
		Edits        []struct {
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	} `json:"tool_input"`
}

// cmdGuard prints {decision, reason, append?}. As a hook (JSON on stdin) a block also exits 2 with the reason on
// stderr, which every host reads as a refusal; anything unreadable is allowed.
func cmdGuard(args []string) int {
	fs := flag.NewFlagSet("guard", flag.ContinueOnError)
	tool := fs.String("tool", "", "Write | Edit | Read")
	path := fs.String("path", "", "the file the tool touches")
	contentFile := fs.String("content-file", "", "the new content (default: stdin for Write/Edit)")
	if _, err := parseAll(fs, args); err != nil {
		return 2
	}
	req, hookMode := guard.Request{Tool: *tool, Path: *path}, *path == ""
	if hookMode {
		var in guardInput
		if json.NewDecoder(io.LimitReader(os.Stdin, maxHookInput)).Decode(&in) != nil {
			return printDecision(guard.Decision{Decision: guard.Allow}, false)
		}
		req = guard.Request{Tool: in.ToolName, Path: in.ToolInput.FilePath}
		if req.Path == "" {
			req.Path = in.ToolInput.NotebookPath
		}
		if in.ToolInput.Content != nil {
			req.Content, req.HasContent = []byte(*in.ToolInput.Content), true
		}
		if in.ToolInput.OldString != "" {
			req.Edits = append(req.Edits, guard.Edit{Old: in.ToolInput.OldString, New: in.ToolInput.NewString, All: in.ToolInput.ReplaceAll})
		}
		for _, e := range in.ToolInput.Edits {
			req.Edits = append(req.Edits, guard.Edit{Old: e.OldString, New: e.NewString, All: e.ReplaceAll})
		}
	} else if req.Tool == "Write" || req.Tool == "Edit" {
		var data []byte
		var err error
		if *contentFile != "" {
			data, err = os.ReadFile(*contentFile)
		} else {
			data, err = io.ReadAll(io.LimitReader(os.Stdin, maxHookInput))
		}
		if err != nil {
			return fail("guard: %v", err)
		}
		// from the command line an Edit carries the whole new file, so it is judged as a Write
		req.Tool, req.Content, req.HasContent = "Write", data, true
	}
	if req.Path != "" && !filepath.IsAbs(req.Path) {
		req.Path, _ = filepath.Abs(req.Path)
	}
	return printDecision(guard.Decide(config.BrainRoot(), req), hookMode)
}

func printDecision(d guard.Decision, hookMode bool) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false) // reasons carry "> 8192", read by people and models
	_ = enc.Encode(d)
	if hookMode && d.Decision == guard.Block {
		fmt.Fprintln(os.Stderr, d.Reason)
		return 2
	}
	return 0
}
