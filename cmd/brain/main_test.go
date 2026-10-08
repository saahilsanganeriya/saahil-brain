package main

import "testing"

// Catches a command that parses no flags running (and writing) when asked for --help.
func TestHelpNeverRunsACommand(t *testing.T) {
	ran := false
	commands["zz-test-writer"] = command{path: "zz-test-writer", summary: "test", run: func([]string) int { ran = true; return 0 }}
	defer delete(commands, "zz-test-writer")
	for _, h := range []string{"-h", "--help"} {
		if code := dispatch([]string{"zz-test-writer", h}); code != 0 || ran {
			t.Fatalf("%s: code %d ran=%v", h, code, ran)
		}
	}
}
