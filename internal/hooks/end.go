package hooks

import "sort"

// End runs at SessionEnd and PostCompact: each pointer the session was shown and neither opened nor followed counts
// once as ignored (after a compaction, the pointers shown before it are gone from the model's view). Live bumps
// node_stats.ignored; both modes log one lookups row with the ids.
func End(env Env, in Input) error {
	session := in.session()
	st, ok := env.loadState(session)
	if !ok {
		return nil
	}
	var ignored []string
	for id := range st.Shown {
		if !st.Counted["opened:"+id] && !st.Counted["followed:"+id] && !st.Counted["ignored:"+id] {
			ignored = append(ignored, id)
		}
	}
	if len(ignored) == 0 {
		return nil
	}
	sort.Strings(ignored)
	ix, err := env.openIndex()
	if err != nil || ix == nil {
		return err
	}
	defer ix.Close()
	tx, err := ix.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if env.Live() {
		for _, id := range ignored {
			if _, err := tx.Exec(`INSERT INTO node_stats(id, ignored) VALUES (?, 1) ON CONFLICT(id) DO UPDATE SET ignored = ignored + 1`, id); err != nil {
				return err
			}
		}
	}
	if err := logLookup(tx, env.Now(), session, "end", []string{"ignored"}, ignored, false, 0, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if st.Counted == nil {
		st.Counted = map[string]bool{}
	}
	for _, id := range ignored {
		st.Counted["ignored:"+id] = true
	}
	return env.saveState(session, st)
}
