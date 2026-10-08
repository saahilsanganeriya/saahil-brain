package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testHandler(t *testing.T) *Handler {
	t.Helper()
	t.Setenv("BRAIN_HOME", t.TempDir())
	none := func(context.Context, string, ...string) ([]byte, error) { return []byte(`{}`), nil }
	return &Handler{RegistryDir: t.TempDir(), Run: none,
		Steps: &Steps{Dir: t.TempDir(), Allowed: map[string]Step{"version": {Args: []string{"version"}}}, Exe: "/bin/true"}}
}

// Catches a bridge that answers without the token (anyone on the devbox's loopback could then drive the laptop).
func TestBridgeRejectsMissingToken(t *testing.T) {
	token := strings.Repeat("k", 48)
	srv := httptest.NewServer(&Server{Handler: testHandler(t), Token: token})
	defer srv.Close()
	post := func(auth string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc", bytes.NewBufferString(`{"op":"status"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + token[:47], token} {
		if code := post(auth); code != http.StatusUnauthorized {
			t.Errorf("auth %q: status %d, want 401", auth, code)
		}
	}
	if code := post("Bearer " + token); code != http.StatusOK {
		t.Errorf("right token: status %d, want 200", code)
	}
	empty := &Server{Handler: testHandler(t)}
	rec := httptest.NewRecorder()
	empty.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewBufferString(`{"op":"status"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("server with no token configured answered %d, want 401", rec.Code)
	}
}

// Catches the bridge running anything but a named, compiled-in step (a shell by another name).
func TestBridgeRunsOnlyNamedSteps(t *testing.T) {
	h := testHandler(t)
	for _, bad := range []string{"", "rm -rf ~", "../version", "version; id", "VERSION"} {
		resp := h.Handle(context.Background(), Request{Op: "step", Step: bad})
		if resp.OK || !strings.Contains(resp.Error, "not allowed") {
			t.Errorf("step %q: ok=%v err=%q, want refused", bad, resp.OK, resp.Error)
		}
	}
	if entries, _ := os.ReadDir(h.Steps.Dir); len(entries) != 0 {
		t.Fatalf("refused steps left %d job files", len(entries))
	}
	resp := h.Handle(context.Background(), Request{Op: "step", Step: "version"})
	job, err := Decode[Job](resp)
	if err != nil || job.Step != "version" || !strings.HasPrefix(job.ID, "j-") {
		t.Fatalf("named step: %+v %v", job, err)
	}
	if resp := h.Handle(context.Background(), Request{Op: "shell", Text: "id"}); resp.OK {
		t.Fatal("unknown op answered")
	}
}

// Catches a note sent to a name that is not a live session silently vanishing instead of failing.
func TestNoteToUnknownSessionFails(t *testing.T) {
	h := testHandler(t)
	resp := h.Handle(context.Background(), Request{Op: "note", To: "laptop:nobody", From: "devbox:x", Text: "hi"})
	if resp.OK {
		var r NoteResult
		_ = json.Unmarshal(resp.Result, &r)
		t.Fatalf("note to an unknown session was accepted: %+v", r)
	}
}
