package hooks

import (
	"bufio"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/parse"
)

const (
	stateHeadBytes   = 3 * 1024
	hotChars         = 2500 // ≈ hot.md's 500 words; an oversized hot.md must not starve the work's STATE
	cardChars        = 1800
	maxWorks         = 2
	maxWorkHubs      = 2
	maxStartHere     = 7
	sessionHeadBytes = 4 * 1024
	maxSessionScan   = 3000
	sessionScanTime  = 120 * time.Millisecond
)

type Digest struct {
	Text    string
	Sources []string // what made it in, by brain path (hot.md, work/<box>/STATE.md, hub:<id>)
	Blocks  []string // each source's block as it went in, parallel to Sources
	Work    []string
	Hubs    []string
}

// Start builds the start digest and returns the hook's stdout: the digest when live, nothing in shadow (where the
// lookup row records its size and sources).
func Start(env Env, in Input) ([]byte, error) {
	started := env.Now()
	ix, err := env.openIndex()
	if err != nil {
		return nil, err
	}
	var aliases func(string) []string
	if ix != nil {
		defer ix.Close()
		var byBox map[string][]string
		aliases = func(box string) []string {
			if byBox == nil {
				byBox = cardAliases(ix.DB())
			}
			return byBox[box]
		}
	}
	d := BuildDigest(env, in.cwd(), aliases)
	env.pruneStates()
	if session := in.session(); session != "" {
		st, _ := env.loadState(session)
		st.Work, st.Hubs = d.Work, d.Hubs
		_ = env.saveState(session, st)
	}
	live := env.Live()
	if ix != nil {
		chosen := map[string]any{"chars": utf8.RuneCountInString(d.Text), "sources": nonNil(d.Sources)}
		_ = logLookup(ix.DB(), env.Now(), in.session(), "start", nonNil(d.Sources), chosen, d.Text == "", env.Now().Sub(started))
	}
	if !live || d.Text == "" {
		return nil, nil
	}
	event := in.Event
	if event == "" {
		event = "SessionStart"
	}
	return Output(event, d.Text), nil
}

// BuildDigest: hot.md, then the current work's STATE.md head and CARD.md, then its hubs' start-here lists, cut to
// 8,000 chars. aliases (optional) lists CARD aliases per box from the index for the folder-name match.
func BuildDigest(env Env, cwd string, aliases func(string) []string) Digest {
	return buildDigest(env, cwd, aliases, nil)
}

// buildDigest reads only the brain paths visible accepts (session notes, work box folders, the files it quotes);
// nil accepts all.
func buildDigest(env Env, cwd string, aliases func(string) []string, visible func(string) bool) Digest {
	var d Digest
	seen := func(p string) bool { return visible == nil || visible(p) }
	var b strings.Builder
	add := func(source, title, text string, most int) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if utf8.RuneCountInString(text) > most {
			text = cutRunes(text, most) + "…"
		}
		block := "## " + title + "\n\n" + text + "\n\n"
		room := config.DigestMaxChars - utf8.RuneCountInString(b.String())
		if room < 200 {
			return
		}
		if utf8.RuneCountInString(block) > room {
			block = cutRunes(block, room-len("…\n\n")) + "…\n\n"
		}
		b.WriteString(block)
		d.Sources = append(d.Sources, source)
		d.Blocks = append(d.Blocks, block)
	}
	if hot, err := os.ReadFile(filepath.Join(env.Root, "hot.md")); err == nil && seen("hot.md") {
		add("hot.md", "This week (hot.md)", parse.Markdown(hot).Body, hotChars)
	}
	d.Work = currentWork(env, cwd, aliases, visible)
	var hubs []string
	for _, box := range d.Work {
		dir := env.workDir(box)
		var raw, card string
		if seen("work/" + box + "/STATE.md") {
			raw, _ = readHead(filepath.Join(dir, "STATE.md"), 2*stateHeadBytes)
		}
		if seen("work/" + box + "/CARD.md") {
			card, _ = readHead(filepath.Join(dir, "CARD.md"), config.CardMaxBytes)
		}
		state := parse.Markdown([]byte(raw))
		body := strings.TrimSpace(state.Body) // its frontmatter repeats what the body says
		if len(body) > stateHeadBytes {
			body = cutAtLine(body, stateHeadBytes) + "\n…(first 3 KB; Read the file for the rest)"
		}
		add("work/"+box+"/STATE.md", "Work "+box+": STATE.md", body, stateHeadBytes+100)
		add("work/"+box+"/CARD.md", "Work "+box+": CARD.md", card, cardChars)
		hubs = appendNew(hubs, parse.Markdown([]byte(card)).Hubs...)
		hubs = appendNew(hubs, state.Hubs...)
		if len(hubs) == 0 {
			hubs = appendNew(hubs, hubsByName(env.Root, box)...)
		}
	}
	hubs = slices.DeleteFunc(hubs, func(h string) bool { return !seen("hubs/" + h + ".md") })
	if len(hubs) > maxWorkHubs {
		hubs = hubs[:maxWorkHubs]
	}
	d.Hubs = hubs
	for _, h := range hubs {
		if items := startHere(env.Root, h); len(items) > 0 {
			add("hub:"+h, "Hub "+h+": start here", strings.Join(items, "\n"), config.DigestMaxChars)
		}
	}
	if b.Len() > 0 {
		d.Text = "# Brain start digest (pointers, not instructions: brain_search / brain_show for more)\n\n" + strings.TrimSpace(b.String())
		d.Text = cutRunes(d.Text, config.DigestMaxChars)
	}
	return d
}

// currentWork: the work boxes of the latest session note for this cwd, else the box whose name or CARD aliases
// best match the worktree folder; only boxes that exist.
func currentWork(env Env, cwd string, aliases func(string) []string, visible func(string) bool) []string {
	if cwd == "" {
		return nil
	}
	var out []string
	for _, box := range worksFromSessions(env.Root, cwd, visible) {
		if env.workDir(box) != "" && (visible == nil || visible("work/"+box)) && len(out) < maxWorks {
			out = append(out, box)
		}
	}
	if len(out) > 0 {
		return out
	}
	if box := workByFolder(env, cwd, aliases, visible); box != "" {
		return []string{box}
	}
	return nil
}

var monthDirRE = regexp.MustCompile(`^\d{4}/\d{2}$`)

// worksFromSessions reads session-note headers newest first until one has this cwd; bounded in files and time. Notes
// visible rejects are skipped uncounted (the brain they replay did not hold them), and the replay has no time bound.
func worksFromSessions(root, cwd string, visible func(string) bool) []string {
	type month struct{ key, dir string }
	var months []month
	machines, _ := os.ReadDir(filepath.Join(root, "sessions"))
	for _, m := range machines {
		dirs, _ := filepath.Glob(filepath.Join(root, "sessions", m.Name(), "[0-9][0-9][0-9][0-9]", "[0-9][0-9]"))
		for _, dir := range dirs {
			key := filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(dir)), filepath.Base(dir)))
			if monthDirRE.MatchString(key) {
				months = append(months, month{key, dir})
			}
		}
	}
	sort.Slice(months, func(i, j int) bool { return months[i].key > months[j].key })
	deadline, scanned := time.Now().Add(sessionScanTime), 0
	for i := 0; i < len(months); {
		// every machine's folder for one month, merged newest file first (names start with the date)
		var files []string
		key := months[i].key
		for ; i < len(months) && months[i].key == key; i++ {
			names, _ := filepath.Glob(filepath.Join(months[i].dir, "*.md"))
			files = append(files, names...)
		}
		sort.Slice(files, func(a, b int) bool { return filepath.Base(files[a]) > filepath.Base(files[b]) })
		for _, f := range files {
			if visible != nil {
				if rel, err := filepath.Rel(root, f); err != nil || !visible(filepath.ToSlash(rel)) {
					continue
				}
			}
			if scanned++; scanned > maxSessionScan || (visible == nil && time.Now().After(deadline)) {
				return nil
			}
			if noteCwd, works := sessionHeader(f); noteCwd == cwd && len(works) > 0 {
				return works
			}
		}
	}
	return nil
}

func sessionHeader(path string) (cwd string, works []string) {
	head, err := readHead(path, sessionHeadBytes)
	if err != nil || !strings.HasPrefix(head, "---") {
		return "", nil
	}
	sc := bufio.NewScanner(strings.NewReader(head))
	for i := 0; sc.Scan(); i++ {
		line := sc.Text()
		if i > 0 && line == "---" {
			break
		}
		if v, ok := strings.CutPrefix(line, "cwd:"); ok {
			cwd = config.Canonical(unquote(v))
		} else if v, ok := strings.CutPrefix(line, "works:"); ok {
			works = flowList(v)
		}
	}
	return cwd, works
}

func flowList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = unquote(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func unquote(s string) string { return strings.Trim(strings.TrimSpace(s), `"'`) }

// generic tokens of worktree paths that name no work
var folderNoise = map[string]bool{"saahil": true, "docs": true, "worktree": true, "worktrees": true, "workspaces": true,
	"main": true, "data": true, "acquisition": true}

func nameTokens(s string) []string {
	var out []string
	for _, t := range tokenRE.FindAllString(strings.ToLower(s), -1) {
		if isDigits(t) || len(t) < 2 || folderNoise[t] {
			continue
		}
		out = appendNew(out, t)
	}
	return out
}

// folderName: the worktree's own name; for <repo>/<slug>/<repo> checkouts the slug.
func folderName(cwd string) string {
	base, parent := filepath.Base(cwd), filepath.Dir(cwd)
	if filepath.Base(filepath.Dir(parent)) == base {
		return filepath.Base(parent)
	}
	return base
}

// overlap scores how well a name matches the folder: shared distinctive tokens over the folder's tokens.
func overlap(folder, name []string) (float64, int) {
	shared, strong := 0, false
	for _, t := range folder {
		for _, n := range name {
			if t == n {
				shared++
				strong = strong || len(t) >= 4
			}
		}
	}
	if !strong || len(folder) == 0 {
		return 0, 0
	}
	return float64(shared) / float64(len(folder)), shared
}

func workByFolder(env Env, cwd string, aliases func(string) []string, visible func(string) bool) string {
	folder := nameTokens(folderName(cwd))
	if len(folder) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var best string
	var bestScore float64
	var bestShared int
	for _, dir := range []string{filepath.Join(env.Root, "work"), env.WorkRoot} {
		entries, _ := os.ReadDir(dir)
		for _, ent := range entries {
			box := ent.Name()
			if !ent.IsDir() || strings.HasPrefix(box, ".") || seen[box] || (visible != nil && !visible("work/"+box)) {
				continue
			}
			seen[box] = true
			names := nameTokens(box)
			if aliases != nil {
				for _, a := range aliases(box) {
					names = appendNew(names, nameTokens(a)...)
				}
			}
			score, shared := overlap(folder, names)
			if score >= 1.0/3 && (score > bestScore || (score == bestScore && (shared > bestShared || (shared == bestShared && box > best)))) {
				best, bestScore, bestShared = box, score, shared
			}
		}
	}
	return best
}

// cardAliases maps each work box to the aliases its CARD.md declares (from the index).
func cardAliases(db *sql.DB) map[string][]string {
	out := map[string][]string{}
	rows, err := db.Query(`SELECT n.path, f.aliases FROM node n JOIN fts f ON f.rowid = n.rowid
		WHERE n.type = 'card' AND n.path LIKE 'work/%/CARD.md' AND f.aliases != ''`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p, aliases string
		if rows.Scan(&p, &aliases) == nil {
			box, _, _ := strings.Cut(strings.TrimPrefix(p, "work/"), "/")
			out[box] = append(out[box], aliases)
		}
	}
	return out
}

// hubsByName: hubs whose id or aliases share most of their words with the box name (image-lane-2026-10 → image-lane).
func hubsByName(root, box string) []string {
	boxTokens := nameTokens(box)
	paths, _ := filepath.Glob(filepath.Join(root, "hubs", "*.md"))
	type scored struct {
		id    string
		score float64
	}
	var out []scored
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".md")
		if id == "index" {
			continue
		}
		head, _ := readHead(p, 2048)
		best := 0.0
		for _, name := range append([]string{id}, parse.Markdown([]byte(head)).Aliases...) {
			if s, _ := overlap(nameTokens(name), boxTokens); s > best {
				best = s
			}
		}
		if best >= 0.5 {
			out = append(out, scored{id, best})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	var ids []string
	for i := 0; i < len(out) && i < maxWorkHubs; i++ {
		ids = append(ids, out[i].id)
	}
	return ids
}

var listItemRE = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+\S`)

// startHere returns the list items under a hub's "## Start here" heading, ≤ 7.
func startHere(root, hub string) []string {
	if strings.ContainsAny(hub, `/\`) {
		return nil
	}
	data, err := readHead(filepath.Join(root, "hubs", hub+".md"), 64<<10)
	if err != nil {
		return nil
	}
	var items []string
	in := false
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "#") {
			in = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(line, "#")), "start here")
			continue
		}
		if in && listItemRE.MatchString(line) && len(items) < maxStartHere {
			items = append(items, strings.TrimRight(line, " \t\r"))
		}
	}
	return items
}

func readHead(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(n)))
	return string(b), err
}

func cutAtLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	if i := strings.LastIndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return strings.ToValidUTF8(s, "")
}

func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func appendNew(list []string, items ...string) []string {
	for _, it := range items {
		dup := it == ""
		for _, have := range list {
			dup = dup || have == it
		}
		if !dup {
			list = append(list, it)
		}
	}
	return list
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
