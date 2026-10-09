package squad

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"saahil.local/brain/internal/agents"
	"saahil.local/brain/internal/config"
)

// SSH reaches the devbox with its own connection, so a stale ControlMaster never hangs a command.
var SSH = []string{"ssh", "-o", "ConnectTimeout=8", "-o", "ControlPath=none"}

// ErrViaBridge: from the devbox the laptop is reached only through the bridge's allow-listed operations.
var ErrViaBridge = errors.New("the laptop is reached from the devbox only through the bridge's read-only operations")

// ErrNoOrcaEnv: the laptop reaches the devbox's Orca server through the environment its Orca app paired, orca_env.
var ErrNoOrcaEnv = errors.New("orca_env is not set: brain identity --init --orca-env <id> (the devbox's environment in the laptop's Orca app)")

// Host runs commands on one machine: here, or on the devbox over ssh from the laptop. Run always starts a process on
// THIS machine; Shell and Orca act on the host's machine.
type Host struct {
	Machine config.Machine
	Remote  bool
	OrcaCLI []string
	Run     agents.Runner
}

// HostFor picks how this machine drives the target: locally, or the devbox from the laptop over ssh and the paired
// Orca environment. The devbox cannot drive the laptop this way (ErrViaBridge).
func HostFor(here, target config.Machine, run agents.Runner) (Host, error) {
	switch {
	case here == target && target == config.Devbox:
		return Host{Machine: target, OrcaCLI: []string{agents.OrcaCLI()}, Run: run}, nil
	case here == target:
		return Host{Machine: target, OrcaCLI: []string{"orca"}, Run: run}, nil
	case here == config.Laptop:
		return Host{Machine: config.Devbox, Remote: true, OrcaCLI: []string{"orca", "--environment", config.CurrentIdentity().OrcaEnv}, Run: run}, nil
	}
	return Host{}, ErrViaBridge
}

// Shell runs a login-shell command on the host (default timeout 2 min).
func (h Host) Shell(ctx context.Context, command string) (string, error) {
	ctx, cancel := withDefaultTimeout(ctx, 2*time.Minute)
	defer cancel()
	name, args := "bash", []string{"-lc", command}
	if h.Remote {
		name, args = SSH[0], append(append([]string{}, SSH[1:]...), "-o", "BatchMode=yes", config.DevboxHost(), "bash -lc "+Quote(command))
	}
	out, err := h.Run(ctx, name, args...)
	if err != nil {
		return string(out), fmt.Errorf("%s command failed (%s): %s%s", h.Machine, exitText(err), command, stderrSuffix(err))
	}
	return string(out), nil
}

// Orca is one orca CLI call on the host's Orca runtime: its result, or Orca's own error.
func (h Host) Orca(ctx context.Context, args ...string) (json.RawMessage, error) {
	if h.Remote && h.OrcaCLI[len(h.OrcaCLI)-1] == "" {
		return nil, ErrNoOrcaEnv
	}
	ctx, cancel := withDefaultTimeout(ctx, time.Minute)
	defer cancel()
	argv := append(append(append([]string{}, h.OrcaCLI[1:]...), args...), "--json")
	out, runErr := h.Run(ctx, h.OrcaCLI[0], argv...)
	var reply struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &reply) != nil {
		reply.OK, reply.Error.Message = false, lastChars(strings.TrimSpace(string(out)+stderrSuffix(runErr)), 300)
	}
	if !reply.OK {
		msg := fmt.Sprintf("orca %s: %s", strings.Join(args[:min(2, len(args))], " "), reply.Error.Message)
		if h.Remote {
			msg += "\nif the server is unreachable: check the laptop's Orca app reaches its environment on " + config.DevboxHost() +
				" (orca_env " + h.OrcaCLI[len(h.OrcaCLI)-1] + ")"
		}
		return nil, errors.New(msg)
	}
	return reply.Result, nil
}

// readJSON fills v from a JSON file on the host; a missing file leaves v unchanged.
func (h Host) readJSON(ctx context.Context, path string, v any) error {
	var raw []byte
	if h.Remote {
		out, err := h.Shell(ctx, fmt.Sprintf("if [ -e %[1]s ]; then cat %[1]s; fi", Quote(path)))
		if err != nil {
			return err
		}
		raw = []byte(out)
	} else {
		var err error
		if raw, err = os.ReadFile(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func (h Host) writeFile(ctx context.Context, path string, data []byte) error {
	if h.Remote {
		_, err := h.Shell(ctx, fmt.Sprintf("mkdir -p %s && printf '%%s\\n' %s > %s", Quote(filepath.Dir(path)), Quote(string(data)), Quote(path)))
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (h Host) modTime(ctx context.Context, path string) (time.Time, error) {
	if !h.Remote {
		st, err := os.Stat(path)
		if err != nil {
			return time.Time{}, err
		}
		return st.ModTime(), nil
	}
	out, err := h.Shell(ctx, "stat -c %Y "+Quote(path))
	if err != nil {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	return time.Unix(sec, 0), err
}

// transcriptCWD is the last cwd a Claude transcript records; a remote transcript is read where it lives, not copied.
func (h Host) transcriptCWD(ctx context.Context, path string) (string, error) {
	if h.Remote {
		const reader = "import sys,json\ncwd=''\nfor l in open(sys.argv[1]):\n" +
			"    try: cwd=json.loads(l).get('cwd') or cwd\n    except ValueError: pass\nprint(cwd)"
		out, err := h.Shell(ctx, "python3 -c "+Quote(reader)+" "+Quote(path))
		return strings.TrimSpace(out), err
	}
	return transcriptCWDOf(path)
}

func transcriptCWDOf(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	cwd := ""
	for {
		line, err := reader.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"cwd"`)) {
			var entry struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.CWD != "" {
				cwd = entry.CWD
			}
		}
		if err == io.EOF {
			return cwd, nil
		} else if err != nil {
			return cwd, err
		}
	}
}

// ---- processes on this machine ----

// StreamCommand runs a command with this terminal attached (progress bars, sign-in prompts).
func StreamCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// ExecProcess replaces this process with path (looked up on PATH when it has no slash), as os.execv does.
func ExecProcess(path string, argv []string) error {
	if !strings.Contains(path, "/") {
		found, err := exec.LookPath(path)
		if err != nil {
			return err
		}
		path = found
	}
	return syscall.Exec(path, argv, os.Environ())
}

// ---- helpers ----

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

// Quote is Python's shlex.quote: one shell word whatever the text holds.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func withDefaultTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, has := ctx.Deadline(); has {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

func exitText(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return strconv.Itoa(exit.ExitCode())
	}
	return err.Error()
}

func stderrSuffix(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		return "\n" + strings.TrimSpace(string(exit.Stderr))
	}
	return ""
}

func lastChars(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}
