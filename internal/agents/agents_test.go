package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeRegistry(t *testing.T, dir string, entries ...registryFile) {
	t.Helper()
	for i, e := range entries {
		raw, _ := json.Marshal(e)
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func ownProcStart(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip("needs /proc")
	}
	fields := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
	return fields[19]
}

// Catches a stale registry file (dead pid) or a pid since reused by another process showing up as a live agent.
func TestListSkipsDeadSessions(t *testing.T) {
	dir := t.TempDir()
	pid := os.Getpid()
	writeRegistry(t, dir,
		registryFile{PID: 999_999_999, SessionID: "dead-session-0001", Name: "dead", Status: "idle"},
		registryFile{PID: pid, SessionID: "reused-pid-000002", Name: "reused", ProcStart: "1", Status: "idle"},
		registryFile{PID: pid, SessionID: "live-session-0003", Name: "live", ProcStart: ownProcStart(t), Status: "busy"},
	)
	sessions, err := LocalSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Name != "live" {
		t.Fatalf("want only the live session, got %+v", sessions)
	}
}

func withHome(t *testing.T) {
	t.Helper()
	t.Setenv("BRAIN_HOME", t.TempDir())
}

// Catches a note delivered twice when several hooks of one session fire at once (PostToolUse + Stop racing).
func TestDeliversOnceBySessionID(t *testing.T) {
	withHome(t)
	n := Note{ID: NewNoteID(time.Now()), From: "devbox:a", To: "laptop:b", SessionID: "sess-b", Text: "hello", Created: time.Now()}
	if err := Enqueue(n); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, _ := Deliver("sess-b", "PostToolUse", time.Now())
			mu.Lock()
			total += len(got)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("delivered %d times, want exactly 1", total)
	}
	r, err := ReceiptOf("sess-b", n.ID)
	if err != nil || r == nil || r.Event != "PostToolUse" {
		t.Fatalf("receipt = %+v, %v", r, err)
	}
}

// Catches the old folder-keyed inbox behaviour: two sessions in one worktree, a note for one reaching the other.
func TestNoteNeverReachesAnotherSessionInSameFolder(t *testing.T) {
	withHome(t)
	if err := Enqueue(Note{ID: NewNoteID(time.Now()), SessionID: "sess-a", Text: "for a", Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Deliver("sess-b", "UserPromptSubmit", time.Now()); len(got) != 0 {
		t.Fatalf("session b received %d notes meant for a", len(got))
	}
	if HasPending("sess-b") {
		t.Fatal("session b reports pending notes")
	}
	if got, _ := Deliver("sess-a", "UserPromptSubmit", time.Now()); len(got) != 1 || got[0].Text != "for a" {
		t.Fatalf("session a got %+v", got)
	}
}

// Catches typing a wake line into a busy agent's prompt (it would land mid-turn) or into a non-Orca session.
func TestNudgesOnlyIdleOrcaTerminals(t *testing.T) {
	cases := []struct {
		session Session
		want    bool
	}{
		{Session{Status: "idle", OrcaHandle: "term_1"}, true},
		{Session{Status: "busy", OrcaHandle: "term_1"}, false},
		{Session{Status: "idle"}, false},
	}
	for _, c := range cases {
		if got := ShouldNudge(c.session); got != c.want {
			t.Errorf("ShouldNudge(%+v) = %v, want %v", c.session, got, c.want)
		}
	}
	var calls [][]string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte(`{"ok":true}`), nil
	}
	if err := Nudge(context.Background(), run, Session{Status: "idle", OrcaHandle: "term_1"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "terminal send --terminal term_1") {
		t.Fatalf("nudge calls = %v", calls)
	}
}
