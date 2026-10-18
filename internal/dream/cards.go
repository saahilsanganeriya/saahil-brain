package dream

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.temporal.io/sdk/workflow"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/config"
)

// ---- curate.cards: a live box's CARD follows its work ----

const (
	cardsStepName  = "curate.cards"
	cardStaleAfter = time.Hour // a CARD this far behind its box's newest work file is refreshed
	maxCardRefresh = 4         // per dream; the rest wait for the next one
	maxNamesListed = 10        // per list in the step record
)

// staleCards picks the boxes whose CARD is at least an hour behind their newest work file, or that have work and no
// CARD, stalest first (no CARD, then the longest lag); the first maxCardRefresh are refreshed, the rest wait. A done or
// paused CARD is no exception: new work after it (a resumed campaign) makes it stale, and without new work it stays.
func staleCards(ages []backfill.CardAge) (refresh, wait []backfill.CardAge) {
	var stale []backfill.CardAge
	for _, c := range ages {
		if work := c.Box.NewestFileTime; !work.IsZero() && (c.Card.IsZero() || work.Sub(c.Card) >= cardStaleAfter) {
			stale = append(stale, c)
		}
	}
	sort.SliceStable(stale, func(i, j int) bool {
		x, y := stale[i], stale[j]
		if x.Card.IsZero() != y.Card.IsZero() {
			return x.Card.IsZero()
		}
		if lx, ly := x.Box.NewestFileTime.Sub(x.Card), y.Box.NewestFileTime.Sub(y.Card); lx != ly {
			return lx > ly
		}
		return x.Box.Name < y.Box.Name
	})
	n := min(len(stale), maxCardRefresh)
	return stale[:n], stale[n:]
}

type CardsPlanResult struct {
	Skip    string             `json:"skip,omitempty"`
	Boxes   int                `json:"boxes"`
	Refresh []backfill.CardAge `json:"refresh,omitempty"`
	Waiting []backfill.CardAge `json:"waiting,omitempty"`
}

// CardsPlan lists the stale CARDs; in shadow there are none to keep, as the backfill stages CARDs until the cut-over.
func (a *Activities) CardsPlan(_ context.Context) (CardsPlanResult, error) {
	var out CardsPlanResult
	if !a.live() {
		out.Skip = "shadow: CARDs stay the backfill's until the cut-over"
		return out, nil
	}
	_, ages, err := backfill.CardAges(a.Cfg.BrainRoot, "")
	if err != nil {
		return out, err
	}
	out.Boxes = len(ages)
	out.Refresh, out.Waiting = staleCards(ages)
	if len(out.Refresh) == 0 {
		out.Skip = "no stale cards"
	}
	return out, nil
}

// cards is the dream's curate.cards step: the plan, then one card-skill call (BackfillCard, Haiku) per stale box in
// turn under the run's cap, then ApplyEdits. The CARDs do not go to r.proposed: verify checks notes, not CARDs.
func (r *run) cards() {
	began := workflow.Now(r.ctx)
	rec := StepRecord{Name: cardsStepName}
	finish := func(status, reason string) {
		rec.Status, rec.Reason = status, reason
		r.record(rec, began)
	}
	var plan CardsPlanResult
	if err := workflow.ExecuteActivity(r.det, a.CardsPlan).Get(r.ctx, &plan); err != nil {
		finish("failed", err.Error())
		return
	}
	if plan.Skip != "" {
		finish("skipped", plan.Skip)
		return
	}
	if r.capStopped() {
		finish("skipped", r.stopLLM)
		return
	}
	var refreshed, unchanged, skipped, failed []string
	var edits []Edit
	for _, c := range plan.Refresh {
		name := c.Box.Name
		if r.capStopped() {
			skipped = append(skipped, name+" ("+r.stopLLM+")")
			continue
		}
		var out BoxStepResult
		err := workflow.ExecuteActivity(llmContext(r.ctx, config.ModelBulk), a.BackfillCard, BoxInput{RunID: r.rep.RunID,
			Box: c.Box, Live: true, BudgetUSD: RunCapUSD, Dream: true, RunSpentUSD: r.rep.CostUSD}).Get(r.ctx, &out)
		r.rep.CostUSD += out.CostUSD
		rec.CostUSD += out.CostUSD
		if out.Called {
			r.rep.LLMCalls++
			rec.Called = true
		}
		if out.Cached {
			r.rep.LLMCached++
			rec.Cached = true
		}
		if out.StopLLM {
			r.stopLLM = out.Reason
		}
		switch {
		case err != nil:
			failed = append(failed, name+" ("+err.Error()+")")
		case out.Status == "skipped":
			skipped = append(skipped, name+" ("+out.Reason+")")
		case out.Status != "ok":
			failed = append(failed, name+" ("+failureKind(out.Reason)+")")
		case len(out.Edits) == 0:
			unchanged = append(unchanged, name)
		default:
			refreshed = append(refreshed, name)
			edits = append(edits, out.Edits...)
		}
	}
	if len(edits) > 0 {
		rec.Edits = r.apply(cardsStepName, edits)
	}
	status := "ok"
	switch {
	case len(refreshed)+len(unchanged) > 0:
	case len(failed) > 0:
		status = "failed"
	default:
		status = "skipped"
	}
	finish(status, cardsSummary(refreshed, unchanged, skipped, failed, plan.Waiting))
}

// cardsSummary names the boxes only, never what a CARD says: the record reaches dream status and notifications.
func cardsSummary(refreshed, unchanged, skipped, failed []string, waiting []backfill.CardAge) string {
	var parts []string
	for _, l := range []struct {
		what  string
		names []string
	}{{"refreshed", refreshed}, {"unchanged (touched)", unchanged}, {"skipped", skipped}, {"failed", failed},
		{"wait for the next dream", boxNames(waiting)}} {
		if len(l.names) == 0 {
			continue
		}
		shown := l.names[:min(len(l.names), maxNamesListed)]
		s := fmt.Sprintf("%s %d: %s", l.what, len(l.names), strings.Join(shown, ", "))
		if len(l.names) > len(shown) {
			s += ", …"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// failureKind keeps a model error's kind, not its message, which quotes the model's answer.
func failureKind(reason string) string {
	if kind, _, ok := strings.Cut(reason, ": "); ok && strings.HasPrefix(kind, "model error ") {
		return kind
	}
	return reason
}

func boxNames(ages []backfill.CardAge) []string {
	var out []string
	for _, c := range ages {
		out = append(out, c.Box.Name)
	}
	return out
}
