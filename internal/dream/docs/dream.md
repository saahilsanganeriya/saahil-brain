# The dream: how it runs, what a skill receives, what it may write

`DreamWorkflow` (DESIGN §7) runs on the devbox's local Temporal (`127.0.0.1:7233`, namespace `brain`) inside
`brain-worker`. The hourly Schedule `brain-dream-hourly` starts `DreamTickWorkflow`, which runs the dream as a child
named `dream/<UTC hour>`; `brain dream run --now [--shadow]` starts one directly under the same id.

```
gate ─► snapshot ─► reconcile ─► capture ─► learn ─► demote ─► file ─► curate.link ─► curate.hubs ─► curate.merge ─► curate.cards ─► summarise ─► held ─► render ─► check ─► verify ─► reindex ─► commit
det      git+tar     det          brain CLI   distil   det        file    link           hubs        merge             card            summarise  det       brain CLI  lint      verify    brain CLI   state+report+git
                                              Opus     (index)    Haiku   Opus           Opus        Opus ×≤2          Haiku ×≤4       Haiku                           (revert)  Opus
```

`demote` sets `node_stats.demoted_at` on a node shown 10 times as a pointer and never opened or followed, and clears
it when the node is opened (`rank.Demote`). It is a ranking prior: it changes no file and no status.

`reconcile` counts the sync conflict copies that the doctor's `sync.conflicts` counts, with the same walk
(`scan.ConflictCopies`: no bulk folder, no `.archive`, 8 goroutines). It changes no file: the owner decides the copies.

## Gate

Material = `.md` files changed after the area's watermark in `.brain/dream-state.json`:

| Area | Roots | Per-run cap | Consumed by |
|---|---|---|---|
| sessions | `sessions/` | 60 | learn (`distil`) |
| captures | `memory/` (not `MEMORY.md`), `inbox/` — the one-time `.import/` copies are filed by `brain file`, not the dream | 40 | file (`file`) |
| docs | `work/`, plus the shadow root (saahil-docs, read-only) presented as `work/…` with `--shadow` | 10 big docs (≥ 20 KB) | summarise (`summarise`) |

Oldest first; what is over the cap waits. A watermark advances only when its step ran (an `ok` answer, cached or
not, or no input) — a missing skill, a cost stop or a failure leaves the material for the next dream. Before the first
dream the docs area starts 24 h back; older docs are the backfill's job. `notes/`, `hubs/`, `journal/` and
`work/<box>/CARD.md` are never scanned, so the dream's own writes do not wake the next dream.

Skip when nothing is new; otherwise run only if ≥ 12 h since the last dream or ≥ 30 new sessions (`--now` lifts this
rule only). Lock: `.brain/dream.lock` (pid, start, run id), stale after 3 h.

## LLM steps — the skill contract

The skill is `<repo>/skills/<name>.md` (`brain-worker --skills`); a missing file is a recorded skip. It is passed as
`--append-system-prompt` to `claude --bare -p --model <m> --output-format json --max-budget-usd <x>
--no-session-persistence --json-schema <EditsSchema> --tools ""`, in an empty working directory, with this JSON on
stdin. The call has no tools: `--bare` still reads the user's permission mode, so a call with tools can run Bash and
read or edit any file.

```json
{"step": "file",
 "material": [{"path": "memory/feedback-x.md", "content": "…", "truncated": false}],
 "proposed": [{"path": "notes/finding-y.md", "action": "write", "content": "…", "why": "…"}],
 "files_left_out": 0}
```

`material` is the area's files (≤ 16 KB each, ≤ 400 KB in all); `proposed` is every edit earlier steps of this run
asked for (curate.link, curate.hubs and verify read only this). Learn's reflect is one call per session: its
`material` is the session note, then the `.md` and `.txt` files in the brain that the note lists in `produced` (≤ 16 KB
each, ≤ 64 KB in all); its `proposed` is the notes that already cite the session in `evidence` or `sources`, of any
status (a live session is reflected again in each dream after it grows). Verify reads only the claim notes in
`proposed` (finding, decision, lesson, anti-pattern, playbook), each as the run last wrote it, in batches of at most 12.
Each batch gets the evidence its own notes cite. The answer must match `EditsSchema` (`edits.go`):

```json
{"edits": [{"path": "notes/finding-y.md", "action": "write", "content": "<whole file>", "why": "…"}], "notes": "…"}
```

Applied only when: `action` is `write`; the path is a clean relative `.md` path under `notes/ hubs/ journal/ memory/
work/` or is `hot.md` / `index.md`; no dot-folder; no symlinked file; the real directory is inside the brain root and
not under saahil-docs or `~/.claude`; content ≤ 64 KB; the step's share of the 50-edit cap is not full, or the run already changed the file (the cap counts files, so a second
write to one is free; learn and file
get what is left; curate.link, curate.hubs, curate.merge, curate.cards and verify each get an equal share of what is
left with the curation steps after them, so the first cannot starve the rest; an edit over the share is `held`). A
held edit of curate.merge or curate.cards comes back next run, because these steps rebuild from the whole brain. A held
edit of any other step is also `lost`, because that step judges only this run's material and proposals. After
summarise, the `held` step gives these lost edits a second chance: an edit whose file no later step wrote lands in an
equal share with verify of what is left of the cap (check then lints it, verify judges it on disk), and its record is
no longer `lost`. A learn
session is one unit (`ApplyInput.Whole`): its edits land together, or all are `held` (not `lost`, "waits for the
next dream, whole"). Then learn stops, and that session and every later session wait for the next dream: the sessions
watermark stops before it, and their edits do not go to the later steps. A session that changes more files than the
cap applies what fits, so it cannot stop learn in every dream. A capture of the file step is one unit too: an edit
belongs to the oldest of the run's captures that its `sources` names, and an edit that names none belongs to the
newest. The file step applies the captures oldest first, each whole. The first capture that the cap has no room for,
and every capture after it, wait for the next dream: the captures watermark stops before it, and their edits do not go
to the later steps. The step record says how many captures wait. In every other step, the files that one delta op
writes (a `supersede` writes the new note and the old one) are a group (`Edit.Group`, set from `delta.File.Group`):
the cap reserves the new files of the whole group at its first edit, so the group lands whole or is held whole.
Anything else is listed in
the report's `rejected`; a `delete` is never applied and is counted in `deletions_rejected`. Identical content is a
no-op, not an edit. Every overwritten file is first copied to `.archive/dream/<run-id>/before/`.

A delta skill's answer is applied in dry run first. When the applier refuses an op there (a cap, an id that does not
resolve), the step asks the same skill one more time. The input of that call holds only the refused ops and the reason
for each, and its key is `<step>.repair`. The ops of the repair answer replace the refused ops, and the applier checks
all the ops again. An op that it refuses again, or that the repair leaves out, becomes no edit: the step record counts
it in `refused`, and its reason names the first one. A refused `delete` is not repaired.

## curate.merge — merge, do not copy

Filing makes about one note per memory. Thus one rule can exist as several global notes, and an old rule can
contradict a newer one. `curate.merge` finds these notes and changes them with checked deltas. It never deletes.

| Part | What it does |
|---|---|
| gate (`MergeGate`) | Reads every current global note: id, type, as_of, project (from a `.import/<machine>/<repo>/` source), summary. Runs when this list is not the list that the last complete merge left (`.brain/merge-state.json`), or 24 h after that merge. With no change, the same input gets a $0 answer from the ledger. |
| find (`MergeStep`, Opus) | The `merge` skill, stage `find`, names duplicate groups, conflicts (`supersede`, `narrow` or `owner`) and stale notes. |
| settle (code) | Keeps only ids of current global notes, each in one group. Keeps the newest note of a group. Sends a `supersede` or `narrow` between equal or missing dates to the owner. Replaces a stale note only with a note of the same or a later date. A note that the plan supersedes takes no further part. At most 20 groups per run. |
| bodies (`MergeStep`, Opus) | Stage `bodies`, one call for each group, writes the body of the kept note. The body holds every distinct fact of the group, within the cap of the type. A failed call costs only its group, and the ledger keeps each answer on the notes of its group. A narrow whose older note (not a lesson) cannot take even the link within 4 KB gets one call as a group of one: the body written shorter, every fact kept, in what the line and the link leave. |
| units (code) | Group: `update_note` (`replace_body`) and one `supersede` for each other note. Narrow: append "Narrowed <date> by <newer>: <why>" and a `related` link; not again when the line is there. A body with no room for the line: the link alone, with the line as its why. A note with no room for that link: its shorter body (`replace_body`) with the line, and the link; with no shorter body it waits. Conflict `supersede` and stale: `supersede`. Owner: a question, no edit. The applier checks the units in dry run. When it refuses one op of a unit, the step drops the full unit. |
| apply | `ApplyEdits`, as for every step: backups, the 50-edit cap (it counts files), revert when lint fails. |
| done (`MergeDone`) | Appends the new owner pairs to `.brain/owner-questions.jsonl` (`ids`, `as_of`, `question`, `created`; one line for each pair, never two). Writes the list as the merge left it, so the writes of the merge do not start the next merge. |

The coordinator relays the owner questions. It sets `answered` on a line when it applies the answer of the owner.
`brain doctor` shows how many questions wait: not answered, and both notes still current.

When the bodies call of a group does not run or gives no usable body (cost cap, failure), that group waits. The other
groups, the conflicts and the stale notes still apply. A waiting group is found again by the merge of the next day, so
a group that fails each time costs at most one try a day. The manual sweep of 2026-10-08 paid $0.78 for the find (248
notes) and $0.67 for the bodies of 15 groups in one call; a dry run at 23:25Z paid $0.91 for the find (267 notes), and
one call for 15 groups went over a $1 budget, which is why each group now has its own call.

`brain dream merge` is a dry run against the brain: it shows the gate, the plan after the checks of the code, each
file that would change, and the owner questions. It applies nothing, queues nothing, and keeps its ledger outside
the brain (`--ledger <dir>`, else a temp folder). `--budget` (per call, default $1) and `--cap` (default $2) limit
it; `--out <file>` writes the delta for `brain apply-delta`. `--find-answer` uses a recorded find answer instead of a
model call; the bodies calls then go to the model, unless `--bodies-answer` gives a recorded answer for them too.

## curate.cards — a CARD follows its box

The start digest gives the CARD of a box to each session in that box. The backfill wrote the CARDs once; this step
keeps them current. On 2026-10-09 the image-lane CARD (22:28Z) said "nothing merged", while its STATE (00:25Z) had 10
merged PRs.

| Part | What it does |
|---|---|
| plan (`CardsPlan`) | Live brain only (in shadow the step is skipped). For each box in `work/`: the newest regular file outside bulk folders (`artifacts/`, `cache*`, `venv/`, dot-folders; `CARD.md` is not work) and the mtime of `CARD.md`. A box is stale when that file is ≥ 1 h newer than its CARD, or when it has files and no CARD. A `done` or `paused` CARD is no exception: new work after it (a resumed campaign) makes it stale, and with no new work it stays. Stalest first: no CARD, then the longest lag. At most 4 boxes for each dream; the others wait. Nothing stale: skipped, `no stale cards`. |
| refresh (`BackfillCard`, Haiku) | The `card` skill on the box inventory, as in the backfill, one box at a time. The call is keyed to the dream (`curate.cards/<box>`) and paid under the $8 cap of the run, with the spend of the run before it. A refused answer is asked again once. |
| apply | The CARD comes back as an edit for `ApplyEdits`: backup, lint, revert, the 50-edit cap (it counts files). CARDs do not go to `verify`, which checks notes. An answer equal to the CARD on disk only touches its mtime, so the box is not picked again until its work changes. |
| record | `refreshed`, `unchanged (touched)`, `skipped` (with the reason, for example the cost cap), `failed` and `wait for the next dream`, with box names only (≤ 10 for each list). |

When the cap stops a call, the step skips the boxes that are left, with the reason. A later LLM step still asks:
its own call runs if it fits what the run has left (see Cost and idempotency). `brain dream cards --dry-run` shows what the plan would pick now: each stale box with the age and status
of its CARD and its newest file. It reads only; it calls no model and writes nothing.

## Cost and idempotency

Key = sha256(workflow, step, model, sha(skill), sha(input)) in `llm_calls` (`.index/brain.db`); a `done` key is
answered from `.data/llm/<key>.json` for $0, so a retry or rerun never pays twice. Before a paid call:
run spent + step budget (Opus $2.50, Haiku $0.50) must be ≤ $8, and today's spend (UTC, failed attempts included)
+ step budget ≤ $100 (owner, 2026-10-08: up to $1,000 is fine; the $8 per-run cap stays); otherwise the call is
refused, with the reason recorded. A refused call stops the rest of its own step (the boxes, groups, sessions or
batches that are left), not the later steps: each later step checks its own smallest call against what is left, so
a $2.50 step that does not fit does not cancel the $0.50 summaries or the verify batches after it. Learn's sessions
stay unconsumed after a refusal, so the next dream learns them. The deterministic steps always finish. A verify batch is different: its budget is the step budget or what the $8 cap leaves, whichever is less. It
does not start with less than $1.00 left. The step reason gives the number of claims that stay unverified, and why. The prompt text is never stored or logged — only its hash.

## Failure modes

| Call | Timeout | Retries | On final failure |
|---|---|---|---|
| deterministic activities (gate, snapshot, apply, git, brain CLI) | 15 min | 3, 5 s backoff ×2 | gate/snapshot/commit: run fails, state not advanced, report written; others: step recorded `failed`, run goes on |
| LLM step | 20 min | 2, 30 s backoff (a crash after the model answered can pay again) | step `failed`; a model-side error (budget hit, error result) is not retried and its cost is counted |
| `brain lint` exit ≥ 2 | — | — | every file this run edited is restored from `.archive/dream/<run-id>/before/`; files it created move to `…/reverted/` |

Never deletes: reverts and the lock file are the only removals, and reverts move files into the archive.

## Operating

```
systemctl --user status brain-temporal brain-worker        # deploy/systemd/*.service, user units
brain dream status                                          # last runs, next tick, cost today
brain dream schedule [--shadow=false]                       # create/update brain-dream-hourly
temporal schedule toggle --schedule-id brain-dream-hourly --unpause --reason go -n brain --address 127.0.0.1:7233
brain-worker --llm fake                                     # no model: empty edits (shadow checks)
brain dream merge [--ledger <dir>] [--out delta.json]          # what curate.merge would change; applies nothing
brain dream cards --dry-run                                 # the stale CARDs curate.cards would refresh now
```

Reports: `.brain/dream-runs/<run-id>.json` per run; the latest skip in `.brain/dream-last-skip.json`. The replay
fixture `testdata/dream-history.json` comes from a run against a synthetic brain; re-record it (run, then
`temporal workflow show -w dream/<hour> -n brain --output json`) when the workflow's step order changes on purpose. It
is from before curate.merge and must replay without the step. `testdata/dream-merge-history.json` goes through a merge
that edits notes (from before curate.cards); `testdata/dream-cards-history.json` goes through a CARD refresh.
`go test -tags record -run 'TestRecord(Merge|Cards)History' ./internal/dream` records them again on a throwaway dev
server.
