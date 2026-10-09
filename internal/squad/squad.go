// Package squad drives Claude agents that run as Orca terminals (and the older Happier/tmux ones) on the devbox, or on
// this machine: the old devbox-agent's screen, attach, new, goal, resume, move, stop and box, same behaviour and
// messages. Paths are identical on both machines (the devbox mounts its home at the laptop spelling), so a transcript,
// a worktree or a box keeps its path when it moves.
package squad

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"saahil.local/brain/internal/agents"
	"saahil.local/brain/internal/config"
)

const (
	claudeCommand   = "claude --dangerously-skip-permissions" // typed into a login shell: `claude` is devbox-exec's
	goalMaxChars    = 4000
	freshTranscript = 120 * time.Second
)

// Terminal is one terminal of an Orca runtime, as `orca terminal list` reports it.
type Terminal struct {
	Handle        string `json:"handle"`
	TabID         string `json:"tabId,omitempty"`
	WorktreePath  string `json:"worktreePath,omitempty"`
	AgentIdentity string `json:"agentIdentity,omitempty"`
	Title         string `json:"title,omitempty"`
}

// RegistryEntry is one orca.json record: Claude retitles its terminal to the conversation's topic, so an agent's name
// is kept here against its tab and folder. devbox-agent shares the file, so its shape must not change.
type RegistryEntry struct {
	Tab    string `json:"tab"`
	Folder string `json:"folder"`
}

type happierName struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// ScreenResult is an agent's screen and how many background agents it says it waits on.
type ScreenResult struct {
	Text    string `json:"text"`
	Waiting int    `json:"waiting,omitempty"`
}

// Squad carries everything the commands touch, so tests swap processes, clock and folders.
type Squad struct {
	Here     config.Machine
	Host     Host
	Stream   func(ctx context.Context, name string, args ...string) error
	Exec     func(path string, argv []string) error
	Sleep    func(time.Duration)
	Now      func() time.Time
	Out      io.Writer
	Sessions func(ctx context.Context) ([]agents.Session, error) // live sessions on the host's machine

	Registry        string // orca.json
	NamesFile       string // Happier agents: session id → name, dir
	HappierSessions string
	Projects        string // Claude transcripts
	Worktrees       string // the only root --rm-worktree removes under
	Repo            string // the main checkout; relative boxes live under it
	DevboxBin       string
}

func New(here config.Machine, host Host) *Squad {
	home := config.Home()
	f := &Squad{
		Here: here, Host: host, Stream: StreamCommand, Exec: ExecProcess, Sleep: time.Sleep, Now: time.Now, Out: os.Stdout,
		Registry:        filepath.Join(config.AgentsDir(), "orca.json"),
		NamesFile:       filepath.Join(config.AgentsDir(), "names.json"),
		HappierSessions: filepath.Join(home, ".happier", "terminal", "sessions"),
		Projects:        filepath.Join(config.ClaudeDir(), "projects"),
		Worktrees:       filepath.Join(home, "Documents", "worktrees", "work-repo"),
		Repo:            filepath.Join(home, "Documents", "work-repo"),
		DevboxBin:       filepath.Join(config.DevboxHome, ".local", "bin"),
	}
	if !host.Remote {
		f.Sessions = func(ctx context.Context) ([]agents.Session, error) {
			sessions, err := agents.LocalSessions(agents.RegistryDir())
			agents.FillOrcaHandles(ctx, host.Run, sessions)
			return sessions, err
		}
	}
	return f
}

func (f *Squad) printf(format string, args ...any) { fmt.Fprintf(f.Out, format, args...) }

func (f *Squad) needDevbox(ctx context.Context) error {
	if !f.Host.Remote {
		return nil
	}
	args := append(append([]string{}, SSH[1:]...), "-o", "BatchMode=yes", config.DevboxHost, "true")
	if _, err := f.Host.Run(ctx, SSH[0], args...); err != nil {
		return errors.New("devbox unreachable: run  brain machine up")
	}
	return nil
}

func (f *Squad) onlyDevbox(command string) error {
	if f.Host.Machine != config.Devbox {
		return fmt.Errorf("%s works on devbox agents only (the devbox's Orca server and transcripts)", command)
	}
	return nil
}

// ---- the Orca runtime ----

func (f *Squad) terminals(ctx context.Context) ([]Terminal, error) {
	raw, err := f.Host.Orca(ctx, "terminal", "list")
	if err != nil {
		return nil, err
	}
	var list struct {
		Terminals []Terminal `json:"terminals"`
	}
	return list.Terminals, json.Unmarshal(raw, &list)
}

func (f *Squad) registry(ctx context.Context) (map[string]RegistryEntry, error) {
	names := map[string]RegistryEntry{}
	return names, f.Host.readJSON(ctx, f.Registry, &names)
}

// register records name → the terminal's tab and folder; an empty handle drops the name.
func (f *Squad) register(ctx context.Context, name, handle string) error {
	names, err := f.registry(ctx)
	if err != nil {
		return err
	}
	if handle == "" {
		delete(names, name)
	} else {
		terminals, err := f.terminals(ctx)
		if err != nil {
			return err
		}
		i := indexOf(terminals, func(t Terminal) bool { return t.Handle == handle })
		if i < 0 {
			return fmt.Errorf("terminal %s is not in Orca's list", handle)
		}
		names[name] = RegistryEntry{Tab: terminals[i].TabID, Folder: terminals[i].WorktreePath}
	}
	raw, _ := json.MarshalIndent(names, "", " ")
	return f.Host.writeFile(ctx, f.Registry, raw)
}

// agent is the live Claude terminal named <name>: its orca.json entry (by tab, else the Claude terminal in its
// folder), else a live session /renamed to it that runs in an Orca terminal. nil means a Happier agent, or none.
func (f *Squad) agent(ctx context.Context, name string) (*Terminal, error) {
	names, err := f.registry(ctx)
	if err != nil {
		return nil, err
	}
	if entry, registered := names[name]; registered {
		terminals, err := f.terminals(ctx)
		if err != nil {
			return nil, err
		}
		if i := indexOf(terminals, func(t Terminal) bool { return t.TabID != "" && t.TabID == entry.Tab }); i >= 0 {
			return &terminals[i], nil
		}
		if i := indexOf(terminals, func(t Terminal) bool {
			return entry.Folder != "" && t.WorktreePath == entry.Folder && t.AgentIdentity == "claude"
		}); i >= 0 {
			return &terminals[i], nil
		}
	}
	if f.Sessions == nil {
		return nil, nil
	}
	sessions, _ := f.Sessions(ctx) // unreachable registry: fall through to Happier, as an unnamed agent would
	s, found := agents.Resolve(sessions, name)
	if !found || s.OrcaHandle == "" {
		return nil, nil
	}
	terminals, err := f.terminals(ctx)
	if err != nil {
		return nil, err
	}
	if i := indexOf(terminals, func(t Terminal) bool { return t.Handle == s.OrcaHandle }); i >= 0 {
		return &terminals[i], nil
	}
	return &Terminal{Handle: s.OrcaHandle, WorktreePath: s.CWD, AgentIdentity: "claude"}, nil
}

func (f *Squad) send(ctx context.Context, handle, text string, enter bool) error {
	args := []string{"terminal", "send", "--terminal", handle, "--text", text}
	if enter {
		args = append(args, "--enter")
	}
	_, err := f.Host.Orca(ctx, args...)
	return err
}

// start opens a terminal titled <name> in a folder and runs command in its login shell.
func (f *Squad) start(ctx context.Context, name, folder, command string) (string, error) {
	if existing, err := f.agent(ctx, name); err != nil {
		return "", err
	} else if existing != nil {
		return "", fmt.Errorf("an agent named %s already runs in Orca (see: brain agents list)", name)
	}
	raw, err := f.Host.Orca(ctx, "terminal", "create", "--worktree", "path:"+folder, "--title", name, "--command", command)
	// Orca opens terminals only at a project or worktree root: a folder below one starts at the nearest root and cds
	// back, so `claude --resume` still finds its transcript under the recorded folder
	for dir := folder; err != nil && strings.Contains(err.Error(), "selector_not_found") && filepath.Dir(dir) != dir; {
		dir = filepath.Dir(dir)
		raw, err = f.Host.Orca(ctx, "terminal", "create", "--worktree", "path:"+dir, "--title", name,
			"--command", "cd "+Quote(folder)+" && "+command)
	}
	if err != nil {
		return "", err
	}
	var created struct {
		Handle   string `json:"handle"`
		Terminal struct {
			Handle string `json:"handle"`
		} `json:"terminal"`
	}
	_ = json.Unmarshal(raw, &created)
	handle := cmp.Or(created.Terminal.Handle, created.Handle)
	return handle, f.register(ctx, name, handle)
}

// nameSession /renames the Claude session once its TUI is up: other sessions message it by this name.
func (f *Squad) nameSession(ctx context.Context, handle, name string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	raw, err := f.Host.Orca(waitCtx, "terminal", "wait", "--terminal", handle, "--for", "tui-idle", "--timeout-ms", "120000")
	var reply struct {
		Wait struct {
			Satisfied bool `json:"satisfied"`
		} `json:"wait"`
	}
	if err == nil && json.Unmarshal(raw, &reply) == nil && reply.Wait.Satisfied {
		return f.send(ctx, handle, "/rename "+name, true)
	}
	f.printf("%s: the TUI was not idle within 2 min, so the session is not renamed (brain agents screen %s)\n", name, name)
	return nil
}

// projectSlug is the folder name Claude keeps a working folder's transcripts under.
func projectSlug(folder string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(folder)
}

func (f *Squad) newestTranscript(ctx context.Context, folder string) string {
	out, _ := f.Host.Shell(ctx, "ls -t "+Quote(filepath.Join(f.Projects, projectSlug(folder)))+"/*.jsonl | head -1")
	return strings.TrimSpace(out)
}

// ---- Happier (tmux) agents ----

func (f *Squad) happierNames(ctx context.Context) (map[string]happierName, error) {
	names := map[string]happierName{}
	return names, f.Host.readJSON(ctx, f.NamesFile, &names)
}

// happierTarget is the agent's tmux session and launch folder. names.json may hold stale duplicates of a name, so the
// first (by id) whose terminal record still exists wins.
func (f *Squad) happierTarget(ctx context.Context, name string) (string, string, error) {
	names, _ := f.happierNames(ctx)
	ids := make([]string, 0, len(names))
	for id, entry := range names {
		if entry.Name == name {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		var session struct {
			Terminal struct {
				Tmux struct {
					Target string `json:"target"`
				} `json:"tmux"`
			} `json:"terminal"`
		}
		if f.Host.readJSON(ctx, filepath.Join(f.HappierSessions, id+".json"), &session) == nil && session.Terminal.Tmux.Target != "" {
			target, _, _ := strings.Cut(session.Terminal.Tmux.Target, ":")
			return target, names[id].Dir, nil
		}
	}
	return "", "", fmt.Errorf("no running agent named %s (see: brain agents list)", name)
}

// typeInto types text into a Happier agent's prompt as keystrokes, then Enter (a paste would not run a slash command).
func (f *Squad) typeInto(ctx context.Context, target, text string) error {
	pane := Quote(target + ":")
	_, err := f.Host.Shell(ctx, fmt.Sprintf("tmux send-keys -t %s -l %s; sleep 1; tmux send-keys -t %s Enter",
		pane, Quote(strings.Join(strings.Fields(text), " ")), pane))
	return err
}

// ---- commands ----

var waitingFor = regexp.MustCompile(`Waiting for (\d+) background agent`)

func (f *Squad) Screen(ctx context.Context, name string, lines int) (ScreenResult, error) {
	if err := f.needDevbox(ctx); err != nil {
		return ScreenResult{}, err
	}
	agent, err := f.agent(ctx, name)
	if err != nil {
		return ScreenResult{}, err
	}
	var text string
	if agent != nil {
		raw, err := f.Host.Orca(ctx, "terminal", "read", "--terminal", agent.Handle, "--limit", strconv.Itoa(lines))
		if err != nil {
			return ScreenResult{}, err
		}
		var read struct {
			Terminal struct {
				Tail []any `json:"tail"`
			} `json:"terminal"`
		}
		if err := json.Unmarshal(raw, &read); err != nil {
			return ScreenResult{}, err
		}
		rows := make([]string, len(read.Terminal.Tail))
		for i, row := range read.Terminal.Tail {
			rows[i] = fmt.Sprint(row)
		}
		text = strings.Join(rows, "\n")
	} else {
		target, _, err := f.happierTarget(ctx, name)
		if err != nil {
			return ScreenResult{}, err
		}
		text, _ = f.Host.Shell(ctx, fmt.Sprintf("tmux capture-pane -p -J -t %s -S -%d", Quote(target+":"), lines))
	}
	res := ScreenResult{Text: strings.TrimRight(text, " \t\r\n")}
	if m := waitingFor.FindStringSubmatch(text); m != nil {
		res.Waiting, _ = strconv.Atoi(m[1])
	}
	return res, nil
}

// Attach opens this terminal onto a Happier agent's tmux session; Orca agents open in the Orca app instead.
func (f *Squad) Attach(ctx context.Context, name string) error {
	if err := f.onlyDevbox("attach"); err != nil {
		return err
	}
	if name != "" {
		if agent, err := f.agent(ctx, name); err != nil {
			return err
		} else if agent != nil {
			return fmt.Errorf("%s runs in Orca: open it in the Orca app (host devbox-host-server), or  brain agents screen %s", name, name)
		}
	}
	argv := []string{filepath.Join(f.DevboxBin, "agent-attach")}
	if name != "" {
		argv = append(argv, name)
	}
	if !f.Host.Remote {
		return f.Exec(argv[0], argv)
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = Quote(a)
	}
	// its own TCP connection, so one dropped link never closes every attached pane
	return f.Exec("ssh", []string{"ssh", "-t", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ServerAliveInterval=15",
		config.DevboxHost, strings.Join(quoted, " ")})
}

// NewAgent starts a fresh agent in its own Orca worktree at base, or in an existing folder.
func (f *Squad) NewAgent(ctx context.Context, name, base, dir string) error {
	if err := f.onlyDevbox("new"); err != nil {
		return err
	}
	if err := f.needDevbox(ctx); err != nil {
		return err
	}
	var handle string
	if dir != "" {
		var err error
		if handle, err = f.start(ctx, name, dir, claudeCommand); err != nil {
			return err
		}
	} else {
		if existing, err := f.agent(ctx, name); err != nil {
			return err
		} else if existing != nil {
			return fmt.Errorf("an agent named %s already runs in Orca (see: brain agents list)", name)
		}
		// a fresh checkout of origin/main: a session loads the repo's rules from where it starts
		fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err := f.Host.Shell(fetchCtx, "git -C "+Quote(f.Repo)+" fetch -q origin main")
		cancel()
		if err != nil {
			return err
		}
		raw, err := f.Host.Orca(ctx, "worktree", "create", "--repo", OrcaRepo, "--name", name, "--no-parent", "--agent", "claude",
			"--base-branch", base)
		if err != nil {
			return err
		}
		var made struct {
			AgentTerminalHandle string `json:"agentTerminalHandle"`
			StartupTerminal     struct {
				Handle string `json:"handle"`
			} `json:"startupTerminal"`
			Worktree struct {
				Path   string `json:"path"`
				Branch string `json:"branch"`
			} `json:"worktree"`
		}
		if err := json.Unmarshal(raw, &made); err != nil {
			return err
		}
		handle = cmp.Or(made.AgentTerminalHandle, made.StartupTerminal.Handle)
		if err := f.register(ctx, name, handle); err != nil {
			return err
		}
		f.printf("worktree %s on %s\n", made.Worktree.Path, strings.TrimPrefix(made.Worktree.Branch, "refs/heads/"))
	}
	if err := f.nameSession(ctx, handle, name); err != nil {
		return err
	}
	f.printf("\nnext:  brain agents goal %s <goal file> [\"done when …\"]\n", name)
	return nil
}

// Goal sets an agent's /goal to a goal file's FULL text and succeeds only when a NEW "Goal set" line appears in its
// transcript.
func (f *Squad) Goal(ctx context.Context, name, goalFile, done string) error {
	if err := f.needDevbox(ctx); err != nil {
		return err
	}
	agent, err := f.agent(ctx, name)
	if err != nil {
		return err
	}
	if agent == nil {
		return f.happierGoal(ctx, name, goalFile, done)
	}
	// The goal checker reads only the /goal text, so it must be the file's FULL text. A long write arrives as a paste,
	// and a pasted "/goal …" is a plain message; typed "/goal " + the pasted body registers as the command.
	path := goalFile
	if !filepath.IsAbs(path) {
		path = agent.WorktreePath + "/" + goalFile
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		var out string
		if out, err = f.Host.Shell(ctx, "cat "+Quote(path)); err != nil {
			return err
		}
		raw = []byte(out)
	}
	text := strings.TrimSpace(string(raw))
	if done != "" {
		text += "\nDONE WHEN: " + done
	}
	if n := utf8.RuneCountInString(text); n > goalMaxChars {
		return fmt.Errorf("%s is %d characters; /goal carries at most 4,000", path, n)
	}
	firstSegment, _, _ := strings.Cut(strings.NewReplacer(`"`, "\n", `\`, "\n").Replace(text), "\n")
	probe := "Goal set: " + firstRunes(firstSegment, 40)
	transcript := f.newestTranscript(ctx, agent.WorktreePath)
	count := func() int {
		out, _ := f.Host.Shell(ctx, "grep -c -F "+Quote(probe)+" "+Quote(transcript)+" || true")
		n, _ := strconv.Atoi(strings.TrimSpace(out))
		return n
	}
	before := count()
	if err := f.send(ctx, agent.Handle, "/goal ", false); err != nil {
		return err
	}
	f.Sleep(time.Second)
	if err := f.send(ctx, agent.Handle, text, false); err != nil {
		return err
	}
	f.Sleep(time.Second)
	if err := f.send(ctx, agent.Handle, "", true); err != nil {
		return err
	}
	for range 30 {
		f.Sleep(2 * time.Second)
		if count() > before {
			f.printf("goal set on %s (%d chars): %s…\n", name, utf8.RuneCountInString(text), firstRunes(text, 100))
			return nil
		}
	}
	return fmt.Errorf("no %q in %s's transcript after 60 s: brain agents screen %s", probe, name, name)
}

// happierGoal: a long /goal typed through tmux arrives as a paste and never registers, so a Happier agent gets one
// short line that points at the goal file, and reads the file itself.
func (f *Squad) happierGoal(ctx context.Context, name, goalFile, done string) error {
	target, dir, err := f.happierTarget(ctx, name)
	if err != nil {
		return err
	}
	line := "/goal Complete the goal in " + goalFile + "."
	if done != "" {
		line = "/goal Complete the goal in " + goalFile + ": " + done
	}
	count := func() int {
		out, _ := f.Host.Shell(ctx, fmt.Sprintf(`grep -h -c "Goal set" $(ls -t %s/*.jsonl | head -1) || echo 0`,
			Quote(filepath.Join(f.Projects, projectSlug(dir)))))
		fields := strings.Fields(out)
		if len(fields) == 0 {
			return 0
		}
		n, _ := strconv.Atoi(fields[0])
		return n
	}
	before := count()
	if err := f.typeInto(ctx, target, line); err != nil {
		return err
	}
	for range 15 {
		f.Sleep(2 * time.Second)
		if count() > before { // a NEW "Goal set" line, not an older goal's
			f.printf("goal set on %s: %s…\n", name, firstRunes(line[len("/goal "):], 110))
			return nil
		}
	}
	return fmt.Errorf(`no "Goal set" in %s's transcript after 30 s: attach and check (brain agents attach %s)`, name, name)
}

// Resume reopens a saved devbox conversation in an Orca terminal, in the folder its transcript was recorded in.
func (f *Squad) Resume(ctx context.Context, name, session string, force bool) error {
	if err := f.onlyDevbox("resume"); err != nil {
		return err
	}
	if err := f.needDevbox(ctx); err != nil {
		return err
	}
	out, err := f.Host.Shell(ctx, "find "+Quote(f.Projects)+" -maxdepth 2 -name "+Quote(session+"*.jsonl"))
	if err != nil {
		return err
	}
	found := strings.Fields(out)
	if len(found) != 1 {
		return fmt.Errorf("need exactly one devbox transcript for %q, found %d (move it first?)", session, len(found))
	}
	sid := strings.TrimSuffix(filepath.Base(found[0]), ".jsonl")
	written, err := f.Host.modTime(ctx, found[0])
	if err != nil {
		return err
	}
	if age := f.Now().Sub(written); age < freshTranscript && !force {
		// two Claude processes appending to one transcript corrupt it
		return fmt.Errorf("the transcript was written %.0fs ago: stop that session first (brain agents stop %s), or --force", age.Seconds(), name)
	}
	cwd, err := f.Host.transcriptCWD(ctx, found[0])
	if err != nil || cwd == "" {
		return fmt.Errorf("no working folder recorded in %s (%v)", found[0], err)
	}
	handle, err := f.start(ctx, name, cwd, claudeCommand+" --resume "+sid)
	if err != nil {
		return err
	}
	if err := f.nameSession(ctx, handle, name); err != nil {
		return err
	}
	f.printf("%s: session %s resumed in Orca at %s\n", name, sid, cwd)
	return nil
}

// Move takes a laptop session to the devbox: its worktree (same path), transcript, subagents, file history and
// session env, then resumes it in Orca.
func (f *Squad) Move(ctx context.Context, session, name string, force, noBoxHint bool) error {
	if f.Here != config.Laptop || !f.Host.Remote {
		return errors.New("move takes a laptop session to the devbox: run it on the laptop")
	}
	found, _ := filepath.Glob(filepath.Join(f.Projects, "*", session+"*.jsonl"))
	if len(found) != 1 {
		return fmt.Errorf("need exactly one laptop transcript for %q, found %d", session, len(found))
	}
	transcript := found[0]
	sid, project := strings.TrimSuffix(filepath.Base(transcript), ".jsonl"), filepath.Dir(transcript)
	cwd, err := transcriptCWDOf(transcript)
	if err != nil || cwd == "" {
		return fmt.Errorf("no working folder recorded in %s (%v)", transcript, err)
	}
	f.printf("session %s\n  in %s\n", sid, cwd)
	st, err := os.Stat(transcript)
	if err != nil {
		return err
	}
	if age := f.Now().Sub(st.ModTime()); age < freshTranscript && !force {
		return fmt.Errorf("the transcript was written %.0fs ago: /exit that session on the laptop first (or --force)", age.Seconds())
	}
	if err := f.needDevbox(ctx); err != nil {
		return err
	}
	if existing, err := f.agent(ctx, name); err != nil {
		return err
	} else if existing != nil {
		return fmt.Errorf("an agent named %s already runs in Orca (see: brain agents list)", name)
	}

	// 1. the worktree, at the same path (devbox-handoff never overwrites an existing checkout)
	_, gitErr := f.Host.Run(ctx, "git", "-C", cwd, "rev-parse", "--is-inside-work-tree")
	_, missing := f.Host.Run(ctx, SSH[0], append(append([]string{}, SSH[1:]...), config.DevboxHost, "test -e "+Quote(cwd))...)
	switch {
	case gitErr == nil && missing != nil:
		if err := f.Stream(ctx, filepath.Join(config.BinDir(), "devbox-handoff"), cwd); err != nil {
			return err
		}
	case gitErr == nil:
		remote, _ := f.Host.Shell(ctx, "git -C "+Quote(cwd)+" log -1 --format=%h")
		local, _ := f.Host.Run(ctx, "git", "-C", cwd, "log", "-1", "--format=%h")
		f.printf("  worktree already on the devbox: %s (laptop %s)\n", strings.TrimSpace(remote), strings.TrimSpace(string(local)))
	default:
		if _, err := f.Host.Shell(ctx, "mkdir -p "+Quote(cwd)); err != nil {
			return err
		}
	}

	// 2. the conversation: transcript, subagents + tool results, file history (rewind), session env
	claude := config.ClaudeDir()
	fileHistory, sessionEnv := filepath.Join(claude, "file-history"), filepath.Join(claude, "session-env")
	if _, err := f.Host.Shell(ctx, "mkdir -p "+Quote(project)+" "+Quote(fileHistory)+" "+Quote(sessionEnv)); err != nil {
		return err
	}
	for _, pair := range [][2]string{{transcript, project}, {filepath.Join(project, sid), project},
		{filepath.Join(fileHistory, sid), fileHistory}, {filepath.Join(sessionEnv, sid), sessionEnv}} {
		if _, err := os.Stat(pair[0]); err == nil {
			if err := f.rsync(ctx, pair[0], config.DevboxHost+":"+pair[1]+"/"); err != nil {
				return err
			}
		}
	}

	// 3. notes and memory (text), then resume in Orca
	if _, err := f.Host.Run(ctx, filepath.Join(config.BinDir(), "devbox"), "sync"); err != nil {
		return fmt.Errorf("devbox sync failed (%s)%s", exitText(err), stderrSuffix(err))
	}
	handle, err := f.start(ctx, name, cwd, claudeCommand+" --resume "+sid)
	if err != nil {
		return err
	}
	if err := f.nameSession(ctx, handle, name); err != nil {
		return err
	}
	f.printf("\n%s runs in Orca (host devbox-host-server); its screen:  brain agents screen %s\n", name, name)
	if !noBoxHint {
		f.printf("if its box has artifacts/ to bring along:  brain agents box <box>\n")
	}
	return nil
}

// Stop ends a finished agent (its transcript stays); with rmWorktree, also removes its worktree when that sits under
// the worktrees root and is clean.
func (f *Squad) Stop(ctx context.Context, name string, rmWorktree bool) error {
	if err := f.needDevbox(ctx); err != nil {
		return err
	}
	agent, err := f.agent(ctx, name)
	if err != nil {
		return err
	}
	var dirs []string
	if agent != nil {
		if _, err := f.Host.Orca(ctx, "terminal", "close", "--terminal", agent.Handle); err != nil {
			return err
		}
		if err := f.register(ctx, name, ""); err != nil {
			return err
		}
		dirs = []string{agent.WorktreePath}
		f.printf("%s stopped; its transcript stays for  brain agents resume %s <session>\n", name, name)
	} else {
		if err := f.onlyDevbox("stop of a Happier agent"); err != nil {
			return err
		}
		names, err := f.happierNames(ctx)
		if err != nil {
			return err
		}
		for _, entry := range names {
			if entry.Name == name {
				dirs = append(dirs, entry.Dir)
			}
		}
		out, err := f.Host.Shell(ctx, filepath.Join(f.DevboxBin, "agent-stop")+" "+Quote(name))
		f.printf("%s", out)
		if err != nil {
			return err
		}
	}
	if !rmWorktree {
		return nil
	}
	for _, dir := range dirs {
		clean, under := underRoot(dir, f.Worktrees)
		if !under {
			continue
		}
		// git refuses a dirty worktree, so nothing unsaved is ever lost
		out, err := f.Host.Shell(ctx, "git -C "+Quote(f.Repo)+" worktree remove "+Quote(clean)+" && echo "+Quote("removed "+clean))
		f.printf("%s", out)
		if err != nil {
			f.printf("%v\n", err)
		}
	}
	return nil
}

// underRoot reports whether dir (either home spelling) lies strictly inside root once cleaned, so "<root>/../x" and a
// sibling "<root>-old" do not count; it returns the cleaned path.
func underRoot(dir, root string) (string, bool) {
	if !filepath.IsAbs(dir) {
		return "", false
	}
	clean := filepath.Clean(dir)
	rel, err := filepath.Rel(filepath.Clean(root), config.Canonical(clean))
	return clean, err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../")
}

// Box copies a box of the brain's work/ from the laptop to the devbox with the bulk Unison leaves out (artifacts,
// big files): it adds files and replaces older ones, and never deletes on the devbox.
func (f *Squad) Box(ctx context.Context, box string, excludes []string, withVenv bool) error {
	if f.Here != config.Laptop || !f.Host.Remote {
		return errors.New("box copies a laptop box to the devbox: run it on the laptop")
	}
	if err := f.needDevbox(ctx); err != nil {
		return err
	}
	local := strings.TrimRight(box, "/")
	if !filepath.IsAbs(local) {
		local = filepath.Join(config.BrainRoot(), "work", local)
	}
	if st, err := os.Stat(local); err != nil || !st.IsDir() {
		return fmt.Errorf("no such box on the laptop: %s", local)
	}
	var flags []string
	if !withVenv {
		flags = append(flags, "--exclude=venv/", "--exclude=.venv/")
	}
	flags = append(flags, "--exclude=__pycache__/", "--exclude=node_modules/", "--exclude=.git/")
	for _, x := range excludes {
		flags = append(flags, "--exclude="+x) // one argument, so a value can never become an rsync flag
	}
	venvNote := "; skips venvs"
	if withVenv {
		venvNote = ""
	}
	f.printf("copy %s  (adds files and replaces OLDER ones; a newer devbox file is kept; never deletes on the devbox%s)\n", local, venvNote)
	return f.rsync(ctx, local+"/", config.DevboxHost+":"+local+"/", append([]string{"--compress", "--update"}, flags...)...)
}

func (f *Squad) rsync(ctx context.Context, source, dest string, extra ...string) error {
	rsync := "rsync"
	if _, err := os.Stat("/opt/homebrew/bin/rsync"); err == nil {
		rsync = "/opt/homebrew/bin/rsync"
	}
	args := append(append([]string{"-a", "--partial", "--info=progress2"}, extra...), "-e", "ssh -o ControlPath=none", source, dest)
	return f.Stream(ctx, rsync, args...)
}

// ---- helpers ----

func indexOf(terminals []Terminal, match func(Terminal) bool) int {
	for i, t := range terminals {
		if match(t) {
			return i
		}
	}
	return -1
}

func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
