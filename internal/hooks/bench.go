package hooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Bench times the prompt hook's own work (open the index, load the names, rank pointers) for each prompt as a
// session's first, which also picks its work and builds the digest's work part: the span Prompt records as its
// latency, without logging a lookup, so the doctor's p95 stays the record of real sessions.
func Bench(env Env, prompts []string, cwd string) (took []time.Duration, abstained int, err error) {
	phases, abstained, err := BenchPhases(env, prompts, cwd)
	for _, p := range phases {
		took = append(took, p.Total())
	}
	return took, abstained, err
}

// Phases splits one prompt's span the way Prompt runs it; Prompt logs it in lookups.phases.
type Phases struct {
	Open, Names, Turn      time.Duration
	Read, Digest, Pointers time.Duration // the turn's parts
	StaleNames             bool          // the names cache was older than the index (or missing)
}

func (p Phases) Total() time.Duration { return p.Open + p.Names + p.Turn }

// marker is an Env.Mark that stores each part of the turn as it ends, timed from start.
func (p *Phases) marker(now func() time.Time, start time.Time) func(string) {
	last := start
	return func(phase string) {
		t := now()
		switch phase {
		case "read":
			p.Read = t.Sub(last)
		case "digest":
			p.Digest = t.Sub(last)
		case "pointers":
			p.Pointers = t.Sub(last)
		}
		last = t
	}
}

// logged is the lookups.phases value: ms per part.
func (p Phases) logged() string {
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	b, _ := json.Marshal(struct {
		Open       float64 `json:"open"`
		Names      float64 `json:"names"`
		Read       float64 `json:"read"`
		Digest     float64 `json:"digest"`
		Pointers   float64 `json:"pointers"`
		StaleNames bool    `json:"stale_names,omitempty"`
	}{ms(p.Open), ms(p.Names), ms(p.Read), ms(p.Digest), ms(p.Pointers), p.StaleNames})
	return string(b)
}

// BenchPhases is Bench with each prompt's span split into opening the index, loading the names cache and the turn.
func BenchPhases(env Env, prompts []string, cwd string) (phases []Phases, abstained int, err error) {
	for _, p := range prompts {
		t0 := time.Now()
		ix, err := env.openIndex()
		if err != nil || ix == nil {
			return phases, abstained, err
		}
		t1 := time.Now()
		nm, stale := env.loadNames(ix.DB())
		t2 := time.Now()
		st := state{DigestOwed: true, CwdWork: folderWork(env, cwd)}
		ph := Phases{StaleNames: stale}
		env.Mark = ph.marker(time.Now, t2)
		_, lk, err := env.turn(ix.DB(), nm, p, folderName(cwd), &st, false)
		if err != nil {
			ix.Close()
			return phases, abstained, err
		}
		ix.Close()
		ph.Open, ph.Names, ph.Turn = t1.Sub(t0), t2.Sub(t1), time.Since(t2)
		phases = append(phases, ph)
		if lk.Abstained {
			abstained++
		}
	}
	return phases, abstained, nil
}

// BenchQuestions picks n questions spread over the QA eval set (.brain/evals/sets/qa.jsonl).
func BenchQuestions(root string, n int) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, ".brain", "evals", "sets", "qa.jsonl"))
	if err != nil {
		return nil, err
	}
	var questions []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var item struct{ Question string }
		if json.Unmarshal([]byte(line), &item) == nil && item.Question != "" {
			questions = append(questions, item.Question)
		}
	}
	if len(questions) == 0 || n <= 0 {
		return nil, errors.New("no questions in the qa set")
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, questions[i*len(questions)/n])
	}
	return out, nil
}

// Quantile reads a sorted copy of took at q (0.5, 0.95).
func Quantile(took []time.Duration, q float64) time.Duration {
	s := slices.Clone(took)
	slices.Sort(s)
	return s[min(len(s)-1, int(q*float64(len(s))))]
}
