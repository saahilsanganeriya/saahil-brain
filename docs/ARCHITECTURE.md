# Architecture: how the brain is built

Read this to understand what each part does and why it is shaped that way. Every section starts in plain words and
then gives the precise version.

**In plain words.** The brain is one folder of plain text files, `~/brain`, kept the same on your laptop and your
devbox. Big documents (your plans, specs and reports) stay as they are. Small facts that agents learn become short
notes, grouped by topic pages called hubs. One program, `brain`, reads the folder, builds an index, and hands each
agent session a short digest and a few hints. Once an hour, a job on the devbox called the **dream** reads what
happened (new sessions, new memories, changed documents) and turns it into notes, links and an updated one-page
memory. It never deletes anything.

```
                    LAPTOP (macOS)                                   DEVBOX (Linux, always on)
 ┌──────────────────────────────────────┐          ┌───────────────────────────────────────────────┐
 │ ~/brain   text + laptop bulk          │◄─Unison─►│ ~/brain   text + devbox bulk + .git            │
 │ brain (hooks · MCP · index · guard)   │  hourly, │ brain (hooks · MCP · index · guard)            │
 │ brain bridge  127.0.0.1:7722          │  laptop- │ Temporal 127.0.0.1:7233 (UI :8233)             │
 │   └ launchd; token; allow-listed ops  │  started │   └ brain-worker: dream hourly · learn ·       │
 │ ssh -R 7722 ─────── tunnel ──────────►│──────────│     backfill · evals   (calls claude -p)       │
 │ Claude Code · Codex · Cursor · Prime  │          │ brain-care.timer: daily care agent (Orca)      │
 │ Orca app ─── ssh -L 16768 ───────────►│──────────│ orca serve :6768                               │
 └──────────────────────────────────────┘          └───────────────────────────────────────────────┘
        every agent session, any repo, either machine:
        L0 loaded  →  L1 ≤ 3 hints per prompt  →  L2 search tools  →  L3 read the file
```

Contents: [1 the folder](#1-the-folder) · [2 nodes and edges](#2-nodes-and-edges) · [3 the four levels](#3-the-four-levels-l0l3) ·
[4 hooks per host](#4-hooks-per-host) · [5 MCP tools](#5-mcp-tools) · [6 the write guard](#6-the-write-guard) ·
[7 the dream](#7-the-dream) · [8 Temporal](#8-temporal) · [9 learning and the usage funnel](#9-learning-and-the-usage-funnel) ·
[10 the mesh and the bridge](#10-the-agent-mesh-and-the-bridge) · [11 sync](#11-sync) · [12 git history](#12-git-history) ·
[13 what runs where](#13-what-runs-where)

## 1. The folder

**In plain words.** Two kinds of things live in `~/brain`. **Your work** is organised by project, in boxes under
`work/`. **What was learned** is organised by topic, in `notes/` and `hubs/`, because a lesson from one repo is
useful in another. Some files are loaded into every session; history files are never loaded on their own.

```
~/brain/
  BRAIN.md        how the brain works, ≤ 80 lines, loaded in every session (code-owned: brain install writes it)
  hot.md          this week, ≤ 500 words (the dream overwrites it)
  index.md        the home hub: one line per top hub
  memory/         Claude Code's auto memory for EVERY repo (autoMemoryDirectory)
    MEMORY.md     the global baseline, ≤ 200 lines and ≤ 24 KiB, loaded in every session
  notes/          small typed notes, flat: <type>-<slug>.md, ≤ 4 KB each
  hubs/           topic pages: <hub-id>.md, ≤ 200 lines; a Start here or Open questions line ≤ 300 chars
  work/<box>/     your work: specs, plans, reports, tools, results
    CARD.md       what the box is, where it stands, its key files (≤ 3 KB, the dream keeps it current)
    STATE.md      where the work stands now (≤ 8 KB, overwritten)
    log/          history of the box (never loaded)
  sessions/<machine>/YYYY/MM/<date>-<session8>-<slug>.md     one note per Claude Code transcript, ≤ 8 KB, rewritten when it grows; the path never changes
  journal/YYYY/MM/YYYY-MM-DD.md                               one page per day, ≤ 6 KB (the dream)
  inbox/          drop anything here; the dream files it
  .data/          bulk artifacts (hidden from Obsidian)
  .index/         brain.db, the SQLite index: per machine, never synced
  .archive/       superseded versions, byte copies, backups, cut-over manifests
  .brain/         live marker, evals/, dream state and reports, metrics/, owner-questions.jsonl, locks
  .import/        copies of old memory folders, made by brain import memory
```

**Baselines and logs.** A *baseline* is a small file that is overwritten and capped: `MEMORY.md`, `hot.md`, hubs,
`STATE.md`, `CARD.md`. Baselines are loaded or pointed to. A *log* is append-only and is never loaded by itself:
`sessions/`, `journal/`, `work/*/log/`, `.archive/`. This split is the design's main rule. A brain that keeps growing
one big always-loaded ledger gets ignored by agents; a capped baseline plus never-loaded logs does not.

## 2. Nodes and edges

**In plain words.** The brain is also a graph, but the graph is not a second copy of anything. The index computes
it from the files: every file is a node, and every link, folder and session trace is an edge.

### Note types

| Kind | Types | File | Cap |
|---|---|---|---|
| semantic (what we know) | `finding decision concept system person example idea experiment` | `notes/<type>-<slug>.md` | 4 KB |
| procedural (how to act) | `lesson anti-pattern playbook` | `notes/<type>-<slug>.md` | lesson 300 chars; others 4 KB |
| structural | `hub`, `card` | `hubs/<id>.md`, `work/<box>/CARD.md` | 200 lines, 3 KB |
| docs under `work/` | `spec plan report review experiment handoff readme state` | anywhere in a box | none (STATE.md 8 KB) |
| episodic (what happened) | `session`, `day` | `sessions/`, `journal/` | 8 KB, 6 KB |
| derived by the indexer | `section` (a heading range of a doc > 50 KB), `artifact` (a data file and its shape), `pr`, `ticket`, `repo` | none | — |

Every note has frontmatter: `id` (= file name), `type`, `status` (`current · proposed · superseded · retired`),
`summary` (one line, ≤ 160 chars), `scope` (`global · hub · work`), `hubs` (1–3), `aliases`, `as_of` (when it was
true) and `updated` (when the file changed).

### The seven relations a model may write

| Field | Meaning | Limit |
|---|---|---|
| `hubs` | the note is a member of these hubs | 1–3; every live note has one |
| `about` | the system, concept, person or work it is about | 0–5 |
| `evidence` | the file (or `file#heading`) that proves a claim | ≥ 1 on finding, decision, lesson |
| `supersedes` | this note replaces an older note on the same question | no cycles |
| `sources` | what the note was distilled from | ≥ 1 when the dream wrote it |
| `related` + `why` | a weak link with a one-line reason | 0–5 |
| `broader` | (hubs only) the parent hub | 0–2, depth ≤ 3 |

### Relations the code derives (never written by hand)

`links_to` (links in the text) · `part_of`, `section_of` (folders, headings) · `copy_of` (same content hash: byte
copies collapse to one node) · `mentions` (aliases, PR and ticket ids) · `touched`, `produced_by` (from session
traces: every tool input, Bash included) · `learned_from`, `instance_of` (the learning loop), plus global PageRank.

**Why only seven.** Models misuse a large relation vocabulary. Seven well-defined fields that lint can check keep the
graph clean; everything mechanical is computed.

### Hubs

```
                       index.md (home)
            ┌──────────────┼──────────────────────────────┐
     domain hubs       domain hubs                practice hubs (apply in every repo)
   ┌────┼─────┐         ┌───┴────┐                 git · CI · PR shipping · devbox and laptop
  lane  lane  lane     sub-hub  sub-hub            agents · PII and security · explaining …
     ▲
     └── a note can sit in up to three hubs; membership lives on the note (hubs: [a, b])
```

A hub page holds a scope paragraph, aliases, a "start here" list (≤ 7), current decisions and top lessons, ≤ 5 open
questions and child hubs. Its member list is a query over the notes, never a hand-kept list. `brain init` seeds
generic hubs (the home page and practice topics). You add a hub for each domain of your own work before the first
filing; after that the dream maintains hubs and proposes new ones when ≥ 8 notes from ≥ 2 boxes share a topic.

## 3. The four levels (L0–L3)

**In plain words.** An agent gets a little of the brain for free and pulls more only when it needs it. Each level
has a hard budget, so the brain never floods a session.

```
 L0 LOADED  every session    BRAIN.md + memory/MEMORY.md (Claude Code loads it natively)
                             + start digest ≤ 8,000 chars: hot.md, this work's STATE + CARD, files earlier
                               sessions of this work read most, its hubs' start-here lists
 L1 NUDGED  every prompt     ≤ 3 pointers (id, one line, why) or NOTHING                ≤ 300 tokens
 L2 PULLED  when it decides  brain_search · brain_show · brain_sections · brain_neighbors · brain_note
 L3 READ    when it decides  the file, one section by line range, or the artifact
```

**How pointers are ranked.** score = relevance (aliases + BM25 over title and summary) + graph proximity (one hop in
the prompt hook; full personalised PageRank in `brain search --deep`) + usefulness (helpful and harmful counts) +
recency of the last helpful use + scope fit + a prior by type. Never ranked: superseded or retired notes, byte copies,
sessions, journal days, `log/` and `.archive/`. The prompt hook skips the agent's own CARD (it is already in the
digest), demotes paused and done boxes, and abstains when no match is strong. One slot may go to a note shown
fewer than 3 times, so new notes get a chance.

**Measured speed (reference setup).** FTS5 BM25 2–6 ms; prompt hook p95 82–104 ms end to end; start hook p95 well
under its 300 ms budget.

## 4. Hooks per host

**In plain words.** A hook is a small command an agent host runs at fixed moments: when a session starts, when you
send a prompt, after each tool call, when the session ends. The brain uses them to hand in the digest and pointers
and to record what each session did. Every hook fails open: on any error it prints nothing and exits 0.

| Moment | Claude Code (the `saahil-brain` mod) | Codex | Cursor | Prime / omp |
|---|---|---|---|---|
| session start (L0 digest) | `SessionStart`, `SubagentStart` → `brain hook start` | `SessionStart` | `sessionStart` | extension `before_agent_start` |
| prompt (L1 pointers) | `UserPromptSubmit` → `brain hook prompt` | `UserPromptSubmit` | none (Cursor cannot inject on submit) | extension |
| after a tool call | `PostToolUse` → `brain hook post` (counts opened / followed pointers) | `PostToolUse` | — | — |
| session end | `SessionEnd` → `brain hook end` (writes the session note in the background) | — | — | — |
| after compaction | `PostCompact` → `brain hook compact` | — | — | — |
| write guard | `tool.call` on Write / Edit / Read under `~/brain` → `brain guard` | lint at dream | lint at dream | lint at dream |
| mesh inbox | `PostToolUse`, `UserPromptSubmit`, `Stop` → `brain hook inbox` (in `settings.json`) | `PostToolUse`, `Stop` | `stop` | — |
| pull tools (L2) | in-process tools `mcp__saahil-brain__brain_*` | `brain mcp` (stdio) | `brain mcp` | CLI / MCP |

The mod also draws a pane (current hubs, pointers shown, related notes) and a band
(`brain: <hub> · synced 12m ✓ · dream 3h ago`). Claude Code is the only host with these.

```
 you type a prompt ─► UserPromptSubmit ─► brain hook prompt ─► alias gate ─► BM25 ─► one hop ─► ≤ 3 pointers or nothing
                                                                                      │
 agent reads a pointed file ─► PostToolUse ─► brain hook post ─► node_stats.opened++ ◄┘
 session ends ─► SessionEnd ─► brain hook end ─► sessions/<machine>/…/<note>.md + node_stats.ignored for unopened
```

The lookup log records which notes were candidates and which were shown, with timings. It never stores prompt text.

## 5. MCP tools

| Tool | Does |
|---|---|
| `brain_search <q>` (`deep: true` follows the graph) | rank notes, hubs and docs |
| `brain_show <id or path>` | a node's frontmatter, summary, sections and edges |
| `brain_sections <doc>` | a big doc's sections with line ranges, so the agent reads one section |
| `brain_neighbors <id>` | nodes within N hops |
| `brain_note "<text>"` | drop a fact, decision or lesson into `inbox/` for the dream |

In Claude Code these are in-process mod tools named `mcp__saahil-brain__brain_search` and so on. Other hosts run
`brain mcp`, an MCP stdio server. The same verbs exist on the command line: `brain search|show|sections|neighbors|note`.

## 6. The write guard

**In plain words.** The guard accepts any document, but it is strict about the files that feed the always-loaded
layer, because one bad baseline costs every session.

| Write | Guard decision |
|---|---|
| a new spec, plan or report under `work/` without the 4-field header, or a section > 20 KB | warn |
| `STATE.md` over 8 KB, ledger-style growth, an invalid `CARD.md`, `MEMORY.md`, hub or note | block |
| a Read of a doc over 50 KB | allowed; the result gets "N sections — use brain_sections" appended |

The brain-ready header for big docs: `type`, `status`, `summary`, `hubs`; H2 sections of ≤ ~20 KB; links to results
by relative path. Hosts without the mod get the same checks from `brain lint` at every dream.

## 7. The dream

**In plain words.** Once an hour, the devbox asks: did anything new land since the last dream? If yes, and enough
has piled up, it runs a fixed list of steps. Code does the safe, checkable parts. A model does only the judgment
parts, and the model can only propose edits; code checks and applies them.

```
 hourly Temporal Schedule brain-dream-hourly
   └─ gate: new material? (sessions/, memory/ captures, inbox/, changed docs under work/)      no ─► skip
            (docs = .md files the index reads: never below artifacts/, node_modules/, cache*/, dot-folders)
            ≥ 12 h since the last dream OR ≥ 30 new sessions?                                  no ─► wait
            lock free (.brain/dream.lock, stale after 3 h)?                                     no ─► wait
   └─ run:
      snapshot ─► reconcile ─► capture ─► learn ─► demote ─► file ─► curate.link ─► curate.hubs
      git + tar   sync copies   missed      Opus    ranking   Haiku   Opus           Opus
                  arrivals      session              prior
                                notes
        ─► curate.merge ─► curate.cards ─► summarise ─► held ─► render ─► check ─► verify ─► reindex ─► commit
           Opus            Haiku ×≤ 4      Haiku        lost     MEMORY.md  lint     Opus      brain      git + report
           duplicates,     stale CARDs     big docs     edits    hot.md     fails →  re-reads  index
           conflicts                                    land     hubs       revert   evidence
```

| Rule | Value |
|---|---|
| Per run | ≤ 50 note edits; ≤ 60 sessions; ≤ 40 captures; ≤ 10 big docs; the rest waits for the next dream |
| Edit cap order | the cap counts files: a write to a file the run already changed costs nothing. learn and file first (they use up their input); curate.link, curate.hubs, curate.merge, curate.cards and verify then get equal shares of what is left (a share a step does not use goes to the steps after it). An edit the cap holds is marked `held`, and also `lost` when its step judges only this run's proposals (every step but curate.merge and curate.cards). After summarise, the `held` step lands the lost edits whose file no later step wrote, in an equal share with verify of what is left; an edit that lands is no longer `lost`. learn applies each session's edits whole: the first session the cap has no room for, and every session after it, wait for the next dream (the sessions watermark stops before it; learn does not reflect on them or propose them to the later steps). A session larger than the cap applies what fits |
| Models | Opus 5.5 for judgment (learn, link, hubs, merge, verify); Haiku 5.5 for bulk (file, cards, summaries) |
| Cost | ≤ $8 per run; ≤ $100 per UTC day; a stop skips the remaining LLM steps, the code steps still finish |
| Writes | only `write` actions to clean `.md` paths under `notes/ hubs/ journal/ memory/ work/` or `hot.md`, `index.md`; ≤ 64 KB each |
| Deletes | never: a `delete` is rejected and counted; every overwritten file is copied to `.archive/dream/<run>/before/` first |
| Lint fails | every file the run edited is restored; files it created move to `.archive/dream/<run>/reverted/` |
| Retries | a model answer is cached by an idempotency key (workflow, step, model, skill hash, input hash): a retry never pays twice |

**The model contract.** Each LLM step runs `claude -p` with a skill file (`skills/<name>.md`) as the system prompt, a
JSON list of material on stdin, and a JSON schema for the answer (`{"edits": [...], "notes": "..."}`). The call runs in
an empty temp folder, with no hooks, no MCP servers and no settings: it sees the brain only through its input.

**curate.merge** finds duplicate global notes and conflicting rules. It keeps the newest note of a duplicate group
with every distinct fact, supersedes the others, narrows a conflict when dates settle it (a line at the end of the
older note, or the why of its `related` link to the newer note when the body has no room, as in most lessons), and
queues a pair it cannot settle in `.brain/owner-questions.jsonl` for you. **curate.cards** refreshes up to 4 CARDs per dream whose box has
work at least 1 h newer than the CARD.

## 8. Temporal

**In plain words.** Temporal is a workflow engine: it runs long jobs step by step, retries a failed step, and keeps a
record of every run. The brain runs its own small Temporal server on the devbox, bound to localhost. It is personal
and local; it is never a company Temporal Cloud account.

| Piece | Detail |
|---|---|
| server | `temporal server start-dev --ip 127.0.0.1 --port 7233 --ui-port 8233 --db-filename ~/.local/state/brain/temporal.db --namespace brain` (systemd user unit `brain-temporal`) |
| worker | `brain-worker` (systemd user unit, after `brain-temporal`): queue `brain-dream` for workflows and code steps; `brain-llm-opus` (1 call at a time) and `brain-llm-haiku` (2 at a time) for model calls |
| workflows | `DreamTickWorkflow` (the schedule's action) → `DreamWorkflow` as child `dream/<UTC hour>`; `LearnSessionWorkflow` (`learn/<session8>`, one per new session); `BackfillWorkflow` + `BackfillBatchWorkflow` (8 boxes per batch, continue-as-new); `EvalWorkflow` |
| schedules | `brain-dream-hourly` (`brain dream schedule`); `brain-eval-weekly` (`brain eval schedule`) |
| history | kept 30 days (`temporal operator namespace update -n brain --retention 720h`) |
| loss | losing `temporal.db` loses run history only: every input is a file, and answers are cached in `.data/llm/` |

Rule for what is a workflow: **if it calls a model or runs unattended for more than a few seconds, it is Temporal;
if a hook or an agent waits on it, it is the `brain` binary.**

## 9. Learning and the usage funnel

**In plain words.** The brain watches which hints agents actually open, and learns from sessions where you corrected
an agent. Hints that never help sink; lessons that help rise.

```
 session transcript ─► signals (code): tool errors, test runs, corrections, pointers shown / opened
                        └─► judge (Haiku): which shown notes helped or misled ─► helpful / harmful counters
                             └─► corrected, failed, or tests red at the end? ─► reflect (Opus)
                                  └─► lessons ("when X → do Y, because Z", ≤ 300 chars), anti-patterns, playbooks
```

Counters live in `node_stats` in the index, not in files: `shown`, `opened`, `followed`, `ignored`, `helpful`,
`harmful`, `last_helpful_at`, `demoted_at`. A node shown 10 times and never opened gets a ranking penalty (not a
status change); opening it clears the penalty. `brain doctor` prints the pull rate (sessions that used a brain tool)
and the open rate (pointers opened per shown).

## 10. The agent mesh and the bridge

**In plain words.** Agents on the laptop and on the devbox can list each other and send each other notes. The devbox
cannot ssh into a company-managed Mac (its ssh server is off by policy), so the laptop runs a tiny server, the
**bridge**, that listens only on the laptop's own localhost. The laptop opens an ssh tunnel to the devbox, so the
devbox can reach that localhost port. The bridge answers only a short allow-list, with a token.

```
 laptop                                                         devbox
 brain bridge serve 127.0.0.1:7722 (launchd, KeepAlive)
 com.saahil.brain-tunnel: ssh -N -R 127.0.0.1:7722:127.0.0.1:7722 <devbox-host>
        ▲                                                         │
        └──── HTTP on loopback, Bearer token ◄── 127.0.0.1:7722 ◄─┘ brain peer … / brain agents note laptop:<name>
 laptop ─── ssh <devbox-host> brain rpc ───────────────────────────► (laptop → devbox: the laptop's own ssh)
```

| Piece | Detail |
|---|---|
| allow-list | `status`, `sessions`, `note`, `receipt`, `job`, and `step <name>` for named steps only: `version doctor install index sync update metrics hook-bench view agents-list init import-memory sync-setup sync-dry-run sync-enable sync-archive-conflicts session-backfill eval-human-build eval-human` (and the one-time cut-over steps). Not a shell |
| token | `~/.config/brain/bridge.token` on both machines, mode 600, ≥ 32 chars |
| log | every request is logged in `~/.local/state/brain/bridge.log`, never with its text |
| discovery | `~/.claude/sessions/*.json` registries; a session is alive by pid + process start time; `/rename` names are the addresses |
| messages | `brain agents note <machine>:<name> "…"` writes a file into `~/.local/state/agents/inbox/<session-id>/` on the target machine; the inbox hook delivers it inside that session at its next tool call, prompt or turn end; it is claimed by atomic rename (exactly once) and a receipt goes to `delivered/`; the sender sees `DELIVERED` or `QUEUED` |
| idle Orca terminal | on the devbox, an idle target also gets a nudge through Orca so it wakes |
| laptop asleep | a note waits in a queue; `brain peer step <name> --wait D` asks the laptop's status every 10 s until it answers or D passes, then starts the step once; the devbox carries on |

Native cross-machine messaging in Claude Code needs Remote Control, which does not work with API keys, so the mesh
uses files keyed by session id. A note can never reach the wrong session in the same folder.

## 11. Sync

**In plain words.** The laptop pushes and pulls `~/brain` text to and from the devbox with Unison every hour. Bulk
outputs stay on the machine that made them. When both sides changed the same file, the newer copy wins and the other
is kept as a conflict copy, never lost.

| Rule | Value |
|---|---|
| tool | Unison over ssh, started by the laptop (LaunchAgent `com.saahil.brain-sync`: hourly and at load; `brain sync` by hand; the devbox asks through the bridge). When launchd stops spawning the agent (last exit 78), the bridge records launchd's view and reloads the agent, at most once an hour |
| options | `-batch -auto -times -prefer newer -copyonconflict -maxsizethreshold 2048` (files over 2 MB are skipped and listed); run with `UNISONLOCALHOSTNAME=laptop`, so the archive keeps one name when macOS renames the laptop on a new network |
| never synced | `.index`, `.data`, `.archive`, `.git`, `*.db*`, locks, the dream's own state (`.brain/dream-state.json`, `.brain/dream-last-skip.json`), `.DS_Store`, and folders named `artifacts`, `.sync-backups`, `ctx`, `target*`, `*.egg-info`, `node_modules`, `__pycache__`, `.venv`, `venv` |
| under `work/` | text only: `md txt py sh js toml yml yaml` and files with no extension |
| conflicts | the loser stays as `<file> (conflict…)`; `brain sync --archive-conflicts` moves losers to `.archive/conflicts/<machine>/`. The index and the session-note writer never read a conflict copy |
| half-copies | a Unison temp file (`.unison.*`) older than 2 h is an abandoned transfer; every sync archives it |
| after a sync | the laptop re-indexes, so its pointers stay fresh |

## 12. Git history

`~/brain/.git` lives on the devbox only. The dream's snapshot step commits brain text before every run and its
commit step commits after. When another git process holds a lock in the repository, the dream's git commands wait and
try again for about 1 minute. Under `work/`, git tracks only `CARD.md`: the boxes hold hundreds of GB of results, and
hashing them once filled the disk (see [LESSONS.md](LESSONS.md#2-the-git-snapshot-hashed-all-of-work)). `memory/`
and `notes/` are also saved as a tarball before each dream. Undo a bad dream with `git revert` of its commit, or copy
files back from `.archive/dream/<run>/before/`.

## 13. What runs where

| `brain` binary (both machines, no server) | Temporal worker (devbox only) |
|---|---|
| hooks: digest, ≤ 3 pointers, session-end capture, inbox | `DreamWorkflow` (hourly, change-gated) |
| `search`, `show`, `sections`, `neighbors`, `note`, MCP | `LearnSessionWorkflow` (one per new session) |
| write guard, lint, incremental index, PageRank | `BackfillWorkflow` (one-time, ≤ $80) |
| agents mesh, `machine`, `sync`, bridge | `EvalWorkflow` (weekly) |
| `doctor`, `metrics`, `install`, `view` | |

Judgment lives in skills (markdown prompts under `skills/`), not in code: `file`, `distil`, `link`, `hubs`, `merge`,
`card`, `summarise`, `judge`, `verify`, `wrap`. When the model gets better, the brain gets better with no code change.
