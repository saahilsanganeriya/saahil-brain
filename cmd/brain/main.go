// Command brain: the brain's index, hooks and tools, and the two-machine agent mesh.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// command is one `brain <group> [<sub>]` entry; each area registers its own from cmd_<area>.go so the files never
// collide.
type command struct {
	path    string // "agents note", "index"
	summary string
	run     func(args []string) int
}

var commands = map[string]command{}

func register(path, summary string, run func(args []string) int) {
	if _, dup := commands[path]; dup {
		panic("duplicate command " + path)
	}
	commands[path] = command{path: path, summary: summary, run: run}
}

func main() { os.Exit(dispatch(os.Args[1:])) }

func dispatch(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage("")
		return 0
	}
	if len(args) >= 2 {
		if c, ok := commands[args[0]+" "+args[1]]; ok {
			return runCommand(c, args[2:])
		}
	}
	if c, ok := commands[args[0]]; ok {
		return runCommand(c, args[1:])
	}
	if hasGroup(args[0]) {
		usage(args[0])
		return 2
	}
	fmt.Fprintf(os.Stderr, "brain: unknown command %q\n\n", args[0])
	usage("")
	return 2
}

// runCommand answers -h/--help itself: a command that parses no flags would otherwise run (and write) on --help.
func runCommand(c command, rest []string) int {
	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
		fmt.Printf("brain %s — %s\n", c.path, c.summary)
		return 0
	}
	return c.run(rest)
}

func hasGroup(group string) bool {
	for path := range commands {
		if strings.HasPrefix(path, group+" ") {
			return true
		}
	}
	return false
}

func usage(group string) {
	fmt.Println("brain — a personal brain for coding agents: index, hooks, pointers, guards, MCP; and the two-machine agent mesh.")
	fmt.Println()
	paths := make([]string, 0, len(commands))
	for path := range commands {
		if strings.HasPrefix(path, "_") || (group != "" && !strings.HasPrefix(path, group+" ")) {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fmt.Printf("  brain %-22s %s\n", path, commands[path].summary)
	}
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "brain: "+format+"\n", args...)
	return 1
}
