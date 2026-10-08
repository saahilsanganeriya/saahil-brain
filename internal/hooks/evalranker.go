package hooks

import (
	"database/sql"

	"saahil.local/brain/internal/rank"
)

// EvalRanker is the prompt hook's ranking for offline evals: the same name dictionary, the same seeds a session
// without a start record gets (the work matched from its folder), live mode, nothing injected or recorded.
type EvalRanker struct {
	env   Env
	db    *sql.DB
	names *Names
}

func NewEvalRanker(env Env, db *sql.DB) (*EvalRanker, error) {
	nm, err := BuildNames(db, func(p string) []byte {
		b, _ := readHead(env.absPath(p), 8<<10)
		return []byte(b)
	})
	if err != nil {
		return nil, err
	}
	return &EvalRanker{env: env, db: db, names: nm}, nil
}

func (r *EvalRanker) Pointers(prompt, cwd string, hide func(id string) bool) []string {
	lk, err := r.Lookup(prompt, cwd, hide)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(lk.Chosen))
	for _, p := range lk.Chosen {
		ids = append(ids, p.ID)
	}
	return ids
}

// Lookup is the whole ranking for a prompt (candidates, best score, abstention), for diagnosing an eval. hide drops
// what a pre-session index would not hold, the session's work box and hubs included.
func (r *EvalRanker) Lookup(prompt, cwd string, hide func(id string) bool) (Lookup, error) {
	seeds := Seeds{Folder: folderName(cwd), Hide: hide}
	if box := workByFolder(r.env, cwd, nil); box != "" && (hide == nil || !hide("work/"+box)) {
		seeds.Work, seeds.Hubs = []string{box}, rank.Visible(hubsByName(r.env.Root, box), hide)
	}
	return Pointers(r.db, prompt, seeds, r.names)
}
