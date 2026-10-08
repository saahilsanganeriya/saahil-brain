package hooks

import (
	"encoding/json"
	"strings"

	"saahil.local/brain/internal/config"
)

// Post runs PostToolUse: when a Read/Grep/Glob (opened) or Edit/Write (followed) reaches a node this session was
// pointed at, count it once per session. Live bumps node_stats; shadow logs a lookups row instead, since nothing
// was really shown. It reads one small state file and touches the index only on a hit.
func Post(env Env, in Input) error {
	field := ""
	switch in.ToolName {
	case "Read", "Grep", "Glob", "NotebookRead":
		field = "opened"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		field = "followed"
	default:
		return nil
	}
	session := in.session()
	st, ok := env.loadState(session)
	if !ok || len(st.Shown) == 0 {
		return nil
	}
	var ti struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
	}
	if json.Unmarshal(in.ToolInput, &ti) != nil {
		return nil
	}
	target, isFile := ti.FilePath, true
	if target == "" {
		target = ti.NotebookPath
	}
	if target == "" {
		target, isFile = ti.Path, false
	}
	if target == "" {
		return nil
	}
	target = config.Canonical(target)
	var hits []string
	for id, abs := range st.Shown {
		if (abs == target || (!isFile && strings.HasPrefix(abs, target+"/"))) && !st.Counted[field+":"+id] {
			hits = append(hits, id)
		}
	}
	if len(hits) == 0 {
		return nil
	}
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
	live := env.Live()
	for _, id := range hits {
		if live {
			_, err = tx.Exec(`INSERT INTO node_stats(id, `+field+`) VALUES (?, 1) ON CONFLICT(id) DO UPDATE SET `+field+` = `+field+` + 1`, id)
		} else {
			err = logLookup(tx, env.Now(), session, "post", []string{field}, []string{id}, false, 0)
		}
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if st.Counted == nil {
		st.Counted = map[string]bool{}
	}
	for _, id := range hits {
		st.Counted[field+":"+id] = true
	}
	return env.saveState(session, st)
}
