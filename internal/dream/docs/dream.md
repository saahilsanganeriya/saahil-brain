# The dream: how it runs, what a skill receives, what it may write

`DreamWorkflow` (DESIGN §7) runs on the devbox's local Temporal (`127.0.0.1:7233`, namespace `brain`) inside
`brain-worker`. The hourly Schedule `brain-dream-hourly` starts `DreamTickWorkflow`, which runs the dream as a child
named `dream/<UTC hour>`; `brain dream run --now [--shadow]` starts one directly under the same id.

```
gate ─► snapshot ─► reconcile ─► capture ─► learn ─► file ─► curate.link ─► curate.hubs ─► summarise ─► render ─► check ─► verify ─► reindex ─► commit
det      git+tar     det          brain CLI   distil   file    link           hubs           summarise    brain CLI  lint      verify    brain CLI   state+report+git
                                              Opus     Haiku   Opus           Opus           Haiku                   (revert)  Opus
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

## Cost and idempotency

Key = sha256(workflow, step, model, sha(skill), sha(input)) in `llm_calls` (`.index/brain.db`); a `done` key is
answered from `.data/llm/<key>.json` for $0, so a retry or rerun never pays twice. Before a paid call:
run spent + step budget (Opus $2.50, Haiku $0.50) must be ≤ $8, and today's spend (UTC, failed attempts included)
+ step budget ≤ $20; otherwise no more LLM calls this run, with the reason recorded, and the deterministic steps
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
```

Reports: `.brain/dream-runs/<run-id>.json` per run; the latest skip in `.brain/dream-last-skip.json`. The replay
fixture `testdata/dream-history.json` comes from a run against a synthetic brain; re-record it (run, then
`temporal workflow show -w dream/<hour> -n brain --output json`) when the workflow's step order changes on purpose.
