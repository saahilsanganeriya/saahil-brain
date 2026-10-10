package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"saahil.local/brain/internal/rpc"
)

// wakingPeer is a laptop that does not answer its first asleepFor calls.
type wakingPeer struct {
	asleepFor int
	calls     []string
}

func (p *wakingPeer) Name() string { return "laptop" }

func (p *wakingPeer) Call(_ context.Context, req rpc.Request) (rpc.Response, error) {
	if p.asleepFor > 0 {
		p.asleepFor--
		p.calls = append(p.calls, req.Op+" (asleep)")
		return rpc.Response{}, fmt.Errorf("%w: tunnel down", rpc.ErrUnreachable)
	}
	p.calls = append(p.calls, req.Op)
	if req.Op == "step" {
		return rpc.Response{OK: true, Result: json.RawMessage(`{"id":"j-1","step":"` + req.Step + `","state":"running"}`)}, nil
	}
	return rpc.Response{OK: true, Result: json.RawMessage(`{}`)}, nil
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time        { return c.t }
func (c *fakeClock) sleep(d time.Duration) { c.t = c.t.Add(d) }

// TestPeerStepWaitsForASleepingPeer catches the 2026-10-10 care run's lost laptop half: `brain peer step doctor
// --wait 10m` failed after one unanswered request, because --wait covered only the job's run, not reaching the laptop.
// The step is sent once, after the laptop answers.
func TestPeerStepWaitsForASleepingPeer(t *testing.T) {
	peer := &wakingPeer{asleepFor: 3}
	clock := &fakeClock{t: time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC)}
	job, err := startStep(context.Background(), peer, "doctor", clock.t.Add(10*time.Minute), clock.now, clock.sleep)
	if err != nil || job.ID != "j-1" {
		t.Fatalf("startStep = %+v, %v; want job j-1 once the laptop answers", job, err)
	}
	want := []string{"status (asleep)", "status (asleep)", "status (asleep)", "status", "step"}
	if !slices.Equal(peer.calls, want) {
		t.Errorf("calls %v, want %v: the step goes once, to a laptop that answered", peer.calls, want)
	}
}

// TestPeerStepGivesUpAtTheDeadline catches a wait that never ends or starts the step on a laptop that never answered.
func TestPeerStepGivesUpAtTheDeadline(t *testing.T) {
	peer := &wakingPeer{asleepFor: 1000}
	clock := &fakeClock{t: time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC)}
	_, err := startStep(context.Background(), peer, "doctor", clock.t.Add(time.Minute), clock.now, clock.sleep)
	if !errors.Is(err, rpc.ErrUnreachable) {
		t.Fatalf("err %v, want peer unreachable", err)
	}
	if slices.Contains(peer.calls, "step (asleep)") || len(peer.calls) > 1+int(time.Minute/peerRetry) {
		t.Errorf("calls %v: only status probes, one per %s, until the deadline", peer.calls, peerRetry)
	}
}
