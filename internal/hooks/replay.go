package hooks

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// Funnel is the pointer funnel a replay re-counts. A pointer is a (session, id) pair: shown twice to one session, it
// counts once.
type Funnel struct {
	Transcripts, Sessions int // read; and among them, sessions shown at least one pointer
	Shown                 int
	OpenedFileTools       int // opened by Read/Grep/Glob: all the post hook counted before Bash reads and brain tools
	Opened                int // opened by any read the post hook counts now
	Followed              int // edited, never opened
	Ignored               int // shown, never opened or followed
	Pulled                int // sessions that called brain search, show, sections or neighbors
}

var pointerLineRE = regexp.MustCompile(`(?m)^- (\S+) — `)

// Replay re-counts one Claude Code transcript with the post hook's rules: every pointer a UserPromptSubmit hook
// injected (pathOf maps its id to a brain path), then each later tool call as PostToolUse would see it. Read-only.
func Replay(env Env, transcript io.Reader, pathOf func(id string) string, f *Funnel) error {
	type block struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	type entry struct {
		Type       string `json:"type"`
		SessionID  string `json:"sessionId"`
		Cwd        string `json:"cwd"`
		Attachment struct {
			Type      string          `json:"type"`
			HookEvent string          `json:"hookEvent"`
			Content   json.RawMessage `json:"content"`
		} `json:"attachment"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	st := state{Shown: map[string]string{}, Counted: map[string]bool{}}
	before := map[string]bool{}
	pulled := false
	br := bufio.NewReaderSize(transcript, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		var entry entry
		if len(line) > 0 && json.Unmarshal(line, &entry) == nil {
			switch {
			case entry.Type == "attachment" && entry.Attachment.Type == "hook_additional_context" &&
				entry.Attachment.HookEvent == "UserPromptSubmit":
				var texts []string
				if json.Unmarshal(entry.Attachment.Content, &texts) != nil {
					texts = []string{string(entry.Attachment.Content)}
				}
				for _, text := range texts {
					if !strings.HasPrefix(text, "Brain pointers") {
						continue
					}
					for _, m := range pointerLineRE.FindAllStringSubmatch(text, -1) {
						if _, ok := st.Shown[m[1]]; !ok {
							st.Shown[m[1]] = env.absPath(pathOf(m[1]))
						}
					}
				}
			case entry.Type == "assistant":
				var blocks []block
				_ = json.Unmarshal(entry.Message.Content, &blocks)
				for _, b := range blocks {
					if b.Type != "tool_use" {
						continue
					}
					in := Input{SessionID: entry.SessionID, Cwd: entry.Cwd, ToolName: b.Name, ToolInput: b.Input}
					use := classify(in)
					pulled = pulled || use.pull != ""
					for _, id := range use.hits(env, st) {
						st.Counted[use.field+":"+id] = true
						if use.field == "opened" && fileReaders[b.Name] {
							before[id] = true
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	f.Transcripts++
	if pulled {
		f.Pulled++
	}
	if len(st.Shown) == 0 {
		return nil
	}
	f.Sessions++
	for id := range st.Shown {
		f.Shown++
		switch {
		case st.Counted["opened:"+id]:
			f.Opened++
		case st.Counted["followed:"+id]:
			f.Followed++
		default:
			f.Ignored++
		}
		if before[id] {
			f.OpenedFileTools++
		}
	}
	return nil
}
