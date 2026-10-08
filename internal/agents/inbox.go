package agents

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
)

// Note is one message from an agent (or the owner) to one session.
type Note struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	SessionID string    `json:"sessionId"`
	Text      string    `json:"text"`
	Created   time.Time `json:"created"`
}

// Receipt records when and through which hook a note reached its session.
type Receipt struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"sessionId"`
	DeliveredAt time.Time `json:"deliveredAt"`
	Event       string    `json:"event"`
}

func NewNoteID(now time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "n-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

// Enqueue writes the note into its session's inbox atomically (temp file + rename in the same directory), so the
// delivery hook never reads a half-written note.
func Enqueue(n Note) error {
	if n.SessionID == "" || n.ID == "" || strings.ContainsAny(n.SessionID+n.ID, "/\\") {
		return errors.New("note needs a session id and an id without slashes")
	}
	dir := config.InboxDir(n.SessionID)
	if err := os.MkdirAll(filepath.Join(dir, "delivered"), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, n.ID+".json"))
}

// Deliver claims every pending note of one session. The claim is a rename into delivered/, which only one caller can
// win, so two hooks firing at once never deliver a note twice.
func Deliver(sessionID, event string, now time.Time) ([]Note, error) {
	dir := config.InboxDir(sessionID)
	pending, err := filepath.Glob(filepath.Join(dir, "n-*.json"))
	if err != nil || len(pending) == 0 {
		return nil, err
	}
	sort.Strings(pending)
	var notes []Note
	for _, path := range pending {
		claimed := filepath.Join(dir, "delivered", filepath.Base(path))
		if err := os.Rename(path, claimed); err != nil {
			continue
		}
		raw, err := os.ReadFile(claimed)
		if err != nil {
			continue
		}
		var n Note
		if json.Unmarshal(raw, &n) != nil {
			continue
		}
		notes = append(notes, n)
		receipt, _ := json.Marshal(Receipt{ID: n.ID, SessionID: sessionID, DeliveredAt: now.UTC(), Event: event})
		_ = os.WriteFile(strings.TrimSuffix(claimed, ".json")+".receipt", receipt, 0o600)
	}
	return notes, nil
}

// HasPending is the hook's fast path: one directory read, no parsing.
func HasPending(sessionID string) bool {
	entries, err := os.ReadDir(config.InboxDir(sessionID))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "n-") && strings.HasSuffix(e.Name(), ".json") {
			return true
		}
	}
	return false
}

// ReceiptOf returns the delivery receipt, or nil while the note still waits.
func ReceiptOf(sessionID, id string) (*Receipt, error) {
	raw, err := os.ReadFile(filepath.Join(config.InboxDir(sessionID), "delivered", id+".receipt"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Render is what the session sees: who sent it and how to answer, then the text.
func Render(notes []Note) string {
	var b strings.Builder
	for i, n := range notes {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "BRAIN NOTE %s from %s (sent %s). Begin your next reply with one visible line "+
			"'NOTE RECEIVED from %s: <gist>'. Answer with: brain agents note %s \"<reply>\"\n\n%s",
			n.ID, n.From, n.Created.UTC().Format(time.RFC3339), n.From, n.From, n.Text)
	}
	return b.String()
}
