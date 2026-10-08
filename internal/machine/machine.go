// Package machine starts, stops and signs in the devbox. From the laptop it runs the existing ~/.local/bin/devbox
// script and ssh, exactly as devbox-agent did; on the devbox it reports and signs in locally.
package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/squad"
)

var Services = []string{"github", "aws", "gcloud"}

type Machine struct {
	Here   config.Machine
	Host   squad.Host // the devbox
	Exec   func(path string, argv []string) error
	Stream func(ctx context.Context, name string, args ...string) error
	Sleep  func(time.Duration)
	Out    io.Writer
}

func New(here config.Machine, host squad.Host) *Machine {
	return &Machine{Here: here, Host: host, Exec: squad.ExecProcess, Stream: squad.StreamCommand, Sleep: time.Sleep, Out: os.Stdout}
}

func (m *Machine) printf(format string, args ...any) { fmt.Fprintf(m.Out, format, args...) }

func devboxBin(name string) string { return filepath.Join(config.DevboxHome, ".local", "bin", name) }

// Verb hands a machine command to the laptop's devbox script with its arguments unchanged (`down --force` reaches it
// as typed). On the devbox only status means something.
func (m *Machine) Verb(ctx context.Context, verb string, rest []string) error {
	if m.Here == config.Laptop {
		return m.Exec(filepath.Join(config.BinDir(), "devbox"), append([]string{"devbox", verb}, rest...))
	}
	switch verb {
	case "status":
		m.printf("%s", LocalStatus(ctx, m.Host))
		return nil
	case "up":
		m.printf("the devbox is up: this is it\n")
		return nil
	}
	return fmt.Errorf("machine %s runs on the laptop (it drives the EC2 instance through ~/.local/bin/devbox)", verb)
}

// LocalStatus is the devbox's own view: host, uptime and load, GitHub certificate expiry, AWS SSO sign-in.
func LocalStatus(ctx context.Context, host squad.Host) string {
	var b strings.Builder
	name, _ := os.Hostname()
	uptime := "?"
	if raw, err := os.ReadFile("/proc/uptime"); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			if sec, err := strconv.ParseFloat(fields[0], 64); err == nil {
				uptime = roundDuration(time.Duration(sec) * time.Second)
			}
		}
	}
	load := "?"
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) >= 3 {
			load = strings.Join(fields[:3], " ")
		}
	}
	fmt.Fprintf(&b, "devbox (this machine): %s, up %s, load %s\n", name, uptime, load)
	now := time.Now()
	if expiry, err := githubCertExpiry(ctx, host, filepath.Join(config.Home(), ".ssh", "work_github-cert.pub")); err != nil {
		fmt.Fprintf(&b, "GitHub cert: unreadable (%v)\n", err)
	} else {
		fmt.Fprintf(&b, "GitHub cert: %s\n", until(expiry, now, "brain machine login github"))
	}
	if expiry, found := awsSSOExpiry(filepath.Join(config.Home(), ".aws", "sso", "cache")); !found {
		fmt.Fprintf(&b, "AWS SSO: not signed in (brain machine login aws)\n")
	} else {
		fmt.Fprintf(&b, "AWS SSO: %s\n", until(expiry, now, "brain machine login aws"))
	}
	return b.String()
}

func until(expiry, now time.Time, renew string) string {
	if !expiry.After(now) {
		return fmt.Sprintf("EXPIRED at %s (%s)", expiry.UTC().Format(time.RFC3339), renew)
	}
	return fmt.Sprintf("valid until %s (%s left)", expiry.UTC().Format(time.RFC3339), roundDuration(expiry.Sub(now)))
}

var certValid = regexp.MustCompile(`Valid: from (\S+) to (\S+)`)

func githubCertExpiry(ctx context.Context, host squad.Host, cert string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := host.Run(ctx, "ssh-keygen", "-L", "-f", cert)
	if err != nil {
		return time.Time{}, err
	}
	match := certValid.FindStringSubmatch(string(out))
	if match == nil {
		return time.Time{}, errors.New("no Valid: line")
	}
	// ssh-keygen prints the machine's local time
	return time.ParseInLocation("2006-01-02T15:04:05", match[2], time.Local)
}

// awsSSOExpiry is the latest expiry among cached SSO access tokens; only the expiry is read out, never a token.
func awsSSOExpiry(dir string) (time.Time, bool) {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var latest time.Time
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry struct {
			StartURL    string          `json:"startUrl"`
			AccessToken json.RawMessage `json:"accessToken"`
			ExpiresAt   time.Time       `json:"expiresAt"`
		}
		if json.Unmarshal(raw, &entry) != nil || entry.StartURL == "" || len(entry.AccessToken) == 0 {
			continue
		}
		if entry.ExpiresAt.After(latest) {
			latest = entry.ExpiresAt
		}
	}
	return latest, !latest.IsZero()
}

func roundDuration(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
	return d.Round(time.Minute).String()
}

// ---- sign-ins ----

// Login renews the devbox sign-ins (all three when none is named): from the laptop through ssh with the laptop's
// browser, on the devbox in this terminal.
func (m *Machine) Login(ctx context.Context, services []string) error {
	for _, s := range services {
		if !slices.Contains(Services, s) {
			return fmt.Errorf("unknown sign-in %q (github|aws|gcloud)", s)
		}
	}
	if len(services) == 0 {
		services = Services
	}
	if m.Host.Remote {
		args := append(append([]string{}, squad.SSH[1:]...), "-o", "BatchMode=yes", config.DevboxHost, "true")
		if _, err := m.Host.Run(ctx, squad.SSH[0], args...); err != nil {
			return errors.New("devbox unreachable: run  brain machine up")
		}
	}
	for _, service := range services {
		m.printf("== %s\n", service)
		var err error
		switch {
		case service == "github" && m.Host.Remote:
			err = m.Stream(ctx, "ssh", "-t", "-L", "8250:127.0.0.1:8250", config.DevboxHost, devboxBin("devbox-github-login"), "--tunnel")
		case service == "github":
			err = m.Stream(ctx, devboxBin("devbox-github-login"))
		case service == "aws" && m.Host.Remote:
			err = m.Stream(ctx, "ssh", "-t", config.DevboxHost, `bash -lc "aws sso login --sso-session Example-Session --use-device-code"`)
		case service == "aws":
			err = m.Stream(ctx, "bash", "-lc", "aws sso login --sso-session Example-Session --use-device-code")
		case m.Host.Remote:
			err = m.loginGcloud(ctx)
		default:
			err = m.Stream(ctx, "bash", "-lc", "gcloud auth login --update-adc --no-browser")
		}
		if err != nil {
			m.printf("%s: %v\n", service, err)
		}
	}
	return nil
}

// loginGcloud is gcloud's remote bootstrap: the devbox starts the flow, the laptop's browser signs in, the code goes
// back.
func (m *Machine) loginGcloud(ctx context.Context) error {
	account, _ := m.Host.Shell(ctx, "gcloud config get-value account 2>/dev/null")
	account = strings.TrimSpace(account)
	flow := "gcloud auth login " + squad.Quote(account) + " --update-adc --no-browser; echo LOGIN-EXIT=\\$?; sleep 120"
	if account == "" {
		flow = "gcloud auth login --update-adc --no-browser; echo LOGIN-EXIT=\\$?; sleep 120"
	}
	_, _ = m.Host.Shell(ctx, "tmux kill-session -t gcloud-login 2>/dev/null; tmux new-session -d -s gcloud-login -x 400 -y 50 "+
		squad.Quote(`bash -lc "`+flow+`"`))
	pane := func() string {
		out, _ := m.Host.Shell(ctx, "tmux capture-pane -p -J -t gcloud-login: -S -60")
		return out
	}
	var text string
	for range 30 {
		if text = pane(); strings.Contains(text, "--remote-bootstrap=") || strings.Contains(text, "LOGIN-EXIT=") {
			break
		}
		m.Sleep(time.Second)
	}
	_, after, found := strings.Cut(text, "--remote-bootstrap=")
	if !found {
		return fmt.Errorf("gcloud on the devbox did not ask for a browser sign-in:\n%s", lastBytes(text, 800))
	}
	url := strings.Trim(strings.Fields(after)[0], `"`)
	m.printf("signing in through your browser…\n")
	// gcloud prints the localhost:8085 code line on stderr, not stdout
	signin := exec.CommandContext(ctx, "gcloud", "auth", "login", "--remote-bootstrap="+url)
	signin.Stdin = strings.NewReader("y\n")
	out, _ := signin.CombinedOutput()
	code := ""
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "https://localhost:8085/") {
			code = line
			break
		}
	}
	if code == "" {
		return fmt.Errorf("the laptop sign-in returned no code:\n%s", lastBytes(string(out), 800))
	}
	if _, err := m.Host.Shell(ctx, "tmux send-keys -t gcloud-login: -l "+squad.Quote(code)+"; tmux send-keys -t gcloud-login: Enter"); err != nil {
		return err
	}
	for range 30 {
		m.Sleep(2 * time.Second)
		text = pane()
		_, tail, _ := cutLast(text, "Enter the output")
		if strings.Contains(tail, "Do you want to continue (Y/n)?") && !strings.Contains(text, "LOGIN-EXIT=") {
			_, _ = m.Host.Shell(ctx, "tmux send-keys -t gcloud-login: Y Enter") // GOOGLE_APPLICATION_CREDENTIALS is set; write ADC anyway
		}
		if strings.Contains(text, "LOGIN-EXIT=") {
			break
		}
	}
	_, _ = m.Host.Shell(ctx, "tmux kill-session -t gcloud-login 2>/dev/null")
	if _, err := m.Host.Shell(ctx, "gcloud auth print-access-token >/dev/null 2>&1"); err != nil {
		m.printf("gcloud: still not signed in\n")
	} else {
		m.printf("gcloud: ok (gcloud + ADC renewed)\n")
	}
	return nil
}

func cutLast(s, sep string) (string, string, bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return "", s, false
}

func lastBytes(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
