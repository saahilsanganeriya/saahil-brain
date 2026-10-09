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

// Phases splits one prompt's span the way Prompt runs it.
type Phases struct {
	Open, Names, Turn time.Duration
}

func (p Phases) Total() time.Duration { return p.Open + p.Names + p.Turn }

// BenchPhases is Bench with each prompt's span split into opening the index, loading the names cache and the turn.
func BenchPhases(env Env, prompts []string, cwd string) (phases []Phases, abstained int, err error) {
	for _, p := range prompts {
		t0 := time.Now()
		ix, err := env.openIndex()
		if err != nil || ix == nil {
			return phases, abstained, err
		}
		t1 := time.Now()
		nm, _ := env.loadNames(ix.DB())
		t2 := time.Now()
		st := state{DigestOwed: true, CwdWork: folderWork(env, cwd)}
		_, lk, err := env.turn(ix.DB(), nm, p, folderName(cwd), &st, false)
		if err != nil {
			ix.Close()
			return phases, abstained, err
		}
		ix.Close()
		phases = append(phases, Phases{Open: t1.Sub(t0), Names: t2.Sub(t1), Turn: time.Since(t2)})
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
