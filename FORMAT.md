# The brain's file formats and code conventions

The contract every part of `brain` (index, hooks, dream, adapters) reads and writes. The design behind it lives in the
brain-redesign box (`plans/DESIGN.md`, v3). Paths are always spelled `<laptop_home>/...` (for example `/Users/<user>/...`) — the devbox
bind-mounts its home (`/home/<user>`) there, and `config.Canonical` maps the other spelling.

## Layout of `~/brain`

```
BRAIN.md        how the brain works, ≤ 80 lines, loaded every session (via ~/.claude/rules/brain.md)
hot.md          this week, ≤ 500 words (baseline, overwritten by the dream)
index.md        the home hub: one line per top hub
memory/         Claude Code auto memory for EVERY repo (autoMemoryDirectory); MEMORY.md ≤ 200 lines / 25 KB
notes/          atomic knowledge, flat: <type>-<slug>.md, ≤ 4 KB each
hubs/           <hub-id>.md, ≤ 200 lines; a Start here or Open questions line ≤ 300 chars
work/<box>/     the saahil-docs boxes, unchanged; + CARD.md (≤ 3 KB) and STATE.md (≤ 8 KB) baselines; log/ history
sessions/<machine>/YYYY/MM/<YYYY-MM-DD>-<session8>-<slug>.md     one per transcript, ≤ 8 KB
journal/YYYY/MM/YYYY-MM-DD.md                                     one per day, ≤ 6 KB (dream)
inbox/          anything dropped for the dream to file
.data/          bulk artifacts (hidden from Obsidian)
.index/brain.db the SQLite index — per machine, never synced
.archive/       superseded versions, byte copies, cut-over manifests
.brain/         brain.toml, evals/, dream state, lock files, owner-questions.jsonl (rule pairs the dream could not settle), live
.import/        shadow copies of old memory folders (Phase A)
```

Shadow phase: until the cut-over, `work/` is empty and the index reads each machine's saahil-docs read-only,
presenting it as `work/<box>/...` (root alias `work` → a pre-cut-over docs folder, settings key `shadow_docs`).
Nothing under any saahil-docs tree is ever written before the cut-over.

## Note frontmatter (notes/, hubs/, CARD.md, brain-ready docs)

```yaml
---
id: finding-image-lane-ocr-recall      # = file name without .md; stable
type: finding                          # see types below
status: current                        # current | proposed | superseded | retired   (docs: live | draft | superseded | done)
summary: one line, ≤ 160 chars
scope: hub                             # global | hub | work
hubs: [image-lane]                     # 1–3; every live note has ≥ 1
about: [system-paddleocr]              # 0–5 ids
evidence: [work/image-lane-2026-10/reports/ocr.md#recall]   # ≥ 1 on finding, decision, lesson; path[#heading] relative to ~/brain
supersedes: []                         # ids
sources: [sessions/devbox/2026/10/2026-10-08-d0d200fd-image-gate.md]   # ≥ 1 when the dream wrote it
related: [{id: decision-x, why: same OCR budget}]   # 0–5, `why` required
aliases: [ocr recall, paddle recall]   # names it answers to, any case
as_of: 2026-10-08                      # when it was true
updated: 2026-10-08T07:00:00Z          # when the file changed
---
```

Types — semantic: `finding decision concept system person example idea experiment`; procedural: `lesson anti-pattern
playbook`; structural: `hub card`; docs under `work/`: `spec plan report review experiment handoff readme state`.
`broader: [<hub-id>]` exists on hubs only (0–2, depth ≤ 3, no cycles; root `index`).

Authored relations are exactly: `hubs about evidence supersedes sources related broader`. Everything else is derived by
code: `links_to part_of section_of copy_of mentions touched produced_by learned_from instance_of`.

## Session note (`sessions/…`)

The path is fixed at the note's first write: `<slug>` is the session's name (or first prompt) then. A later `/rename`
changes `name:`, never the path, because notes cite a session note by its path.

```yaml
---
id: session-2026-10-08-d0d200fd
type: session
machine: devbox
session_id: d0d200fd-294f-46df-b0cc-d655067a45bd
name: image-unified-f1
cwd: /Users/<user>/Documents/worktrees/<repo>/<task>
started: 2026-10-08T01:02:03Z
ended: 2026-10-08T06:00:00Z
transcript: /Users/<user>/.claude/projects/<slug>/<id>.jsonl
works: [image-lane-2026-10]          # work boxes touched
touched: [work/image-lane-2026-10/STATE.md, ...]   # files read or written by any tool, Bash included (≤ 60, most-used first)
produced: [work/image-lane-2026-10/.data/run-7/result.json]
prs: [example-org/work-repo#3488]
corrections: 2                       # owner prompts that corrected the agent
messages: [{from: laptop:orchestrator-work, to: devbox:image-unified-f1, id: n-…}]
---
## Owner prompts        (origin.kind == "human" only, secrets redacted, each ≤ 500 chars)
## What it did          (compact summaries + final assistant message, ≤ 2 KB)
## Files                (touched / produced, grouped by work box)
```

## Index (`.index/brain.db`)

SQLite via `modernc.org/sqlite` (no cgo), FTS5. Tables (owned by `internal/store`): `node(id, kind, type, path,
title, summary, status, hubs, scope, as_of, updated, size, sha256, mtime, canonical_of)`, `edge(src, dst, rel, why,
source)`, `fts(id, title, aliases, summary, body)`, `section(doc, idx, heading, line_start, line_end, bytes,
summary)`, `node_stats(id, shown, opened, followed, helpful, harmful, owner_confirmed, last_shown_at,
last_helpful_at, ignored, demoted_at)`, `lookups(time, session, hook, candidates, chosen, abstained, ms, phases)` — never the
prompt text, `llm_calls(key, workflow, step, model, cost_usd, tokens_in, tokens_out, started, ended, status)`
(idempotency key unique), `meta(key, value)`.

The usage funnel. The hooks write `shown` (prompt), `opened` and `followed` (post), and `ignored` (end and compact).
Each counts once per session. A read is Read, Grep or Glob, a Bash read (cat, sed without -i, head, tail, less, rg,
grep) or brain show/sections. The learn step's judge writes `helpful` and `harmful` through the delta `counter` op.
The dream sets `demoted_at` on a node shown 10 times and never opened (an L1 ranking prior of −0.15, not a status
change) and clears it when the node is opened. Lookup rows by hook: `start` and `prompt` (as above), `post`
(`["opened"]` or `["followed"]`, the id), `pull` (`["brain_<verb>"]`, once per session), `end` (`["ignored"]`, the ids).
A `prompt` row's `phases` says where its `ms` went: `open`, `names`, `read`, `digest` and `pointers` in ms, and
`stale_names` when the names cache was older than the index (the hook started a rebuild). Other rows leave it empty.

Never ranked: `status ∈ {superseded, retired}`, `canonical_of != ''` (byte copies), `type ∈ {session, day}`, paths
under `log/` or `.archive/`. Never indexed: sync conflict copies (`<file> (conflict…)`, `config.IsSyncConflictCopy`).

## Code conventions

- Module `saahil.local/brain`, Go 1.26, `CGO_ENABLED=0`, builds for linux/amd64 and darwin/arm64 (`make build`).
- A command area registers itself from `cmd/brain/cmd_<area>.go` with `register("<group> <sub>", summary, fn)` in
  `init()`; never edit another area's file. `main.go` is the dispatcher only.
- Hooks fail open: any error → no output, exit 0, within budget (start ≤ 300 ms, prompt p95 ≤ 150 ms).
- Tests: failing first; the doc comment names the defect it catches; no wall-clock sleeps; `go test -p 6 ./...`;
  synthetic data only in `testdata/` — no real names, emails, customer data, transcripts.
- Never read `.env`; never log prompt text or secrets; kill processes by PID only.
