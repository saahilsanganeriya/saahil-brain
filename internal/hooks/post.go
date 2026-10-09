package hooks

import (
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"saahil.local/brain/internal/config"
)

// Post runs PostToolUse: when a tool call reads (opened) or edits (followed) a node this session was pointed at,
// count it once per session; when it pulls from the brain (search, show, sections, neighbors), mark the session once.
// Live bumps node_stats; both modes log a lookups row, which the dream's learn step and the doctor read. It reads one
// small state file and touches the index only on a hit.
func Post(env Env, in Input) error {
	use := classify(in)
	if use.field == "" && use.pull == "" {
		return nil
	}
	session := in.session()
	st, ok := env.loadState(session)
	var hits []string
	if ok {
		hits = use.hits(env, st)
	}
	// every pull is logged, with its channel: the pull rate counts sessions, the volume counts calls
	pull := use.pull != ""
	if len(hits) == 0 && !pull {
		return nil
	}
	ix, err := env.openIndex()
	if err != nil || ix == nil {
		return err
	}
	defer ix.Close()
	tx, err := ix.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	live := env.Live()
	for _, id := range hits {
		if live {
			if _, err := tx.Exec(`INSERT INTO node_stats(id, `+use.field+`) VALUES (?, 1) ON CONFLICT(id) DO UPDATE SET `+
				use.field+` = `+use.field+` + 1`, id); err != nil {
				return err
			}
		}
		if err := logLookup(tx, env.Now(), session, "post", []string{use.field}, []string{id}, false, 0, ""); err != nil {
			return err
		}
	}
	if pull {
		channel := "cli"
		if strings.HasPrefix(in.ToolName, "mcp__") {
			channel = "mcp"
		}
		if err := logLookup(tx, env.Now(), session, "pull", []string{use.pull, channel}, []string{}, false, 0, ""); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !ok || len(hits) == 0 {
		return nil
	}
	if st.Counted == nil {
		st.Counted = map[string]bool{}
	}
	for _, id := range hits {
		st.Counted[use.field+":"+id] = true
	}
	return env.saveState(session, st)
}

// toolUse is what one tool call read or edited, as far as the funnel cares.
type toolUse struct {
	field string   // opened | followed | ""
	files []string // canonical absolute paths
	dirs  []string // a Grep/Glob search root: every shown file under it
	refs  []string // ids or paths handed to brain show / sections
	pull  string   // the brain verb when the call pulled from the brain, e.g. brain_search
}

var (
	fileReaders = map[string]bool{"Read": true, "Grep": true, "Glob": true, "NotebookRead": true}
	fileEditors = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}
	brainPulls  = map[string]bool{"search": true, "show": true, "sections": true, "neighbors": true}
	brainOpens  = map[string]bool{"show": true, "sections": true}
)

// classify reads a PostToolUse payload: the file tools, the brain's MCP tools (mcp__<server>__brain_<verb>) and Bash.
func classify(in Input) toolUse {
	var ti struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		ID           string `json:"id"`
		Command      string `json:"command"`
	}
	if json.Unmarshal(in.ToolInput, &ti) != nil {
		return toolUse{}
	}
	name := in.ToolName
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	switch {
	case fileReaders[name] || fileEditors[name]:
		u := toolUse{field: "opened"}
		if fileEditors[name] {
			u.field = "followed"
		}
		switch {
		case ti.FilePath != "":
			u.files = []string{config.Canonical(ti.FilePath)}
		case ti.NotebookPath != "":
			u.files = []string{config.Canonical(ti.NotebookPath)}
		case ti.Path != "":
			u.dirs = []string{config.Canonical(ti.Path)}
		}
		return u
	case strings.HasPrefix(name, "brain_") && brainPulls[strings.TrimPrefix(name, "brain_")]:
		u := toolUse{pull: name}
		if brainOpens[strings.TrimPrefix(name, "brain_")] && ti.ID != "" {
			u.field, u.refs = "opened", []string{ti.ID}
		}
		return u
	case name == "Bash" && ti.Command != "":
		return bashUse(ti.Command, in.cwd())
	}
	return toolUse{}
}

func (u toolUse) hits(env Env, st state) []string {
	var hits []string
	for id, abs := range st.Shown {
		if st.Counted[u.field+":"+id] {
			continue
		}
		hit := slices.Contains(u.files, abs)
		for _, dir := range u.dirs {
			hit = hit || abs == dir || strings.HasPrefix(abs, dir+"/")
		}
		for _, ref := range u.refs {
			ref, _, _ = strings.Cut(ref, "#")
			hit = hit || ref == id || (filepath.IsAbs(ref) && config.Canonical(ref) == abs) || env.absPath(ref) == abs
		}
		if hit {
			hits = append(hits, id)
		}
	}
	sort.Strings(hits)
	return hits
}

// ---- what a Bash command reads ----

// shellReaders print or search the files they are given; a path to one of them is a read of that file.
var shellReaders = map[string]bool{"cat": true, "head": true, "tail": true, "less": true, "more": true, "sed": true,
	"grep": true, "egrep": true, "rg": true}

var (
	heredocRE = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
	assignRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// bashUse sees which files a command line reads (cat, sed without -i, head, tail, less, rg, grep) and whether it runs
// the brain CLI. It follows `cd`, NAME=value and $NAME / ${NAME} / ~ in order, and skips heredoc bodies and
// redirection targets: a reader's approximation of sh, not a shell.
func bashUse(command, cwd string) toolUse {
	var u toolUse
	vars := map[string]string{"HOME": config.Home()}
	for _, words := range shellCommands(dropHeredocs(command)) {
		args := make([]string, 0, len(words))
		for _, w := range words {
			args = append(args, w.expand(vars))
		}
		if len(args) > 0 && args[0] == "export" {
			args = args[1:]
		}
		// NAME=value alone sets a shell variable; before a command it is only that command's environment
		if !slices.ContainsFunc(args, func(a string) bool { return !assignRE.MatchString(a) }) {
			for _, a := range args {
				name, value, _ := strings.Cut(a, "=")
				vars[name] = value
			}
			continue
		}
		for assignRE.MatchString(args[0]) {
			args = args[1:]
		}
		verb, args := path.Base(args[0]), args[1:]
		switch {
		case verb == "cd" && len(args) > 0:
			cwd = resolve(cwd, args[0])
		case shellReaders[verb]:
			if verb == "sed" && slices.ContainsFunc(args, inPlace) {
				continue
			}
			for _, a := range args {
				if a != "" && !strings.HasPrefix(a, "-") {
					u.files = append(u.files, resolve(cwd, a))
				}
			}
		case verb == "brain" && len(args) > 0 && brainPulls[args[0]]:
			u.pull = "brain_" + args[0]
			if brainOpens[args[0]] {
				for _, a := range args[1:] {
					if !strings.HasPrefix(a, "-") {
						u.refs = append(u.refs, a)
						break
					}
				}
			}
		}
	}
	if len(u.files) > 0 || len(u.refs) > 0 {
		u.field = "opened"
	}
	return u
}

// inPlace is sed's -i, alone, with a suffix (-i.bak), in a flag cluster (-Ei) or spelled out: an edit, not a read.
func inPlace(arg string) bool {
	return strings.HasPrefix(arg, "--in-place") || (len(arg) > 1 && arg[0] == '-' && arg[1] != '-' && strings.ContainsRune(arg, 'i'))
}

func resolve(cwd, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return config.Canonical(p)
}

// dropHeredocs removes the bodies of here-documents: a script fed to python is not a command line.
func dropHeredocs(command string) string {
	lines := strings.Split(command, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		for _, m := range heredocRE.FindAllStringSubmatch(lines[i], -1) {
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != m[1] {
				i++
			}
			i++ // the delimiter line
		}
	}
	return strings.Join(out, "\n")
}

// word is one shell word as pieces; a single-quoted piece is never expanded.
type word []piece

type piece struct {
	text    string
	literal bool
}

var varRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

func (w word) expand(vars map[string]string) string {
	var b strings.Builder
	for i, p := range w {
		text := p.text
		if !p.literal {
			if i == 0 && (text == "~" || strings.HasPrefix(text, "~/")) {
				text = vars["HOME"] + text[1:]
			}
			text = varRE.ReplaceAllStringFunc(text, func(m string) string {
				name := strings.Trim(m, "${}")
				if v, ok := vars[name]; ok {
					return v
				}
				return m
			})
		}
		b.WriteString(text)
	}
	return b.String()
}

// shellCommands splits a command line into simple commands of words: at | & ; ( ) ` and newlines, with quotes
// removed, comments dropped and every redirection's target left out.
func shellCommands(line string) [][]word {
	var (
		cmds        [][]word
		cmd         []word
		cur         word
		buf         strings.Builder
		inWord      bool
		quote       rune // ' or " while inside one
		skipNext    bool // the next word is a redirection target
		literalPart bool
	)
	flushPiece := func() {
		if buf.Len() > 0 || quote != 0 {
			cur = append(cur, piece{text: buf.String(), literal: literalPart})
		}
		buf.Reset()
	}
	endWord := func() {
		flushPiece()
		if inWord {
			if !skipNext {
				cmd = append(cmd, cur)
			}
			skipNext = false
		}
		cur, inWord = nil, false
	}
	endCmd := func() {
		endWord()
		if len(cmd) > 0 {
			cmds = append(cmds, cmd)
		}
		cmd, skipNext = nil, false
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				flushPiece()
				quote, literalPart = 0, false
			} else {
				buf.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				flushPiece()
				quote = 0
			case r == '\\' && i+1 < len(rs):
				i++
				buf.WriteRune(rs[i])
			default:
				buf.WriteRune(r)
			}
		case r == '\'' || r == '"':
			flushPiece()
			quote, inWord, literalPart = r, true, r == '\''
		case r == '\\' && i+1 < len(rs):
			i++
			if rs[i] != '\n' {
				buf.WriteRune(rs[i])
				inWord = true
			}
		case r == ' ' || r == '\t':
			endWord()
		case r == '#' && !inWord:
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case r == '>' || r == '<':
			// 2>&1, >>, &>, <<<: whatever follows the operator is its target, not an argument
			if digits := buf.String(); inWord && strings.Trim(digits, "0123456789") == "" {
				buf.Reset()
				cur, inWord = nil, false
			} else {
				endWord()
			}
			for i+1 < len(rs) && strings.ContainsRune("<>&|", rs[i+1]) {
				i++
			}
			skipNext = true
		case strings.ContainsRune("|&;()`\n", r):
			endCmd()
		default:
			buf.WriteRune(r)
			inWord = true
		}
	}
	endCmd()
	return cmds
}
