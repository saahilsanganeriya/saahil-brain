# The dream: how it runs, what a skill receives, what it may write

`DreamWorkflow` (DESIGN §7) runs on the devbox's local Temporal (`127.0.0.1:7233`, namespace `brain`) inside
`brain-worker`. The hourly Schedule `brain-dream-hourly` starts `DreamTickWorkflow`, which runs the dream as a child
named `dream/<UTC hour>`; `brain dream run --now [--shadow]` starts one directly under the same id.

```
gate ─► snapshot ─► reconcile ─► capture ─► learn ─► file ─► curate.link ─► curate.hubs ─► curate.merge ─► summarise ─► render ─► check ─► verify ─► reindex ─► commit
det      git+tar     det          brain CLI   distil   file    link           hubs           merge          summarise    brain CLI  lint      verify    brain CLI   state+report+git
                                              Opus     Haiku   Opus           Opus           Opus ×≤2       Haiku                   (revert)  Opus
```

## Gate

Material = `.md` files changed after the area's watermark in `.brain/dream-state.json`:

| Area | Roots | Per-run cap | Consumed by |
|---|---|---|---|
| sessions | `sessions/` | 60 | learn (`distil`) |
| captures | `memory/` (not `MEMORY.md`), `inbox/` — the one-time `.import/` copies are filed by `brain file`, not the dream | 40 | file (`file`) |
| docs | `work/`, plus the shadow root (saahil-docs, read-only) presented as `work/…` with `--shadow` | 10 big docs (≥ 20 KB) | summarise (`summarise`) |

Oldest first; what is over the cap waits. A watermark advances only when its step ran (an `ok` answer, cached or
not, or no input) — a missing skill, a cost stop or a failure leaves the material for the next dream. Before the first
dream the docs area starts 24 h back; older docs are the backfill's job. `notes/`, `hubs/`, `journal/` are never
scanned, so the dream's own writes do not wake the next dream.

Skip when nothing is new; otherwise run only if ≥ 12 h since the last dream or ≥ 30 new sessions (`--now` lifts this
rule only). Lock: `.brain/dream.lock` (pid, start, run id), stale after 3 h.

## LLM steps — the skill contract

The skill is `<repo>/skills/<name>.md` (`brain-worker --skills`); a missing file is a recorded skip. It is passed as
`--append-system-prompt` to `claude --bare -p --model <m> --output-format json --max-budget-usd <x>
--no-session-persistence --json-schema <EditsSchema>`, in an empty working directory, with this JSON on stdin:

```json
{"step": "file",
 "material": [{"path": "memory/feedback-x.md", "content": "…", "truncated": false}],
 "proposed": [{"path": "notes/finding-y.md", "action": "write", "content": "…", "why": "…"}],
 "files_left_out": 0}
```

`material` is the area's files (≤ 16 KB each, ≤ 400 KB in all); `proposed` is every edit earlier steps of this run
asked for (curate.link, curate.hubs and verify read only this). The answer must match `EditsSchema` (`edits.go`):

```json
{"edits": [{"path": "notes/finding-y.md", "action": "write", "content": "<whole file>", "why": "…"}], "notes": "…"}
```

Applied only when: `action` is `write`; the path is a clean relative `.md` path under `notes/ hubs/ journal/ memory/
work/` or is `hot.md` / `index.md`; no dot-folder; no symlinked file; the real directory is inside the brain root and
not under saahil-docs or `~/.claude`; content ≤ 64 KB; fewer than 50 edits applied this run. Anything else is listed in
the report's `rejected`; a `delete` is never applied and is counted in `deletions_rejected`. Identical content is a
no-op, not an edit. Every overwritten file is first copied to `.archive/dream/<run-id>/before/`.

## curate.merge — merge, do not copy

Filing makes about one note per memory. Thus one rule can exist as several global notes, and an old rule can
contradict a newer one. `curate.merge` finds these notes and changes them with checked deltas. It never deletes.

| Part | What it does |
|---|---|
| gate (`MergeGate`) | Reads every current global note: id, type, as_of, project (from a `.import/<machine>/<repo>/` source), summary. Runs when this list is not the list that the last complete merge left (`.brain/merge-state.json`), or 24 h after that merge. With no change, the same input gets a $0 answer from the ledger. |
| find (`MergeStep`, Opus) | The `merge` skill, stage `find`, names duplicate groups, conflicts (`supersede`, `narrow` or `owner`) and stale notes. |
| settle (code) | Keeps only ids of current global notes, each in one group. Keeps the newest note of a group. Sends a `supersede` or `narrow` between equal or missing dates to the owner. Replaces a stale note only with a note of the same or a later date. A note that the plan supersedes takes no further part. At most 20 groups per run. |
| bodies (`MergeStep`, Opus) | Stage `bodies`, one call for each group, writes the body of the kept note. The body holds every distinct fact of the group, within the cap of the type. A failed call costs only its group, and the ledger keeps each answer on the notes of its group. |
| units (code) | Group: `update_note` (`replace_body`) and one `supersede` for each other note. Narrow: append "Narrowed <date> by <newer>: <why>" and a `related` link; not again when the line is there. Conflict `supersede` and stale: `supersede`. Owner: a question, no edit. The applier checks the units in dry run. When it refuses one op of a unit, the step drops the full unit. |
| apply | `ApplyEdits`, as for every step: backups, the 50-edit cap, revert when lint fails. |
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
it; `--out <file>` writes the delta for `brain apply-delta`; `--find-answer` and `--bodies-answer` replay recorded
answers instead of calling the model.

## Cost and idempotency

Key = sha256(workflow, step, model, sha(skill), sha(input)) in `llm_calls` (`.index/brain.db`); a `done` key is
answered from `.data/llm/<key>.json` for $0, so a retry or rerun never pays twice. Before a paid call:
run spent + step budget (Opus $2.50, Haiku $0.50) must be ≤ $8, and today's spend (UTC, failed attempts included)
+ step budget ≤ $100 (owner, 2026-10-08: up to $1,000 is fine; the $8 per-run cap stays); otherwise no more LLM calls this run, with the reason recorded, and the deterministic steps
finish. The prompt text is never stored or logged — only its hash.

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
```

Reports: `.brain/dream-runs/<run-id>.json` per run; the latest skip in `.brain/dream-last-skip.json`. The replay
fixture `testdata/dream-history.json` comes from a run against a synthetic brain; re-record it (run, then
`temporal workflow show -w dream/<hour> -n brain --output json`) when the workflow's step order changes on purpose. It
is from before curate.merge and must replay without the step. `testdata/dream-merge-history.json` goes through a merge
that edits notes; `go test -tags record -run TestRecordMergeHistory ./internal/dream` records it again on a throwaway
dev server.
