// Package mcpserver is `brain mcp`: the brain's five tools over MCP stdio for hosts without the Claude Code mod
// (Codex, Cursor, prime, omp). brain_note writes a capture into <brain>/inbox/ for the dream to file; no tool ever
// edits notes/ or hubs/.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/rpc"
)

type SearchIn struct {
	Q     string `json:"q" jsonschema:"what to look for: words, a name, an id"`
	Deep  bool   `json:"deep,omitempty" jsonschema:"also rank by graph proximity (slower)"`
	Limit int    `json:"limit,omitempty" jsonschema:"results, default 8, at most 50"`
}

type IDIn struct {
	ID string `json:"id" jsonschema:"a node id or path from brain_search"`
}

type NeighborsIn struct {
	ID    string `json:"id" jsonschema:"a node id or path from brain_search"`
	Depth int    `json:"depth,omitempty" jsonschema:"hops, default 1, at most 3"`
}

type NoteIn struct {
	Title string   `json:"title" jsonschema:"one line: the fact, decision or lesson"`
	Body  string   `json:"body" jsonschema:"the detail, with its evidence path"`
	Hubs  []string `json:"hubs,omitempty" jsonschema:"hub ids it belongs to, if known"`
	Type  string   `json:"type,omitempty" jsonschema:"finding, decision, lesson, idea... if known"`
}

// Server builds the MCP server over the brain at root. The index opens per call, so a long-lived server never
// holds a stale view or a lock across an index update.
func Server(root string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "brain", Version: rpc.Version}, nil)
	withIndex := func(run func(*index.Index) (any, error)) (*mcp.CallToolResult, any, error) {
		ix, err := index.Open(root)
		if err != nil {
			return nil, nil, err
		}
		defer ix.Close()
		v, err := run(ix)
		if err != nil {
			return nil, nil, err
		}
		return text(v), nil, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "brain_search", Description: "Search Saahil's brain (notes, hubs, work docs) for ranked ids " +
		"with one-line summaries. Use it before re-deriving a decision or a lesson."},
		func(_ context.Context, _ *mcp.CallToolRequest, in SearchIn) (*mcp.CallToolResult, any, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 8
			}
			return withIndex(func(ix *index.Index) (any, error) {
				return ix.Search(in.Q, index.SearchOpts{Limit: min(limit, 50), Deep: in.Deep})
			})
		})
	mcp.AddTool(s, &mcp.Tool{Name: "brain_show", Description: "One brain node (an id or path from brain_search): " +
		"its summary, fields and edges."},
		func(_ context.Context, _ *mcp.CallToolRequest, in IDIn) (*mcp.CallToolResult, any, error) {
			return withIndex(func(ix *index.Index) (any, error) { return ix.Show(in.ID) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "brain_sections", Description: "The sections of a big brain doc with line ranges, so " +
		"you Read one section instead of the whole file."},
		func(_ context.Context, _ *mcp.CallToolRequest, in IDIn) (*mcp.CallToolResult, any, error) {
			return withIndex(func(ix *index.Index) (any, error) { return ix.Sections(in.ID) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "brain_neighbors", Description: "Brain nodes linked to one node within depth hops " +
		"(default 1): its hubs, related notes, sources."},
		func(_ context.Context, _ *mcp.CallToolRequest, in NeighborsIn) (*mcp.CallToolResult, any, error) {
			depth := min(max(in.Depth, 1), 3)
			return withIndex(func(ix *index.Index) (any, error) { return ix.Neighbors(in.ID, depth) })
		})
	mcp.AddTool(s, &mcp.Tool{Name: "brain_note", Description: "Drop a short note into the brain's inbox for the dream " +
		"to file: a finding, decision or lesson with its evidence. Returns the capture's path."},
		func(_ context.Context, _ *mcp.CallToolRequest, in NoteIn) (*mcp.CallToolResult, any, error) {
			p, err := Capture(root, Note{Title: in.Title, Body: in.Body, Hubs: in.Hubs, Type: in.Type, Source: "mcp"}, time.Now())
			if err != nil {
				return nil, nil, err
			}
			return text(map[string]string{"path": p}), nil, nil
		})
	return s
}

// Serve runs the server on stdin/stdout until the client hangs up.
func Serve(ctx context.Context, root string) error {
	return Server(root).Run(ctx, &mcp.StdioTransport{})
}

func text(v any) *mcp.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprintf("%v", v))
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// ---- inbox captures ----

type Note struct {
	Title, Body, Type, Source string
	Hubs                      []string
}

const (
	maxCaptureBytes = 16 << 10
	maxTitleChars   = 160
)

var (
	slugRE  = regexp.MustCompile(`[^a-z0-9]+`)
	tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// Capture writes one note into <root>/inbox/ as markdown and returns its path. It never overwrites.
func Capture(root string, n Note, now time.Time) (string, error) {
	n.Title = strings.Join(strings.Fields(n.Title), " ")
	n.Body = strings.TrimSpace(n.Body)
	if n.Title == "" && n.Body == "" {
		return "", errors.New("a note needs a title or a body")
	}
	if n.Title == "" {
		n.Title = strings.Join(strings.Fields(n.Body), " ")
	}
	if r := []rune(n.Title); len(r) > maxTitleChars {
		n.Title = string(r[:maxTitleChars-1]) + "…"
	}
	if len(n.Body) > maxCaptureBytes {
		return "", fmt.Errorf("note is %d bytes (≤ %d): link the long text instead", len(n.Body), maxCaptureBytes)
	}
	for _, h := range n.Hubs {
		if !tokenRE.MatchString(h) {
			return "", fmt.Errorf("hub %q is not an id", h)
		}
	}
	if n.Type != "" && !tokenRE.MatchString(n.Type) {
		return "", fmt.Errorf("type %q is not a note type", n.Type)
	}
	dir := filepath.Join(root, "inbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	slug := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(n.Title), "-"), "-")
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "-")
	}
	var b strings.Builder
	b.WriteString("---\ntype: capture\n")
	fmt.Fprintf(&b, "title: %s\n", quote(n.Title))
	if n.Type != "" {
		fmt.Fprintf(&b, "note_type: %s\n", n.Type)
	}
	if len(n.Hubs) > 0 {
		fmt.Fprintf(&b, "hubs: [%s]\n", strings.Join(n.Hubs, ", "))
	}
	fmt.Fprintf(&b, "source: %s\nmachine: %s\ncaptured: %s\n---\n\n%s\n", n.Source, config.ThisMachine(),
		now.UTC().Format(time.RFC3339), n.Body)
	stamp := now.UTC().Format("20060102T150405Z")
	for i := 0; i < 100; i++ {
		name := stamp + "-" + slug + ".md"
		if i > 0 {
			name = fmt.Sprintf("%s-%s-%d.md", stamp, slug, i)
		}
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.WriteString(b.String()); err != nil {
			f.Close()
			return "", err
		}
		return p, f.Close()
	}
	return "", errors.New("inbox: no free name")
}

func quote(s string) string {
	b, _ := json.Marshal(s) // a JSON string is a valid YAML scalar
	return string(b)
}
