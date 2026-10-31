package dream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"go.temporal.io/sdk/workflow"
)

// A held edit of a step that judges only its run's proposals (link, hubs, verify) waits for a later dream instead of
// being lost: the run saves it with the file as it was when the cap held it, and the next dream's held step lands it
// in what its cap leaves, only while the file is byte for byte that file and for at most carryMaxAge. A carried edit
// that cannot land any more is recorded lost in the run that drops it.

const (
	carryMaxAge = 24 * time.Hour
	carryMax    = 2 * MaxEdits // newest kept; an older one beyond it is dropped as lost
)

// CarriedEdit is one held edit waiting in .brain/held-carry.json.
type CarriedEdit struct {
	Run  string    `json:"run"`
	Held time.Time `json:"held"`
	Step string    `json:"step"`
	Edit Edit      `json:"edit"`
	Base string    `json:"base"`
}

type CarryLoad struct {
	Valid   []CarriedEdit
	Dropped []Rejection // lost: their file changed since, or they waited too long
}

func (a *Activities) carryPath() string { return a.path(".brain", "held-carry.json") }

// fileBase names a file's content when an edit was held; "absent" when it did not exist.
func fileBase(content []byte, exists bool) string {
	if !exists {
		return "absent"
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// LoadCarried reads the carried edits and keeps those whose file is still the one they were made against.
func (a *Activities) LoadCarried(_ context.Context, now time.Time) (CarryLoad, error) {
	var out CarryLoad
	b, err := os.ReadFile(a.carryPath())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var carried []CarriedEdit
	if err := json.Unmarshal(b, &carried); err != nil {
		return out, fmt.Errorf("%s: %w", a.carryPath(), err)
	}
	for _, c := range carried {
		drop := func(why string) {
			out.Dropped = append(out.Dropped, Rejection{Step: c.Step, Path: c.Edit.Path, Action: c.Edit.Action, Held: true,
				Lost: true, Reason: fmt.Sprintf("carried from %s: %s; lost", c.Run, why)})
		}
		abs, err := a.writablePath(c.Edit.Path)
		if err != nil {
			drop(err.Error())
			continue
		}
		cur, err := os.ReadFile(abs)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
		switch {
		case exists && bytes.Equal(cur, []byte(c.Edit.Content)):
			// landed already (a run that landed it stopped before it saved the carry list)
		case now.Sub(c.Held) > carryMaxAge:
			drop(fmt.Sprintf("waited more than %s", carryMaxAge))
		case fileBase(cur, exists) != c.Base:
			drop("the file changed since")
		default:
			out.Valid = append(out.Valid, c)
		}
	}
	return out, nil
}

// SaveCarried replaces the carry list; an empty list removes the file.
func (a *Activities) SaveCarried(_ context.Context, carried []CarriedEdit) error {
	if len(carried) == 0 {
		if err := os.Remove(a.carryPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(carried, "", " ")
	if err != nil {
		return err
	}
	return writeFile(a.carryPath(), append(b, '\n'))
}

func (r *run) carries() bool {
	return workflow.GetVersion(r.ctx, "carry-held", workflow.DefaultVersion, 1) == 1
}

// loadCarried returns the earlier dreams' held edits that can still land, as held-step candidates, and records the
// ones it drops as lost.
func (r *run) loadCarried() []heldEdit {
	var load CarryLoad
	if err := workflow.ExecuteActivity(r.det, a.LoadCarried, workflow.Now(r.ctx).UTC()).Get(r.ctx, &load); err != nil {
		r.rep.Rejected = append(r.rep.Rejected, Rejection{Step: heldStepName, Reason: "carried edits unread: " + err.Error()})
		return nil
	}
	r.rep.Rejected = append(r.rep.Rejected, load.Dropped...)
	var out []heldEdit
	for _, c := range load.Valid {
		out = append(out, heldEdit{Step: c.Step, Edit: c.Edit, After: len(r.applied), Base: c.Base, From: c.Run, Held: c.Held})
	}
	return out
}

// saveCarried keeps for the next dream every held edit this run did not land and that is still landable: its own
// (link, hubs, verify) and the carried ones that did not fit again. Its own are no longer recorded lost.
func (r *run) saveCarried() {
	if !r.carries() {
		return
	}
	now := workflow.Now(r.ctx).UTC()
	var keep []CarriedEdit
	if r.rep.Reverted > 0 { // check put every file back, so the carried edits that landed here still fit their base
		for _, h := range r.landedLate {
			if h.From != "" {
				keep = append(keep, CarriedEdit{Run: h.From, Held: h.Held, Step: h.Step, Edit: h.Edit, Base: h.Base})
			}
		}
	}
	waiting := slices.DeleteFunc(slices.Clone(r.held), func(h heldEdit) bool {
		return slices.ContainsFunc(r.landedLate, func(l heldEdit) bool { return l.Step == h.Step && l.Edit.Path == h.Edit.Path })
	})
	still := landable(waiting, r.applied)
	for _, h := range waiting {
		replaced := slices.ContainsFunc(waiting, func(o heldEdit) bool { return o.From == "" && o.Edit.Path == h.Edit.Path })
		if h.From != "" && !replaced && !slices.Contains(still, h) {
			r.rep.Rejected = append(r.rep.Rejected, Rejection{Step: h.Step, Path: h.Edit.Path, Held: true, Lost: true,
				Reason: fmt.Sprintf("carried from %s: the file changed since; lost", h.From)})
		}
	}
	carriedOwn := map[string]bool{}
	for _, h := range still {
		if h.From == "" {
			keep = append(keep, CarriedEdit{Run: r.rep.RunID, Held: now, Step: h.Step, Edit: h.Edit, Base: h.Base})
			carriedOwn[h.Step+"\x00"+h.Edit.Path] = true
		} else {
			keep = append(keep, CarriedEdit{Run: h.From, Held: h.Held, Step: h.Step, Edit: h.Edit, Base: h.Base})
		}
	}
	if len(keep) > carryMax {
		for _, c := range keep[:len(keep)-carryMax] {
			r.rep.Rejected = append(r.rep.Rejected, Rejection{Step: c.Step, Path: c.Edit.Path, Held: true, Lost: true,
				Reason: fmt.Sprintf("carried from %s: the carry list holds %d; lost", c.Run, carryMax)})
			delete(carriedOwn, c.Step+"\x00"+c.Edit.Path)
		}
		keep = keep[len(keep)-carryMax:]
	}
	if err := workflow.ExecuteActivity(r.det, a.SaveCarried, keep).Get(r.ctx, nil); err != nil {
		r.rep.Rejected = append(r.rep.Rejected, Rejection{Step: heldStepName, Reason: "carried edits unsaved: " + err.Error()})
		return
	}
	for i := len(r.rep.Rejected) - 1; i >= 0; i-- { // the latest record of a file is the edit carried
		rej := &r.rep.Rejected[i]
		if key := rej.Step + "\x00" + rej.Path; rej.Lost && carriedOwn[key] {
			rej.Lost, rej.Reason = false, strings.Replace(rej.Reason, "; lost: ", "; carried to the next dream: ", 1)
			delete(carriedOwn, key)
		}
	}
}
