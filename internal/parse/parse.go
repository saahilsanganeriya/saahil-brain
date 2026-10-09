// Package parse reads markdown, html and data files into what the index stores: title, summary, frontmatter
// fields, authored relations, headings with line numbers, links and mentions.
package parse

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"html"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"saahil.local/brain/internal/config"
)

const SummaryMax = 160

// AuthoredRels are the only relations a note may write (FORMAT.md); everything else is derived.
var AuthoredRels = []string{"hubs", "about", "evidence", "supersedes", "sources", "related", "broader"}

type Rel struct{ Rel, Dst, Why string }

type Link struct {
	Target string // as written, fragment and query stripped
	Wiki   bool   // [[target]]
}

type Heading struct {
	Level int
	Text  string
	Line  int // 1-based, counting frontmatter
}

type Doc struct {
	Type, Status, Title, Summary, Scope, AsOf, Updated string
	Hubs, Aliases                                      []string
	Authored                                           []Rel
	Headings                                           []Heading
	Links                                              []Link
	Mentions                                           []string // pr:owner/repo#n, ticket:KEY-n, code:path@sha
	Body                                               string   // text without frontmatter
	lines                                              []string
	bodyLine                                           int // first line after frontmatter, 1-based
}

type Section struct {
	Heading            string
	LineStart, LineEnd int
	Bytes              int
	Summary, Text      string
}

// Markdown parses a document; Title falls back to the caller's file name when empty.
func Markdown(data []byte) Doc {
	text := string(data)
	d := Doc{lines: strings.SplitAfter(text, "\n")}
	if n := len(d.lines); n > 0 && d.lines[n-1] == "" {
		d.lines = d.lines[:n-1]
	}
	body := 0
	fm := map[string]any{}
	if len(d.lines) > 0 && strings.TrimSpace(d.lines[0]) == "---" {
		for i := 1; i < len(d.lines); i++ {
			if strings.TrimSpace(d.lines[i]) == "---" {
				fm = frontmatter(d.lines[1:i])
				body = i + 1
				break
			}
		}
	}
	d.applyFrontmatter(fm)
	d.bodyLine = body + 1
	d.Body = strings.Join(d.lines[body:], "")
	fenced, para, paraDone := false, []string{}, false
	for i := body; i < len(d.lines); i++ {
		line := strings.TrimRight(d.lines[i], "\r\n")
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if lvl, txt := atxHeading(line); lvl > 0 {
			d.Headings = append(d.Headings, Heading{Level: lvl, Text: txt, Line: i + 1})
			if d.Title == "" && lvl == 1 {
				d.Title = txt
			}
			paraDone = paraDone || len(para) > 0
			continue
		}
		if !paraDone {
			if trim == "" {
				paraDone = len(para) > 0
			} else {
				para = append(para, trim)
			}
		}
	}
	if d.Summary == "" {
		d.Summary = clip(strings.Join(para, " "), SummaryMax)
	}
	d.Links = links(d.Body)
	d.Mentions = mentions(d.Body)
	return d
}

func atxHeading(line string) (int, string) {
	lvl := 0
	for lvl < len(line) && lvl < 7 && line[lvl] == '#' {
		lvl++
	}
	if lvl == 0 || lvl > 6 || (len(line) > lvl && line[lvl] != ' ' && line[lvl] != '\t') {
		return 0, ""
	}
	return lvl, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[lvl:]), "#"))
}

// Sections splits at H1/H2 (H3 when the doc has fewer than two of those); text before the first is a preamble.
func (d Doc) Sections() []Section {
	maxLvl := 2
	if count(d.Headings, 2) < 2 {
		maxLvl = 3
	}
	var starts []Heading
	for _, h := range d.Headings {
		if h.Level <= maxLvl {
			starts = append(starts, h)
		}
	}
	if len(starts) == 0 {
		return nil
	}
	if first := starts[0].Line; first > d.bodyLine && strings.TrimSpace(strings.Join(d.lines[d.bodyLine-1:first-1], "")) != "" {
		starts = append([]Heading{{Text: "(preamble)", Line: d.bodyLine}}, starts...)
	}
	out := make([]Section, len(starts))
	for i, h := range starts {
		end := len(d.lines)
		if i+1 < len(starts) {
			end = starts[i+1].Line - 1
		}
		text := strings.Join(d.lines[h.Line-1:end], "")
		out[i] = Section{Heading: h.Text, LineStart: h.Line, LineEnd: end, Bytes: len(text), Text: text,
			Summary: firstParagraph(d.lines[h.Line:end])}
	}
	return out
}

func count(hs []Heading, maxLvl int) int {
	n := 0
	for _, h := range hs {
		if h.Level <= maxLvl {
			n++
		}
	}
	return n
}

func firstParagraph(lines []string) string {
	var para []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if lvl, _ := atxHeading(t); lvl > 0 || strings.HasPrefix(t, "```") {
			if len(para) > 0 {
				break
			}
			continue
		}
		if t == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, t)
	}
	return clip(strings.Join(para, " "), SummaryMax)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ---- frontmatter: the YAML subset FORMAT.md uses ----

// frontmatter reads `key: scalar`, `key: [a, {id: x, why: y}]`, block lists and one level of nested maps
// (flattened as parent.child).
// frontmatter reads YAML with yaml.v3 (flow lists may wrap over lines, e.g. related: [{id: …, why: '…'},\n  {…}]);
// a block yaml.v3 rejects (hand-written legacy headers) falls back to the line reader.
func frontmatter(lines []string) map[string]any {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines, "\n")), &doc); err == nil && doc != nil {
		out := map[string]any{}
		for k, v := range doc {
			flattenYAML(out, k, v)
		}
		return out
	}
	return frontmatterLines(lines)
}

// flattenYAML turns yaml.v3 values into the shapes the line reader produced: scalars as strings, lists as []any with
// maps as map[string]string, nested maps as "parent.key".
func flattenYAML(out map[string]any, key string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			flattenYAML(out, key+"."+k, x)
		}
	case []any:
		items := make([]any, 0, len(t))
		for _, x := range t {
			if m, ok := x.(map[string]any); ok {
				sm := map[string]string{}
				for k, y := range m {
					sm[k] = yamlScalar(y)
				}
				items = append(items, sm)
			} else {
				items = append(items, yamlScalar(x))
			}
		}
		out[key] = items
	default:
		out[key] = yamlScalar(v)
	}
}

func yamlScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case time.Time:
		if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
			return t.Format("2006-01-02")
		}
		return t.Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(t)
	}
}

func frontmatterLines(lines []string) map[string]any {
	out := map[string]any{}
	parent := ""
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		trim := strings.TrimSpace(line)
		if indented && parent != "" {
			if item, ok := strings.CutPrefix(trim, "- "); ok {
				list, _ := out[parent].([]string)
				out[parent] = append(list, scalar(item))
			} else if k, v, ok := strings.Cut(trim, ":"); ok {
				out[parent+"."+strings.TrimSpace(k)] = value(v)
			}
			continue
		}
		k, v, ok := strings.Cut(trim, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		parent = k
		out[k] = value(v)
	}
	return out
}

func value(v string) any {
	v = stripComment(strings.TrimSpace(v))
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		var items []any
		for _, part := range splitTop(v[1 : len(v)-1]) {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				m := map[string]string{}
				for _, kv := range splitTop(part[1 : len(part)-1]) {
					if k, val, ok := strings.Cut(kv, ":"); ok {
						m[strings.TrimSpace(k)] = scalar(val)
					}
				}
				items = append(items, m)
			} else if part != "" {
				items = append(items, scalar(part))
			}
		}
		return items
	}
	return scalar(v)
}

func stripComment(v string) string {
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, `'`) {
		return v
	}
	if i := strings.Index(v, " #"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}

func scalar(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// splitTop splits on commas outside braces, brackets and quotes.
func splitTop(s string) []string {
	var out []string
	depth, quote, start := 0, byte(0), 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

func strs(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []string:
		return t
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (d *Doc) applyFrontmatter(fm map[string]any) {
	str := func(k string) string { s, _ := fm[k].(string); return s }
	d.Type, d.Status, d.Scope = str("type"), str("status"), str("scope")
	d.Title, d.AsOf, d.Updated = str("title"), str("as_of"), str("updated")
	if d.Type == "" {
		d.Type = str("metadata.type") // Claude memory files
	}
	d.Summary = clip(str("summary"), SummaryMax)
	if d.Summary == "" {
		d.Summary = clip(str("description"), SummaryMax)
	}
	d.Hubs = strs(fm["hubs"])
	d.Aliases = strs(fm["aliases"])
	if name := str("name"); name != "" {
		d.Aliases = append(d.Aliases, name)
	}
	for _, rel := range AuthoredRels {
		if list, ok := fm[rel].([]any); ok {
			for _, x := range list {
				switch t := x.(type) {
				case string:
					d.Authored = append(d.Authored, Rel{Rel: rel, Dst: t})
				case map[string]string:
					d.Authored = append(d.Authored, Rel{Rel: rel, Dst: t["id"], Why: t["why"]})
				}
			}
			continue
		}
		for _, s := range strs(fm[rel]) {
			d.Authored = append(d.Authored, Rel{Rel: rel, Dst: s})
		}
	}
}

// ---- links and mentions ----

var (
	mdLinkRE  = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	wikiRE    = regexp.MustCompile(`\[\[([^\]|#\n]+)(?:[#|][^\]\n]*)?\]\]`)
	hrefRE    = regexp.MustCompile(`(?i)href="([^"#]+)`)
	pullURLRE = regexp.MustCompile(`github\.com/([\w.-]+/[\w.-]+)/pull/(\d+)`)
	ticketRE  = regexp.MustCompile(`\b(?:ABC|XYZ)-\d{1,5}\b`)
)

func links(body string) []Link {
	seen := map[Link]bool{}
	var out []Link
	add := func(l Link) {
		if l.Target != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	for _, m := range mdLinkRE.FindAllStringSubmatch(body, -1) {
		add(Link{Target: localTarget(m[1])})
	}
	for _, m := range wikiRE.FindAllStringSubmatch(body, -1) {
		add(Link{Target: strings.TrimSpace(m[1]), Wiki: true})
	}
	id := config.CurrentIdentity()
	for _, home := range slices.Compact([]string{id.LaptopHome + "/", id.DevboxHome + "/"}) {
		for at := 0; ; {
			i := strings.Index(body[at:], home)
			if i < 0 {
				break
			}
			i += at
			j := i + strings.IndexAny(body[i:]+" ", " \t\r\n)]\"'`<>|,;*")
			add(Link{Target: trimLineSuffix(strings.TrimRight(body[i:j], ".:"))})
			at = j
		}
	}
	return out
}

// localTarget drops external URLs, anchors-only links and placeholders like (url); "" means not a file link.
func localTarget(t string) string {
	if strings.Contains(t, "://") || strings.HasPrefix(t, "mailto:") || strings.HasPrefix(t, "#") {
		return ""
	}
	if !strings.ContainsAny(t, "/.") {
		return ""
	}
	t, _, _ = strings.Cut(t, "#")
	t, _, _ = strings.Cut(t, "?")
	if u, err := url.PathUnescape(t); err == nil {
		t = u
	}
	return trimLineSuffix(strings.Trim(t, "<>"))
}

func mentions(body string) []string {
	set := map[string]bool{}
	hashRefs(body, set)
	shaRefs(body, set)
	if strings.Contains(body, "/pull/") {
		for _, m := range pullURLRE.FindAllStringSubmatch(body, -1) {
			set["pr:"+m[1]+"#"+m[2]] = true
		}
	}
	if strings.Contains(body, "ABC-") || strings.Contains(body, "XYZ-") {
		for _, m := range ticketRE.FindAllString(body, -1) {
			set["ticket:"+m] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The scanners below find an anchor byte and expand around it: unanchored regexps that start with a character
// class cost most of a full index run on multi-megabyte docs.

func isWord(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// hashRefs finds owner/repo#123 and "PR #1234" (bare numbers go to the default repo).
func hashRefs(body string, set map[string]bool) {
	for i := strings.IndexByte(body, '#'); i >= 0; {
		j := i + 1
		for j < len(body) && isDigit(body[j]) {
			j++
		}
		if n := j - i - 1; n > 0 && n <= 6 && (j == len(body) || !isWord(body[j])) {
			num := body[i+1 : j]
			k := i
			for k > 0 && (isWord(body[k-1]) || strings.IndexByte("./-", body[k-1]) >= 0) {
				k--
			}
			segs := strings.Split(body[k:i], "/")
			if len(segs) >= 2 {
				owner, repo := segs[len(segs)-2], segs[len(segs)-1]
				if owner != "" && repo != "" && isLetter(owner[0]) && isLetter(repo[0]) && !strings.Contains(owner, ".") {
					set["pr:"+owner+"/"+repo+"#"+num] = true
				}
			} else if repo := config.CurrentIdentity().DefaultPRRepo; repo != "" && n >= 3 && saysPR(body[:i]) {
				set["pr:"+repo+"#"+num] = true
			}
		}
		next := strings.IndexByte(body[i+1:], '#')
		if next < 0 {
			break
		}
		i += 1 + next
	}
}

// saysPR: the text before '#' ends in "PR" or "PRs", then optional spaces or colons.
func saysPR(before string) bool {
	before = strings.TrimRight(before, " \t\r\n:")
	for _, w := range []string{"PRs", "PR"} {
		if len(before) >= len(w) && strings.EqualFold(before[len(before)-len(w):], w) {
			start := len(before) - len(w)
			return start == 0 || !isWord(before[start-1])
		}
	}
	return false
}

// shaRefs finds path.ext@abcdef1 (7–40 lowercase hex).
func shaRefs(body string, set map[string]bool) {
	for at := 0; ; {
		i := strings.IndexByte(body[at:], '@')
		if i < 0 {
			return
		}
		i += at
		at = i + 1
		j := i + 1
		for j < len(body) && (isDigit(body[j]) || 'a' <= body[j] && body[j] <= 'f') {
			j++
		}
		if n := j - i - 1; n < 7 || n > 40 || (j < len(body) && isWord(body[j])) {
			continue
		}
		k := i
		for k > 0 && (isWord(body[k-1]) || strings.IndexByte("./-", body[k-1]) >= 0) {
			k--
		}
		p := body[k:i]
		dot := strings.LastIndexByte(p, '.')
		if dot < 0 || dot == len(p)-1 || strings.IndexFunc(p[dot+1:], func(r rune) bool { return r > 127 || !isWord(byte(r)) || r == '_' }) >= 0 {
			continue
		}
		set["code:"+p+"@"+body[i+1:j]] = true
	}
}

var lineSuffixRE = regexp.MustCompile(`:\d+(-\d+)?$`)

// trimLineSuffix drops an editor-style line reference: graph.py:995 → graph.py.
func trimLineSuffix(t string) string { return lineSuffixRE.ReplaceAllString(t, "") }

// ---- html ----

var (
	titleRE   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	hTagRE    = regexp.MustCompile(`(?is)<h([1-6])[^>]*>(.*?)</h[1-6]>`)
	scriptRE  = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	tagRE     = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRE   = regexp.MustCompile(`\s+`)
	htmlLimit = 64 << 10
	htmlScan  = 256 << 10 // title, headings, links and text come from this prefix; the rest is markup or data
)

// HTML reads title, headings, local links and visible text (no line numbers: html has no sections).
func HTML(data []byte) Doc {
	src := string(data[:min(len(data), htmlScan)])
	var d Doc
	if m := titleRE.FindStringSubmatch(src); m != nil {
		d.Title = textOf(m[1])
	}
	for _, m := range hTagRE.FindAllStringSubmatch(src, 200) {
		lvl := int(m[1][0] - '0')
		d.Headings = append(d.Headings, Heading{Level: lvl, Text: textOf(m[2])})
		if d.Title == "" && lvl == 1 {
			d.Title = textOf(m[2])
		}
	}
	for _, m := range hrefRE.FindAllStringSubmatch(src, -1) {
		if t := localTarget(m[1]); t != "" {
			d.Links = append(d.Links, Link{Target: t})
		}
	}
	text := textOf(scriptRE.ReplaceAllString(src, " "))
	d.Body = clip(text, htmlLimit)
	d.Summary = clip(text, SummaryMax)
	d.Mentions = mentions(d.Body)
	return d
}

func textOf(s string) string {
	return strings.TrimSpace(spaceRE.ReplaceAllString(html.UnescapeString(tagRE.ReplaceAllString(s, " ")), " "))
}
