// Package session turns a Claude Code transcript into the brain's session note (FORMAT.md "Session note"): what the
// owner asked, which files every tool touched, the PRs, corrections and messages, and a short account of the work.
package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
)

const (
	// MaxLineBytes bounds one transcript line; longer lines (huge tool outputs) are skipped and counted.
	MaxLineBytes   = 8 << 20
	maxPromptRunes = 500
	maxKeptPrompts = 200
	maxCompact     = 4 * 1024
)

// Session is what one transcript (and its folded subagent transcripts) says about the session.
type Session struct {
	ID           string
	Transcript   string // canonical path
	Renamed      string // the /rename (or agent) name, "" when never named
	AITitle      string
	CWD          string
	Started      time.Time
	Ended        time.Time
	Prompts      []Prompt // owner prompts, redacted, ≤ maxKeptPrompts kept
	PromptCount  int
	Corrections  int
	Summaries    []string
	Compact      string // the last compact summary, redacted
	FinalText    string // raw; redacted when rendered
	Messages     []Message
	PRs          []string
	Subagents    int
	SkippedLines int
	Entries      int // user + assistant entries in the main transcript
	paths        pathCounter
}

type Prompt struct {
	At   time.Time
	Text string
}

// Message is a note between sessions; an empty From or To means this session.
type Message struct {
	From, To, ID string
}

// Name is the session's display name: its /rename name, else Claude Code's own title.
func (s *Session) Name() string {
	if s.Renamed != "" {
		return s.Renamed
	}
	return s.AITitle
}

// entry holds a transcript line's top-level fields; message and attachment stay raw (and alias the line buffer) until
// a handler needs them, because most of a transcript's bytes are tool outputs nobody reads.
type entry struct {
	Type             string
	Timestamp        string
	CWD              string
	IsSidechain      bool
	IsMeta           bool
	IsCompactSummary bool
	Origin           *origin
	Message          []byte
	Attachment       []byte
	Content          []byte
	Summary          string
	CustomTitle      string
	AgentName        string
	AITitle          string
	PRNumber         int
	PRRepository     string
}

func decodeEntry(line []byte) (entry, bool) {
	var e entry
	ok := eachField(line, func(key string, val []byte) {
		switch key {
		case "type":
			e.Type = jsonString(val)
		case "timestamp":
			e.Timestamp = jsonString(val)
		case "cwd":
			e.CWD = jsonString(val)
		case "isSidechain":
			e.IsSidechain = string(val) == "true"
		case "isMeta":
			e.IsMeta = string(val) == "true"
		case "isCompactSummary":
			e.IsCompactSummary = string(val) == "true"
		case "origin":
			e.Origin = decodeOrigin(val)
		case "message":
			e.Message = val
		case "attachment":
			e.Attachment = val
		case "content":
			e.Content = val
		case "summary":
			e.Summary = jsonString(val)
		case "customTitle":
			e.CustomTitle = jsonString(val)
		case "agentName":
			e.AgentName = jsonString(val)
		case "aiTitle":
			e.AITitle = jsonString(val)
		case "prNumber":
			e.PRNumber, _ = strconv.Atoi(string(val))
		case "prRepository":
			e.PRRepository = jsonString(val)
		}
	})
	return e, ok
}

func decodeOrigin(val []byte) *origin {
	var o origin
	if json.Unmarshal(val, &o) != nil {
		return nil
	}
	return &o
}

type origin struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	MsgID string `json:"msg_id"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

type attachment struct {
	Type    string
	Content []byte
	Prompt  []byte
	Stdout  string
	Origin  *origin
}

func decodeAttachment(raw []byte) attachment {
	var a attachment
	eachField(raw, func(key string, val []byte) {
		switch key {
		case "type":
			a.Type = jsonString(val)
		case "content":
			a.Content = val
		case "prompt":
			a.Prompt = val
		case "stdout":
			a.Stdout = jsonString(val)
		case "origin":
			a.Origin = decodeOrigin(val)
		}
	})
	return a
}

// field returns the raw value of one top-level key of a JSON object, nil when absent.
func field(obj []byte, name string) []byte {
	var out []byte
	eachField(obj, func(key string, val []byte) {
		if key == name {
			out = val
		}
	})
	return out
}

type toolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Path         string `json:"path"`
	Command      string `json:"command"`
}

// Parse reads a main transcript and folds in its subagent transcripts. It streams line by line and only reads.
func Parse(transcript string) (*Session, error) {
	s := &Session{
		ID:         strings.TrimSuffix(filepath.Base(transcript), ".jsonl"),
		Transcript: config.Canonical(transcript),
		paths:      pathCounter{uses: map[string]*pathUse{}},
	}
	if err := parseFile(s, transcript, false); err != nil {
		return nil, err
	}
	for _, sub := range subagentFiles(transcript) {
		if err := parseFile(s, sub, true); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		s.Subagents++
	}
	return s, nil
}

func parseFile(s *Session, path string, isSubagent bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	p := &parser{s: s, sub: isSubagent, pending: map[string]wanted{}, seenMsg: map[string]bool{}, seenPR: map[string]bool{}}
	for _, m := range s.Messages {
		p.seenMsg[m.ID] = true
	}
	for _, pr := range s.PRs {
		p.seenPR[pr] = true
	}
	skipped, err := readLines(f, MaxLineBytes, func(line []byte) error {
		if e, ok := decodeEntry(line); ok {
			p.entry(&e)
		}
		return nil
	})
	s.SkippedLines += skipped
	return err
}

// readLines calls fn for each line of at most maxLine bytes (the slice is only valid during the call) and returns
// how many longer lines it skipped.
func readLines(r io.Reader, maxLine int, fn func([]byte) error) (int, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var buf []byte
	skipped, overflow := 0, false
	for {
		chunk, err := br.ReadSlice('\n')
		complete := !errors.Is(err, bufio.ErrBufferFull)
		switch {
		case overflow:
		case len(buf)+len(chunk) > maxLine:
			overflow, buf = true, buf[:0]
		case complete && len(buf) == 0:
			if len(chunk) > 0 {
				if ferr := fn(chunk); ferr != nil {
					return skipped, ferr
				}
			}
		default:
			buf = append(buf, chunk...)
			if complete {
				if ferr := fn(buf); ferr != nil {
					return skipped, ferr
				}
				buf = buf[:0]
			}
		}
		if !complete {
			continue
		}
		if overflow {
			skipped++
			overflow = false
		}
		if err == io.EOF {
			return skipped, nil
		}
		if err != nil {
			return skipped, err
		}
	}
}

// subagentFiles are <transcript minus .jsonl>/subagents/**/agent-*.jsonl; workflow journals are not transcripts.
func subagentFiles(transcript string) []string {
	var files []string
	root := filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.HasPrefix(d.Name(), "agent-") && strings.HasSuffix(d.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// MainTranscript maps a subagent transcript to the main one it folds into.
func MainTranscript(path string) string {
	if i := strings.Index(path, string(filepath.Separator)+"subagents"+string(filepath.Separator)); i >= 0 {
		return path[:i] + ".jsonl"
	}
	return path
}

type wanted int

const (
	wantPR wanted = iota + 1
	wantSentNote
)

type parser struct {
	s       *Session
	sub     bool
	pending map[string]wanted // tool_use id → what its result is scanned for
	seenMsg map[string]bool
	seenPR  map[string]bool
}

func (p *parser) entry(e *entry) {
	main := !p.sub && !e.IsSidechain
	if !p.sub {
		if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
			if p.s.Started.IsZero() || t.Before(p.s.Started) {
				p.s.Started = t
			}
			if t.After(p.s.Ended) {
				p.s.Ended = t
			}
		}
		if p.s.CWD == "" && e.CWD != "" {
			p.s.CWD = config.Canonical(e.CWD)
		}
	}
	switch e.Type {
	case "custom-title":
		if !p.sub && e.CustomTitle != "" {
			p.s.Renamed = e.CustomTitle
		}
	case "agent-name":
		if !p.sub && e.AgentName != "" && p.s.Renamed == "" {
			p.s.Renamed = e.AgentName
		}
	case "ai-title":
		if !p.sub && e.AITitle != "" {
			p.s.AITitle = e.AITitle
		}
	case "summary":
		if summary := Redact(e.Summary); !p.sub && summary != "" && len(p.s.Summaries) < 10 && !contains(p.s.Summaries, summary) {
			p.s.Summaries = append(p.s.Summaries, summary)
		}
	case "pr-link":
		if e.PRRepository != "" && e.PRNumber > 0 {
			p.addPR(e.PRRepository + "#" + strconv.Itoa(e.PRNumber))
		}
	case "user":
		if main {
			p.s.Entries++
		}
		p.user(e, main)
	case "assistant":
		if main {
			p.s.Entries++
		}
		p.assistant(e, main)
	case "attachment":
		p.attachment(e, main)
	case "system":
		p.injected(textOf(e.Content))
	}
}

func (p *parser) user(e *entry, main bool) {
	if e.Message == nil {
		return
	}
	human := e.Origin != nil && e.Origin.Kind == "human" && main && !e.IsMeta
	peer := e.Origin != nil && e.Origin.Kind == "peer"
	if peer {
		p.addMessage(Message{From: "peer:" + e.Origin.Name, ID: e.Origin.MsgID})
	}
	if !e.IsCompactSummary && !human && !e.IsMeta && len(p.pending) == 0 {
		return // a tool result nobody waits for: never decoded
	}
	content := field(e.Message, "content")
	switch {
	case e.IsCompactSummary:
		if main {
			p.s.Compact = truncateBytes(Redact(stripCompactPreamble(textOf(content))), maxCompact)
		}
		return
	case human:
		p.prompt(textOf(content), e.Timestamp)
		return
	case e.IsMeta:
		p.injected(textOf(content))
		return
	case !isArray(content):
		return
	}
	var blocks []block
	if json.Unmarshal(content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if want, ok := p.pending[b.ToolUseID]; ok && b.Type == "tool_result" {
			delete(p.pending, b.ToolUseID)
			p.toolResult(want, textOf(b.Content))
		}
	}
}

func (p *parser) assistant(e *entry, main bool) {
	content := field(e.Message, "content")
	if !isArray(content) {
		return
	}
	var blocks []block
	if json.Unmarshal(content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			p.toolUse(b, e.CWD)
		case "text":
			if main && strings.TrimSpace(b.Text) != "" {
				p.s.FinalText = b.Text
			}
		}
	}
}

func (p *parser) attachment(e *entry, main bool) {
	if e.Attachment == nil {
		return
	}
	a := decodeAttachment(e.Attachment)
	switch {
	case a.Type == "queued_command" && a.Origin != nil && a.Origin.Kind == "human":
		if main {
			p.prompt(textOf(a.Prompt), e.Timestamp)
		}
	case a.Type == "queued_command" && a.Origin != nil && a.Origin.Kind == "peer":
		p.addMessage(Message{From: "peer:" + a.Origin.Name, ID: a.Origin.MsgID})
	case a.Type == "hook_success":
		// A Stop hook's {"decision":"block","reason":…} only shows up here, JSON inside the stdout string.
		if strings.Contains(a.Stdout, " NOTE ") {
			for _, s := range jsonStrings(a.Stdout) {
				p.injected(s)
			}
		}
	case strings.HasPrefix(a.Type, "hook_"):
		p.injected(textOf(a.Content))
	}
}

var (
	brainNoteRe = regexp.MustCompile(`BRAIN NOTE (n-[0-9A-Za-z-]+) from (\S+) \(sent `)
	sentNoteRe  = regexp.MustCompile(`(?:DELIVERED to|QUEUED for) (\S+) .*?\(note (n-[0-9A-Za-z-]+)\)`)
	prURLRe     = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([0-9]+)`)
)

const advisorMarker = "ADVISOR NOTE ("

// injected scans text a hook or the harness put into the session for mesh notes. Old devbox-agent advisor notes carry
// no id or sender, so their id is a hash of the note.
func (p *parser) injected(text string) {
	for _, m := range brainNoteRe.FindAllStringSubmatch(text, -1) {
		p.addMessage(Message{From: m[2], ID: m[1]})
	}
	if i := strings.Index(text, advisorMarker); i >= 0 {
		sum := sha256.Sum256([]byte(text[i:]))
		p.addMessage(Message{From: "advisor", ID: "advisor-" + hex.EncodeToString(sum[:4])})
	}
}

func (p *parser) addMessage(m Message) {
	if m.ID == "" || p.seenMsg[m.ID] || len(p.s.Messages) >= 50 {
		return
	}
	p.seenMsg[m.ID] = true
	p.s.Messages = append(p.s.Messages, m)
}

func (p *parser) addPR(pr string) {
	if p.seenPR[pr] || len(p.s.PRs) >= 30 {
		return
	}
	p.seenPR[pr] = true
	p.s.PRs = append(p.s.PRs, pr)
}

func (p *parser) prompt(text, ts string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	p.s.PromptCount++
	if p.s.PromptCount > 1 && isCorrection(text) {
		p.s.Corrections++
	}
	if len(p.s.Prompts) >= maxKeptPrompts {
		return
	}
	at, _ := time.Parse(time.RFC3339Nano, ts)
	p.s.Prompts = append(p.s.Prompts, Prompt{At: at, Text: truncateRunes(collapseSpace(Redact(text)), maxPromptRunes)})
}

var (
	correctionStart = regexp.MustCompile(`^(no|nope|stop|wrong|actually|instead|don't|dont|do not|why did you|that's not|thats not|not what)\b`)
	correctionWords = regexp.MustCompile(`\b(don't|do not|stop|wrong|actually|instead|why did you)\b`)
)

// isCorrection is a deliberately simple, deterministic heuristic: a follow-up owner prompt (never the first) that
// starts with no/nope/stop/wrong/actually/instead/don't/why did you…, or uses don't/do not/stop/wrong/actually/
// instead/why did you within its first 300 characters. Prompts over 1,500 characters are pasted briefs, not
// corrections.
func isCorrection(text string) bool {
	if len(text) > 1500 {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, "’", "'")))
	if correctionStart.MatchString(t) {
		return true
	}
	return correctionWords.MatchString(truncateBytes(t, 300))
}

func (p *parser) toolUse(b block, cwd string) {
	var in toolInput
	if json.Unmarshal(b.Input, &in) != nil {
		return
	}
	writes := b.Name == "Write" || b.Name == "Edit" || b.Name == "MultiEdit" || b.Name == "NotebookEdit"
	use := map[string]bool{} // path → produced; one use per path per tool call
	for _, raw := range []string{in.FilePath, in.NotebookPath, in.Path} {
		if path, ok := normalisePath(raw, cwd); ok {
			use[path] = use[path] || writes
		}
	}
	if in.Command != "" {
		for _, raw := range commandPaths(in.Command) {
			if path, ok := normalisePath(raw, ""); ok && !use[path] {
				use[path] = false
			}
		}
		for _, raw := range commandOutputs(in.Command) {
			if path, ok := normalisePath(raw, ""); ok {
				use[path] = true
			}
		}
		if strings.Contains(in.Command, "gh pr create") {
			p.pending[b.ID] = wantPR
		} else if strings.Contains(in.Command, "agents note") {
			p.pending[b.ID] = wantSentNote
		}
	}
	if strings.Contains(b.Name, "create_pull_request") {
		p.pending[b.ID] = wantPR
	}
	for path, produced := range use {
		p.s.paths.add(path, produced)
	}
}

func (p *parser) toolResult(want wanted, text string) {
	switch want {
	case wantPR:
		for _, m := range prURLRe.FindAllStringSubmatch(text, -1) {
			p.addPR(m[1] + "#" + m[2])
		}
	case wantSentNote:
		for _, m := range sentNoteRe.FindAllStringSubmatch(text, -1) {
			p.addMessage(Message{To: m[1], ID: m[2]})
		}
	}
}

// ---- paths ----

func isPathByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._@%+=~/-", c) >= 0
}

// pathAt is the path-like token starting at i; literal is false when a shell variable, brace or glob continues
// it, so the token is not a file name.
func pathAt(cmd string, i int) (tok string, literal bool) {
	j := i
	for j < len(cmd) && isPathByte(cmd[j]) {
		j++
	}
	return cmd[i:j], j == len(cmd) || strings.IndexByte("$*?[{", cmd[j]) < 0
}

// commandPaths are the absolute and ~ paths in a shell command, each starting at a word boundary so URL tails
// (https://…) do not count; a variable or glob path counts as its literal directory. A byte scan: commands carry
// whole heredoc files and regexes backtrack on them.
func commandPaths(cmd string) []string {
	var out []string
	for i := 0; i < len(cmd); i++ {
		if (cmd[i] != '/' && cmd[i] != '~') || (i > 0 && strings.IndexByte(" \t\n'\"=(<>|;&", cmd[i-1]) < 0) {
			continue
		}
		tok, literal := pathAt(cmd, i)
		i += len(tok)
		if !literal {
			tok = tok[:strings.LastIndexByte(tok, '/')+1]
		}
		if fromRoot(tok) && strings.Count(strings.Trim(tok, "/"), "/") >= 1 {
			out = append(out, tok)
		}
	}
	return out
}

// commandOutputs are files a command writes through a redirect (>, >>, &>, 2>) or tee.
func commandOutputs(cmd string) []string {
	var out []string
	target := func(j int) {
		for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
			j++
		}
		if j < len(cmd) && (cmd[j] == '"' || cmd[j] == '\'') {
			j++ // a quoted target; a quote before the spaces means the > sat inside a quoted pattern
		}
		if tok, literal := pathAt(cmd, j); literal && fromRoot(tok) {
			out = append(out, tok)
		}
	}
	for i := 0; i < len(cmd); i++ {
		switch {
		case cmd[i] == '>' && (i == 0 || (cmd[i-1] != '-' && cmd[i-1] != '=')): // not an arrow (->, =>)
			if i+1 < len(cmd) && cmd[i+1] == '>' {
				i++
			}
			target(i + 1)
		case strings.HasPrefix(cmd[i:], "tee ") && (i == 0 || !isPathByte(cmd[i-1])):
			j := i + len("tee ")
			if strings.HasPrefix(cmd[j:], "-a ") {
				j += len("-a ")
			}
			target(j)
		}
	}
	return out
}

// commandRoots are where real files live. A shell command also holds things that only look like absolute paths —
// "$ROOT"/.index, sed patterns, URL tails — so a command path must start at one of these.
var commandRoots = []string{"~/", "/Users/", "/home/", "/tmp/", "/private/", "/var/", "/opt/", "/mnt/", "/Volumes/",
	"/workspace/", "/srv/", "/data/", "/root/"}

func fromRoot(path string) bool {
	for _, root := range commandRoots {
		if strings.HasPrefix(path, root) {
			return true
		}
	}
	return false
}

var systemPrefixes = []string{"/dev/", "/proc/", "/sys/", "/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/", "/etc/", "/run/",
	"/nix/", "/opt/homebrew/", "/System/", "/Library/"}

// normalisePath gives the stored spelling: canonical home, saahil-docs boxes as work/<box>/…, ~/brain as relative.
func normalisePath(raw, cwd string) (string, bool) {
	p := strings.TrimRight(strings.Trim(strings.TrimSpace(raw), `"'`), ".,:;)]}")
	switch {
	case p == "" || p == "~":
		return "", false
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(config.Home(), p[2:])
	case !filepath.IsAbs(p):
		if cwd == "" {
			return "", false
		}
		p = filepath.Join(cwd, p)
	}
	p = config.Canonical(p)
	for _, prefix := range systemPrefixes {
		if strings.HasPrefix(p+"/", prefix) {
			return "", false
		}
	}
	if i := strings.Index(p+"/", "/saahil-docs/"); i >= 0 {
		return strings.TrimSuffix("work/"+strings.TrimPrefix(p[i+len("/saahil-docs"):], "/"), "/"), true
	}
	if root := config.Canonical(config.BrainRoot()); strings.HasPrefix(p, root+"/") {
		return strings.TrimPrefix(p, root+"/"), true
	}
	return p, true
}

// boxOf is the work box a stored path lies in, "" when none.
func boxOf(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "work" || parts[1] == "" || strings.HasPrefix(parts[1], ".") {
		return ""
	}
	if len(parts) == 2 && strings.Contains(parts[1], ".") {
		return "" // a file at the saahil-docs root, not a box
	}
	return parts[1]
}

type pathUse struct {
	uses     int
	produced bool
}

type pathCounter struct {
	uses map[string]*pathUse
}

func (c *pathCounter) add(path string, produced bool) {
	u := c.uses[path]
	if u == nil {
		u = &pathUse{}
		c.uses[path] = u
	}
	u.uses++
	u.produced = u.produced || produced
}

// ranked lists paths most-used first, ties by path, so the order never depends on read order.
func (c *pathCounter) ranked(producedOnly bool) []string {
	var out []string
	for path, u := range c.uses {
		if !producedOnly || u.produced {
			out = append(out, path)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := c.uses[out[i]].uses, c.uses[out[j]].uses
		if a != b {
			return a > b
		}
		return out[i] < out[j]
	})
	return out
}

// works ranks the boxes by how much the session used their files.
func (c *pathCounter) works() []string {
	weight := map[string]int{}
	for path, u := range c.uses {
		if box := boxOf(path); box != "" {
			weight[box] += u.uses
		}
	}
	out := make([]string, 0, len(weight))
	for box := range weight {
		out = append(out, box)
	}
	sort.Slice(out, func(i, j int) bool {
		if weight[out[i]] != weight[out[j]] {
			return weight[out[i]] > weight[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// ---- text helpers ----

// eachField calls fn with each top-level key of a JSON object and the raw bytes of its value. It is one pass that
// steps over values without decoding them, so a megabyte tool output costs a scan, not a parse; malformed input
// reports false.
func eachField(obj []byte, fn func(key string, val []byte)) bool {
	i := skipSpace(obj, 0)
	if i >= len(obj) || obj[i] != '{' {
		return false
	}
	i++
	for {
		i = skipSpace(obj, i)
		if i < len(obj) && obj[i] == '}' {
			return true
		}
		if i >= len(obj) || obj[i] != '"' {
			return false
		}
		keyEnd := stringEnd(obj, i)
		if keyEnd < 0 {
			return false
		}
		key := obj[i+1 : keyEnd-1]
		i = skipSpace(obj, keyEnd)
		if i >= len(obj) || obj[i] != ':' {
			return false
		}
		i = skipSpace(obj, i+1)
		end := valueEnd(obj, i)
		if end < 0 {
			return false
		}
		fn(string(key), obj[i:end])
		i = skipSpace(obj, end)
		switch {
		case i < len(obj) && obj[i] == ',':
			i++
		case i < len(obj) && obj[i] == '}':
			return true
		default:
			return false
		}
	}
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// stringEnd returns the index just past the string starting at b[i] == '"', or -1. A quote ends the string when an
// even number of backslashes precedes it.
func stringEnd(b []byte, i int) int {
	for j := i + 1; ; j++ {
		k := bytes.IndexByte(b[j:], '"')
		if k < 0 {
			return -1
		}
		j += k
		backslashes := 0
		for m := j - 1; m > i && b[m] == '\\'; m-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return j + 1
		}
	}
}

// valueEnd returns the index just past the JSON value starting at b[i], or -1.
func valueEnd(b []byte, i int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return stringEnd(b, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end := stringEnd(b, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	default:
		j := i
		for j < len(b) && strings.IndexByte(",}] \t\r\n", b[j]) < 0 {
			j++
		}
		return j
	}
}

// jsonString decodes a JSON string value; anything else is "".
func jsonString(val []byte) string {
	if len(val) >= 2 && val[0] == '"' && bytes.IndexByte(val, '\\') < 0 {
		return string(val[1 : len(val)-1])
	}
	var s string
	_ = json.Unmarshal(val, &s)
	return s
}

// textOf flattens message content: a string, or an array of strings and {type:text} blocks.
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var parts []string
	for _, item := range items {
		if json.Unmarshal(item, &s) == nil {
			parts = append(parts, s)
			continue
		}
		var b block
		if json.Unmarshal(item, &b) == nil && b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// jsonStrings returns every string leaf of a JSON document, or nothing when it is not JSON.
func jsonStrings(doc string) []string {
	var v any
	if json.Unmarshal([]byte(doc), &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	return out
}

func isArray(raw json.RawMessage) bool {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		}
		return false
	}
	return false
}

func stripCompactPreamble(text string) string {
	if strings.HasPrefix(text, "This session is being continued") {
		if _, rest, ok := strings.Cut(text, "Summary:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return text
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// truncateBytes cuts s to at most n bytes on a rune boundary, marking the cut with an ellipsis.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
