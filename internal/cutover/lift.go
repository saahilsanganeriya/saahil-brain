package cutover

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"saahil.local/brain/internal/parse"
)

// LiftOptions lifts a legacy STATE.md over the cap: the file moves unchanged to log/ as history, and a new STATE.md
// holds a pointer to it plus the sections someone judged current, copied verbatim in their original order.
type LiftOptions struct {
	Box    string   // the box directory
	Keep   []string // headings of the sections to lift, exactly as written; none: plan only
	Note   string   // markdown placed before the lifted sections, e.g. a status written when a current section is over the cap
	Date   string   // YYYY-MM-DD in the log file's name
	Cap    int      // bytes allowed in the new STATE.md
	MinAge time.Duration
	Apply  bool
	Now    time.Time
}

type LiftPlan struct {
	State, Log string
	Bytes      int
	Sections   []parse.Section
	Lifted     string // the new STATE.md; empty without Keep
}

var ErrWithinCap = errors.New("STATE.md is within the cap")

func LiftState(o LiftOptions) (LiftPlan, error) {
	state := filepath.Join(o.Box, "STATE.md")
	name := o.Date + "-STATE-before-cutover.md"
	plan := LiftPlan{State: state, Log: filepath.Join(o.Box, "log", name)}
	info, err := os.Stat(state)
	if err != nil {
		return plan, err
	}
	if age := o.Now.Sub(info.ModTime()); age < o.MinAge {
		return plan, fmt.Errorf("%s changed %s ago: an agent may be writing it", state, age.Round(time.Second))
	}
	data, err := os.ReadFile(state)
	if err != nil {
		return plan, err
	}
	plan.Bytes = len(data)
	if len(data) <= o.Cap {
		return plan, ErrWithinCap
	}
	plan.Sections = parse.Markdown(data).Sections()
	if len(o.Keep) == 0 && o.Note == "" {
		return plan, nil
	}
	for _, k := range o.Keep {
		if !slices.ContainsFunc(plan.Sections, func(s parse.Section) bool { return s.Heading == k }) {
			return plan, fmt.Errorf("no section headed %q", k)
		}
	}

	title, _, _ := strings.Cut(string(data), "\n")
	if !strings.HasPrefix(title, "# ") {
		title = "# STATE"
	}
	var b strings.Builder
	copied := "the sections below are copied from it verbatim"
	if o.Note != "" {
		copied = "the sections below are copied from it verbatim, after a note written at the lift"
	}
	fmt.Fprintf(&b, "%s\n\n> Lifted at the cut-over on %s: the full STATE (%d KB, %d sections) moved unchanged to\n"+
		"> [log/%s](log/%s); %s. `brain sections` on it maps the rest.\n\n",
		title, o.Date, len(data)/1024, len(plan.Sections), name, name, copied)
	if o.Note != "" {
		b.WriteString(strings.TrimRight(o.Note, "\n") + "\n\n")
	}
	for _, s := range plan.Sections {
		if !slices.Contains(o.Keep, s.Heading) {
			continue
		}
		text := s.Text
		if first, rest, _ := strings.Cut(text, "\n"); first == title {
			text = strings.TrimLeft(rest, "\n")
		}
		b.WriteString(strings.TrimRight(text, "\n") + "\n\n")
	}
	plan.Lifted = strings.TrimRight(b.String(), "\n") + "\n"
	if len(plan.Lifted) > o.Cap {
		return plan, fmt.Errorf("the lifted STATE.md would be %d bytes, over the cap of %d: keep fewer sections", len(plan.Lifted), o.Cap)
	}
	if !o.Apply {
		return plan, nil
	}
	if _, err := os.Lstat(plan.Log); err == nil {
		return plan, fmt.Errorf("%s already exists", plan.Log)
	}
	if err := os.MkdirAll(filepath.Dir(plan.Log), 0o755); err != nil {
		return plan, err
	}
	tmp := state + ".lift-tmp"
	if err := os.WriteFile(tmp, []byte(plan.Lifted), info.Mode().Perm()); err != nil {
		return plan, err
	}
	if err := os.Rename(state, plan.Log); err != nil {
		return plan, errors.Join(err, os.Remove(tmp))
	}
	return plan, os.Rename(tmp, state)
}
