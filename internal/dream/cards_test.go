package dream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/llm"
)

// newCardsBrain is a live brain whose boxes each have a STATE.md written 10 minutes ago and a live CARD older than it
// by the box's lag; the fake answers the card skill with a CARD naming the box.
func newCardsBrain(t *testing.T, costPerCall float64, lags map[string]time.Duration) (*Activities, *llm.Fake) {
	t.Helper()
	brain, skills := t.TempDir(), t.TempDir()
	write(t, filepath.Join(brain, ".brain", "live"), "")
	write(t, filepath.Join(skills, "card.md"), "# Skill: card\n")
	write(t, filepath.Join(brain, "hubs", "tabular-lane.md"),
		"---\nid: tabular-lane\ntype: hub\nstatus: current\nsummary: the tabular lane\nbroader: [index]\n---\n")
	worked := time.Now().Add(-10 * time.Minute)
	for box, lag := range lags {
		for rel, at := range map[string]time.Time{"STATE.md": worked, "CARD.md": worked.Add(-lag)} {
			p := filepath.Join(brain, "work", box, rel)
			write(t, p, "---\nid: card-"+box+"\ntype: card\nstatus: live\nsummary: old card\n---\nold\n")
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	fake := &llm.Fake{CostUSD: costPerCall, Respond: func(c llm.Call) llm.Result {
		var in struct {
			Inventory struct{ Box string } `json:"inventory"`
		}
		_ = json.Unmarshal([]byte(c.Input), &in)
		out, _ := json.Marshal(map[string]any{"card": map[string]any{"status": "live",
			"summary": "synthetic box " + in.Inventory.Box, "hubs": []string{"tabular-lane"}},
			"body": "## What it is\nA box.\n\n## Key files\n- `STATE.md` — state\n"})
		return llm.Result{JSON: out}
	}}
	ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ledger.Close() })
	cfg := Config{BrainRoot: brain, SkillsDir: skills, BrainBin: filepath.Join(brain, "no-brain-cli")}
	return &Activities{Cfg: cfg, LLM: fake, Ledger: ledger}, fake
}

// TestStaleCardsSelection catches the selection going wrong: a CARD under an hour behind its work refreshed (a busy
// box would pay every dream), a done CARD never refreshed after its campaign resumed (or refreshed with no new work),
// an empty box with no CARD taking a slot every dream, more than 4 boxes per dream, or the stalest not first.
func TestStaleCardsSelection(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	age := func(name string, lag time.Duration, status string) backfill.CardAge {
		c := backfill.CardAge{Box: backfill.Box{Name: name, NewestFileTime: t0}, Card: t0.Add(-lag), Status: status}
		if status == "" {
			c.Card = time.Time{}
		}
		return c
	}
	empty := age("empty", 0, "")
	empty.Box.NewestFileTime = time.Time{}
	refresh, wait := staleCards([]backfill.CardAge{
		age("fresh", 59*time.Minute, "live"), age("done-quiet", -time.Hour, "done"), age("resumed", 2*time.Hour, "done"),
		age("busy", time.Hour, "live"), age("old", 30*24*time.Hour, "paused"), age("new-box", 0, ""), empty,
		age("other", 3*time.Hour, "live"),
	})
	if got := boxNames(refresh); !slices.Equal(got, []string{"new-box", "old", "other", "resumed"}) {
		t.Errorf("refresh %v", got)
	}
	if got := boxNames(wait); !slices.Equal(got, []string{"busy"}) {
		t.Errorf("wait %v", got)
	}
}

// TestDreamKeepsCardsCurrent catches the dream leaving a stale CARD (the image-lane CARD said "nothing merged" two
// hours after its STATE recorded 10 merged PRs), rewriting a current one, paying outside the run's cost, letting its
// own CARD write wake the next dream, or calling the model when no CARD is stale.
func TestDreamKeepsCardsCurrent(t *testing.T) {
	acts, fake := newCardsBrain(t, 0.05, map[string]time.Duration{"busy": 2 * time.Hour, "quiet": 20 * time.Minute})
	quietCard, _ := os.ReadFile(filepath.Join(acts.Cfg.BrainRoot, "work", "quiet", "CARD.md"))
	t0 := time.Now().UTC()

	first := runDream(t, acts, Input{}, t0)
	s := step(first, cardsStepName)
	if s.Status != "ok" || s.Edits != 1 || !strings.Contains(s.Reason, "refreshed 1: busy") || fake.Calls() != 1 ||
		first.CostUSD != 0.05 || !slices.Contains(first.EditedPaths, "work/busy/CARD.md") {
		t.Fatalf("calls=%d cost=$%.2f edited=%v step=%+v", fake.Calls(), first.CostUSD, first.EditedPaths, s)
	}
	busy, _ := os.ReadFile(filepath.Join(acts.Cfg.BrainRoot, "work", "busy", "CARD.md"))
	quiet, _ := os.ReadFile(filepath.Join(acts.Cfg.BrainRoot, "work", "quiet", "CARD.md"))
	if !strings.Contains(string(busy), "synthetic box busy") || string(quiet) != string(quietCard) {
		t.Fatalf("busy:\n%s\nquiet:\n%s", busy, quiet)
	}

	if second := runDream(t, acts, Input{}, t0.Add(13*time.Hour)); second.Status != "skipped" ||
		!strings.Contains(second.Skipped, "nothing new") {
		t.Fatalf("the dream's own CARD write woke the next dream: %s %q", second.Status, second.Skipped)
	}

	write(t, filepath.Join(acts.Cfg.BrainRoot, "work", "busy", "plans", "PLAN.md"), "next step\n")
	third := runDream(t, acts, Input{}, t0.Add(26*time.Hour))
	if s := step(third, cardsStepName); s.Status != "skipped" || s.Reason != "no stale cards" || fake.Calls() != 1 {
		t.Fatalf("calls=%d step=%+v", fake.Calls(), s)
	}
}

// TestDreamCardsStopAtRunCap catches CARD refreshes paid outside the dream's $8: once the run's spend plus the next
// call's budget would pass it, the step stops, names the boxes left and why, and later LLM steps stop too.
func TestDreamCardsStopAtRunCap(t *testing.T) {
	acts, fake := newCardsBrain(t, 2.6, map[string]time.Duration{"a": 5 * time.Hour, "b": 4 * time.Hour,
		"c": 3 * time.Hour, "d": 2 * time.Hour})
	rep := runDream(t, acts, Input{}, time.Now().UTC())
	s := step(rep, cardsStepName)
	if fake.Calls() != 3 || rep.CostUSD > RunCapUSD || s.Status != "ok" ||
		!strings.Contains(s.Reason, "refreshed 3: a, b, c") || !strings.Contains(s.Reason, "skipped 1: d (cost cap") {
		t.Fatalf("calls=%d cost=$%.2f step=%+v", fake.Calls(), rep.CostUSD, s)
	}
}

// TestUnchangedCardAnswerTouchesCard catches a refresh whose answer equals the CARD on disk (a change deep in the box
// that the inventory does not show) leaving the box stale, so that every later dream picks it again.
func TestUnchangedCardAnswerTouchesCard(t *testing.T) {
	acts, _ := newCardsBrain(t, 0.05, map[string]time.Duration{"busy": 2 * time.Hour})
	_, ages, err := backfill.CardAges(acts.Cfg.BrainRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	in := BoxInput{RunID: "r", Box: ages[0].Box, Live: true, BudgetUSD: RunCapUSD, Dream: true}
	first, err := acts.BackfillCard(t.Context(), in)
	if err != nil || len(first.Edits) != 1 {
		t.Fatalf("first %+v err %v", first, err)
	}
	card := filepath.Join(acts.Cfg.BrainRoot, first.Edits[0].Path)
	write(t, card, first.Edits[0].Content)
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(card, old, old); err != nil {
		t.Fatal(err)
	}
	second, err := acts.BackfillCard(t.Context(), in)
	if err != nil || len(second.Edits) != 0 || !second.Cached {
		t.Fatalf("second %+v err %v", second, err)
	}
	_, ages, _ = backfill.CardAges(acts.Cfg.BrainRoot, "")
	if refresh, _ := staleCards(ages); len(refresh) != 0 {
		t.Fatalf("still stale: CARD %s, newest %s", ages[0].Card, ages[0].Box.NewestFileTime)
	}
}
