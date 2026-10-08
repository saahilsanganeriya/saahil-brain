package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, root string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := Server(root).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// Catches a tool missing from (or renamed in) what Codex and Cursor see: the five brain tools, each with an
// object input schema.
func TestMCPToolsListed(t *testing.T) {
	cs := connect(t, t.TempDir())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema == nil {
			t.Errorf("%s has no input schema", tool.Name)
		}
	}
	slices.Sort(names)
	want := []string{"brain_neighbors", "brain_note", "brain_search", "brain_sections", "brain_show"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}
}

// Catches brain_note writing anywhere but inbox/ (the dream alone files notes) or losing what was said.
func TestNoteLandsInInbox(t *testing.T) {
	root := t.TempDir()
	cs := connect(t, root)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "brain_note",
		Arguments: map[string]any{"title": "OCR recall is 0.91", "body": "see work/box/reports/ocr.md", "hubs": []string{"image-lane"}}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "*"))
	if len(files) != 1 || filepath.Base(filepath.Dir(files[0])) != "inbox" {
		t.Fatalf("files written: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "hubs: [image-lane]") || !strings.Contains(string(b), "see work/box/reports/ocr.md") {
		t.Fatalf("capture:\n%s", b)
	}
}
