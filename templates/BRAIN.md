# BRAIN.md — how the brain works

The brain is one folder, `~/brain`, on both machines (laptop and devbox, kept in sync). It holds the owner's work and
what was learned from it, joined as a graph. Read this once per session; it is loaded for you.

## The layout

```
memory/MEMORY.md   the global baseline: facts and rules for every repo (≤ 200 lines, rendered — do not edit)
hot.md             this week (≤ 500 words, rendered)
hubs/<id>.md       topic pages (image lane, identity resolution, git, CI …): scope, start here, open questions
notes/<type>-<slug>.md   small typed notes (≤ 4 KB): finding decision concept system person example idea experiment
                   lesson anti-pattern playbook — ONE set for every repo, organised by hub
work/<box>/        the work itself: specs, plans, reports, tools; CARD.md (what it is) and STATE.md (where it stands)
sessions/ journal/ work/*/log/ .archive/   history: never loaded, read by date or link only
inbox/             drop anything here for the dream to file
```

Every note has `id` (= file name), `type`, `status` (current · proposed · superseded · retired), a one-line
`summary`, `scope` (global · hub · work), `hubs` (1–3) and `as_of` (when it was true). It links with exactly seven
fields: `hubs about evidence supersedes sources related broader`. A claim (finding, decision, lesson) cites
`evidence`: a path, with `#heading` when it is one section.

## The four levels

| Level | What reaches you | Budget |
|---|---|---|
| L0 loaded | this file, `memory/MEMORY.md`, `hot.md` at start; with your first prompt, the STATE + CARD of the work it names (else the cwd's), the files its earlier sessions read most, and its hubs' start-here | ≤ 8,000 chars |
| L1 nudged | each prompt: up to 3 pointers (id, one line, why), never to your work's CARD — or nothing when nothing fits | ≤ 300 tokens |
| L2 pulled | the five tools below, when you need more | you decide |
| L3 read | the file itself, one section by line range, or the artifact | you decide |

Go down a level only when the one above did not answer. A pointer is a hint, not an order: open it if it fits.

## The five tools

| Tool | Use it to |
|---|---|
| `brain_search <words>` (`--deep` follows the graph) | find notes, hubs and docs; superseded notes and copies never rank |
| `brain_show <id or path>` | read a note or doc's frontmatter, summary, links and sections |
| `brain_sections <doc>` | list a big doc's sections with line ranges, then read one |
| `brain_note "<text>"` | drop a fact, decision or lesson into `inbox/` for the dream |
| `/wrap` | at the end of a session, on request: say what to keep (the `wrap` skill) |

Same tools from the CLI: `brain search|show|sections|note`.

## Where to write — three places only

1. **`work/<box>/`** — your real work: specs, plans, reports, results. New docs follow the `brain-write` skill
   (a short header, H2 sections ≤ 20 KB, relative links to results).
2. **`inbox/`** — anything worth keeping that is not work: a fact, a lesson, a correction. The dream files it.
3. **a note through `/wrap`** — when the owner asks to wrap up a session.

Never write `notes/`, `hubs/`, `memory/MEMORY.md`, `hot.md`, CARD.md or `sessions/` by hand: the dream owns them and
applies changes as checked deltas.

## The caps

MEMORY.md ≤ 200 lines / 25 KB · hot.md ≤ 500 words · hub ≤ 200 lines · note ≤ 4 KB · lesson ≤ 300 chars ·
STATE.md ≤ 8 KB · CARD.md ≤ 3 KB · a doc section ≤ ~20 KB · ≤ 3 pointers per prompt.

## The rules that never bend

- **Never delete. Supersede.** A replaced note stays, marked `superseded`, and the new one says `supersedes: [old]`.
  Wrong notes are `retired`, not removed. History is never edited.
- **Keep every fact.** Compress the words, never the facts: numbers, dates, names of systems, commands, the why.
- **Volatile numbers point at their artifact** (`as_of` + `evidence`), they are not copied around.
- **No secrets** in any note, ever. Personal data stays in the brain; it never goes into a commit or a post.
- The dream runs on the devbox when something new has landed: it files captures, learns from sessions, links,
  keeps hubs, renders the baselines, checks everything and commits. It never deletes.
