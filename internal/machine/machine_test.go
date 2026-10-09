package machine

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/config/identitytest"
	"saahil.local/brain/internal/squad"
)

// TestLaptopVerbsReachTheDevboxScriptUnchanged catches a machine command that is not the old devbox-agent's: on the
// laptop each verb must exec ~/.local/bin/devbox with the arguments as typed (`down --force` must stay `--force`), and
// on the devbox the verbs that drive the EC2 must refuse instead of acting on the machine they run on.
func TestLaptopVerbsReachTheDevboxScriptUnchanged(t *testing.T) {
	for _, argv := range [][]string{{"up"}, {"down"}, {"down", "--force"}, {"sync"}, {"status"}} {
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

	for _, verb := range []string{"down", "sync"} {
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

// TestAWSLoginUsesTheIdentitysSession catches every person's AWS sign-in going to the owner's SSO session name, a
// session name with a space reaching the shell as two words (from the laptop over ssh and on the devbox), or a login
// with no session set running aws instead of naming the key to set.
func TestAWSLoginUsesTheIdentitysSession(t *testing.T) {
	identitytest.Write(t, `{"aws_sso_session": "Team Session", "devbox_host": "box-test"}`)
	var streamed [][]string
	stream := func(_ context.Context, name string, args ...string) error {
		streamed = append(streamed, append([]string{name}, args...))
		return nil
	}
	reachable := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	for _, m := range []*Machine{
		{Here: config.Devbox, Out: &bytes.Buffer{}, Stream: stream},
		{Here: config.Laptop, Host: squad.Host{Remote: true, Run: reachable}, Out: &bytes.Buffer{}, Stream: stream},
	} {
		if err := m.Login(context.Background(), []string{"aws"}); err != nil {
			t.Fatal(err)
		}
	}
	login := "aws sso login --sso-session 'Team Session' --use-device-code"
	want := [][]string{{"bash", "-lc", login}, {"ssh", "-t", "box-test", "bash -lc " + squad.Quote(login)}}
	if len(streamed) != 2 || !slices.Equal(streamed[0], want[0]) || !slices.Equal(streamed[1], want[1]) {
		t.Errorf("streamed %q\nwant     %q", streamed, want)
	}
	identitytest.Write(t, `{}`)
	out := &bytes.Buffer{}
	_ = (&Machine{Here: config.Devbox, Out: out, Stream: stream}).Login(context.Background(), []string{"aws"})
	if len(streamed) != 2 || !strings.Contains(out.String(), "aws_sso_session") {
		t.Errorf("no session: streamed %q, said %q", streamed[2:], out)
	}
}
