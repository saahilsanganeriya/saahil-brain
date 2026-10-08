// Package delta applies a skill's itemised changes (ACE-style deltas) to the brain's notes and hubs. The model never
// rewrites a file: it asks for ops, and this package checks each against FORMAT.md and writes the result.
package delta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"saahil.local/brain/internal/config"
)

type Related struct {
	ID  string `json:"id" yaml:"id"`
	Why string `json:"why" yaml:"why"`
}

// Note is one file under notes/ or hubs/: FORMAT.md frontmatter plus the body.
type Note struct {
	ID         string         `json:"id" yaml:"id"`
	Type       string         `json:"type" yaml:"type"`
	Status     string         `json:"status" yaml:"status"`
	Summary    string         `json:"summary" yaml:"summary"`
	Scope      string         `json:"scope,omitempty" yaml:"scope"`
	Hubs       []string       `json:"hubs,omitempty" yaml:"hubs"`
	About      []string       `json:"about,omitempty" yaml:"about"`
	Evidence   []string       `json:"evidence,omitempty" yaml:"evidence"`
	Supersedes []string       `json:"supersedes,omitempty" yaml:"supersedes"`
	Sources    []string       `json:"sources,omitempty" yaml:"sources"`
	Related    []Related      `json:"related,omitempty" yaml:"related"`
	Aliases    []string       `json:"aliases,omitempty" yaml:"aliases"`
	Broader    []string       `json:"broader,omitempty" yaml:"broader"`
	AsOf       string         `json:"as_of,omitempty" yaml:"as_of"`
	Updated    string         `json:"updated,omitempty" yaml:"updated"`
	Extra      map[string]any `json:"-" yaml:"-"` // hand-added keys, kept as they are
	Body       string         `json:"body" yaml:"-"`
}

var (
	semanticTypes   = []string{"finding", "decision", "concept", "system", "person", "example", "idea", "experiment"}
	proceduralTypes = []string{"lesson", "anti-pattern", "playbook"}
	statuses        = []string{"current", "proposed", "superseded", "retired"}
	scopes          = []string{"global", "hub", "work"}
	slugRE          = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	dateRE          = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	needsEvidence   = map[string]bool{"finding": true, "decision": true, "lesson": true}
	listFields      = []string{"hubs", "about", "evidence", "supersedes", "sources", "aliases", "broader"}
	knownKeys       = map[string]bool{"id": true, "type": true, "status": true, "summary": true, "scope": true,
		"related": true, "as_of": true, "updated": true}
)

func init() {
	for _, f := range listFields {
		knownKeys[f] = true
	}
}

func (n *Note) list(field string) *[]string {
	switch field {
	case "hubs":
		return &n.Hubs
	case "about":
		return &n.About
	case "evidence":
		return &n.Evidence
	case "supersedes":
		return &n.Supersedes
	case "sources":
		return &n.Sources
	case "aliases":
		return &n.Aliases
	case "broader":
		return &n.Broader
	}
	return nil
}

func (n *Note) scalar(field string) *string {
	switch field {
	case "status":
		return &n.Status
	case "summary":
		return &n.Summary
	case "scope":
		return &n.Scope
	case "as_of":
		return &n.AsOf
	}
	return nil
}

// Path is where the note lives, relative to the brain root.
func (n Note) Path() string {
	if n.Type == "hub" {
		return "hubs/" + n.ID + ".md"
	}
	return "notes/" + n.ID + ".md"
}

func (n Note) clone() Note {
	c := n
	for _, f := range listFields {
		*c.list(f) = append([]string(nil), *n.list(f)...)
	}
	c.Related = append([]Related(nil), n.Related...)
	return c
}

// merge folds other into n without losing anything: scalars it sets replace, lists and related links are unioned,
// and its body is appended unless n already holds it.
func (n *Note) merge(other Note) {
	for _, f := range []string{"status", "summary", "scope", "as_of"} {
		if v := *other.scalar(f); v != "" {
			*n.scalar(f) = v
		}
	}
	for _, f := range listFields {
		*n.list(f) = union(*n.list(f), *other.list(f))
	}
	n.addRelated(other.Related...)
	n.appendBody(other.Body)
}

func (n *Note) addRelated(rs ...Related) {
	for _, r := range rs {
		found := false
		for _, have := range n.Related {
			found = found || have.ID == r.ID
		}
		if !found {
			n.Related = append(n.Related, r)
		}
	}
}

func (n *Note) appendBody(text string) {
	text = strings.TrimSpace(text)
	if text == "" || strings.Contains(n.Body, text) {
		return
	}
	if strings.TrimSpace(n.Body) == "" {
		n.Body = text + "\n"
		return
	}
	n.Body = strings.TrimRight(n.Body, "\n") + "\n\n" + text + "\n"
}

func union(a, b []string) []string {
	for _, v := range b {
		if v = strings.TrimSpace(v); v != "" && !contains(a, v) {
			a = append(a, v)
		}
	}
	return a
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Parse reads a note file; a file without frontmatter is an error, since every note and hub must carry one.
func Parse(b []byte) (Note, error) {
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Note{}, fmt.Errorf("no frontmatter")
	}
	fm, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		if fm, ok = strings.CutSuffix(rest, "\n---"); !ok {
			return Note{}, fmt.Errorf("frontmatter not closed")
		}
	}
	var n Note
	if err := yaml.Unmarshal([]byte(fm), &n); err != nil {
		return Note{}, fmt.Errorf("frontmatter: %w", err)
	}
	var all map[string]any
	if err := yaml.Unmarshal([]byte(fm), &all); err != nil {
		return Note{}, fmt.Errorf("frontmatter: %w", err)
	}
	for k, v := range all {
		if !knownKeys[k] {
			if n.Extra == nil {
				n.Extra = map[string]any{}
			}
			n.Extra[k] = v
		}
	}
	n.Body = body
	return n, nil
}

// Render writes FORMAT.md frontmatter in a fixed key order with flow-style lists, the form the index parser reads.
func Render(n Note) []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	line("id", n.ID)
	line("type", n.Type)
	line("status", n.Status)
	line("summary", yamlScalar(n.Summary))
	line("scope", n.Scope)
	for _, f := range []string{"hubs", "about", "evidence", "supersedes", "sources"} {
		line(f, flowList(*n.list(f)))
	}
	if len(n.Related) > 0 {
		items := make([]string, len(n.Related))
		for i, r := range n.Related {
			items[i] = "{id: " + yamlScalar(r.ID) + ", why: " + yamlScalar(r.Why) + "}"
		}
		line("related", "["+strings.Join(items, ", ")+"]")
	}
	line("aliases", flowList(n.Aliases))
	line("broader", flowList(n.Broader))
	line("as_of", n.AsOf)
	line("updated", n.Updated)
	keys := make([]string, 0, len(n.Extra))
	for k := range n.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, _ := json.Marshal(n.Extra[k]) // JSON is valid flow YAML
		line(k, string(v))
	}
	b.WriteString("---\n")
	body := strings.TrimLeft(n.Body, "\n")
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	b.WriteString(body)
	return b.Bytes()
}

func flowList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = yamlScalar(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// yamlScalar leaves a plain string plain and double-quotes anything YAML (or a flow list) would read differently.
func yamlScalar(s string) string {
	plain := s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, ",[]{}\"'\n\t\\") &&
		!strings.ContainsAny(s[:1], "-?:#&*!|>%@`") && !strings.Contains(s, ": ") && !strings.Contains(s, " #") &&
		!strings.HasSuffix(s, ":")
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "null", "~", "on", "off":
		plain = false
	}
	if plain {
		return s
	}
	q, _ := json.Marshal(s)
	return string(q)
}

// Check lists every FORMAT.md rule the note breaks; resolve reports whether a referenced id or path exists.
func Check(n Note, rendered []byte, resolve func(kind, ref string) bool) []string {
	var out []string
	bad := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	isHub := n.Type == "hub"
	switch {
	case !slugRE.MatchString(n.ID):
		bad("id %q is not a lowercase slug", n.ID)
	case !isHub && !strings.HasPrefix(n.ID, n.Type+"-"):
		bad("id %q must start with its type %q", n.ID, n.Type+"-")
	}
	if !isHub && !contains(semanticTypes, n.Type) && !contains(proceduralTypes, n.Type) {
		bad("type %q is not a note type", n.Type)
	}
	if !contains(statuses, n.Status) {
		bad("status %q not in %v", n.Status, statuses)
	}
	if s := utf8.RuneCountInString(n.Summary); s == 0 || s > 160 {
		bad("summary is %d chars (1–160)", s)
	}
	if n.AsOf != "" && !dateRE.MatchString(n.AsOf) {
		bad("as_of %q is not YYYY-MM-DD", n.AsOf)
	}
	live := n.Status == "current" || n.Status == "proposed"
	if isHub {
		if len(n.Broader) > 2 || (n.ID != "index" && len(n.Broader) == 0) {
			bad("a hub has 1–2 broader hubs (only index has none)")
		}
		if lines := bytes.Count(rendered, []byte("\n")); lines > config.HubMaxLines {
			bad("hub is %d lines (> %d)", lines, config.HubMaxLines)
		}
	} else {
		if !contains(scopes, n.Scope) {
			bad("scope %q not in %v", n.Scope, scopes)
		}
		if len(n.Hubs) > 3 || (live && len(n.Hubs) == 0) {
			bad("a live note sits in 1–3 hubs, has %d", len(n.Hubs))
		}
		if len(n.Broader) > 0 {
			bad("broader is for hubs only")
		}
		if len(rendered) > config.NoteMaxBytes {
			bad("note is %d bytes (> %d): keep the facts, point sources at the original", len(rendered), config.NoteMaxBytes)
		}
		if n.Type == "lesson" && utf8.RuneCountInString(strings.TrimSpace(n.Body)) > config.LessonMaxChars {
			bad("a lesson body is ≤ %d chars", config.LessonMaxChars)
		}
		if needsEvidence[n.Type] && live && len(n.Evidence) == 0 {
			bad("a %s needs ≥ 1 evidence path", n.Type)
		}
	}
	if len(n.About) > 5 || len(n.Related) > 5 {
		bad("about and related hold ≤ 5 each")
	}
	for _, r := range n.Related {
		if strings.TrimSpace(r.Why) == "" {
			bad("related %s has no why", r.ID)
		}
	}
	check := func(kind string, refs ...string) {
		for _, ref := range refs {
			if ref == n.ID || !resolve(kind, ref) {
				bad("%s %q does not resolve", kind, ref)
			}
		}
	}
	check("hub", n.Hubs...)
	check("hub", n.Broader...)
	check("id", n.About...)
	check("id", n.Supersedes...)
	for _, r := range n.Related {
		check("id", r.ID)
	}
	check("path", n.Evidence...)
	check("path", n.Sources...)
	return out
}
