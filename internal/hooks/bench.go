package hooks

import "time"

// Bench times the prompt hook's own work (open the index, load the names, rank pointers) for each prompt, the span
// Prompt records as its latency, without logging a lookup: the doctor's p95 stays the record of real sessions.
func Bench(env Env, prompts []string, cwd string) (took []time.Duration, abstained int, err error) {
	for _, p := range prompts {
		started := time.Now()
		ix, err := env.openIndex()
		if err != nil || ix == nil {
			return took, abstained, err
		}
		nm, _ := env.loadNames(ix.DB())
		lk := Lookup{Abstained: true}
		if nm != nil {
			if lk, err = Pointers(ix.DB(), p, Seeds{Folder: folderName(cwd)}, nm); err != nil {
				ix.Close()
				return took, abstained, err
			}
		}
		ix.Close()
		took = append(took, time.Since(started))
		if lk.Abstained {
			abstained++
		}
	}
	return took, abstained, nil
}
