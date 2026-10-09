package machine

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"saahil.local/brain/internal/config"
)

// TestLaptopVerbsReachTheDevboxScriptUnchanged catches a machine command that is not the old devbox-agent's: on the
// laptop each verb must exec ~/.local/bin/devbox with the arguments as typed (`down --force` must stay `--force`), and
// on the devbox the verbs that drive the EC2 must refuse instead of acting on the machine they run on.
func TestLaptopVerbsReachTheDevboxScriptUnchanged(t *testing.T) {
	for _, argv := range [][]string{{"up"}, {"down"}, {"down", "--force"}, {"resize", "m7i.4xlarge"}, {"sync"}, {"status"}} {
		var gotPath string
		var gotArgv []string
		m := &Machine{Here: config.Laptop, Out: &bytes.Buffer{}, Exec: func(path string, args []string) error {
			gotPath, gotArgv = path, args
			return nil
		}}
		if err := m.Verb(context.Background(), argv[0], argv[1:]); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		if want := filepath.Join(config.BinDir(), "devbox"); gotPath != want {
			t.Errorf("%v exec'd %s, want %s", argv, gotPath, want)
		}
		if want := append([]string{"devbox"}, argv...); !slices.Equal(gotArgv, want) {
			t.Errorf("%v reached the script as %v, want %v", argv, gotArgv, want)
		}
	}

	for _, verb := range []string{"down", "resize", "sync"} {
		m := &Machine{Here: config.Devbox, Out: &bytes.Buffer{}, Exec: func(string, []string) error {
			t.Fatalf("%s on the devbox must not exec anything", verb)
			return nil
		}}
		err := m.Verb(context.Background(), verb, nil)
		if err == nil || !strings.Contains(err.Error(), "runs on the laptop") {
			t.Errorf("%s on the devbox: %v, want a refusal that names the laptop", verb, err)
		}
	}
}
