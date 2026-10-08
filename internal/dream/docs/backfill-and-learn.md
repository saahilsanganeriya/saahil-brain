# Backfill and learn: the two workflows beside the dream

Both run in `brain-worker` on the devbox's local Temporal (namespace `brain`), with the dream's ledger, idempotency
keys and per-model queues (`dream.md`). Neither deletes anything; both write knowledge through `ApplyEdits`.

## BackfillWorkflow — a CARD for every work box (DESIGN §4.7, PLAN D2)

```
plan (det) ──► batch child × ⌈boxes/8⌉, one at a time ──► report .brain/backfill-runs/<id>.json
  boxes: shadow → saahil-docs/*        per box: card (Haiku, card skill) ─► CARD valid? ─no─► ask again once with the reason
         live   → <brain>/work/*                  └► finished? ─► campaign (Opus, distil) ─► notes/ via ApplyEdits
  skip: CARD newer than every file      continue-as-new after 10 batches
```

- **Where a CARD goes**: no `.brain/live` (shadow) → `.brain/staging/work/<box>/CARD.md`; live → `work/<box>/CARD.md`,
  an older CARD first copied to `.archive/backfill/<run>/`. The index presents staged CARDs at `work/<box>/CARD.md`.
  After the cut-over, `brain backfill promote-cards` moves them in (an existing CARD goes to `.archive/` first).
- **A CARD is refused** when it is over 3 KB, its status is not `live|paused|done`, a hub is not seeded, or a key file
  does not exist in the box or is a superseded / `.archive/` / `log/` copy.
- **Finished campaign** = the CARD says `done`, or the newest file is > 14 days old and STATE says
  finished/landed/merged. It gets ≥ 1 "what we built and why" note plus lessons; none kept → asked again once.
- **Cost**: keys are per (box, skill, input sha), so a rerun pays only for boxes that changed. The cap (`--budget`,
  default $80) is on the ledger's total for `workflow = backfill`, earlier runs included; the $100/day guard still
  applies. A stop marks the remaining boxes `skipped` with the reason.

## LearnSessionWorkflow — `learn/<session8>` (DESIGN §6, PLAN D1)

```
signals (code: note header, transcript tool errors and test runs, lookups shown/opened)
  └► anything to judge? ─► judge (Haiku, judge skill) ─► helpful/harmful counters on pointers really shown
       └► corrected, judged failure, or tests failing at the end? ─► reflect (Opus, distil) ─► edits
```

Started by the dream's learn step, one child per new session in turn (≤ 60 per dream, the dream's $8 cap passed in
as spent-so-far); the dream applies, lints and can revert the edits. A session whose child cannot run goes to the old
batched distil call. `brain learn <session-id>` runs one standalone and applies its own edits.

## Failure modes

| Call | Timeout | Retries | On final failure |
|---|---|---|---|
| plan, report, signals, apply | 15 min | 3 | backfill/learn run fails; nothing half-written (atomic writes) |
| card, campaign, judge, reflect | 20 min | 2 | box or session recorded `failed`; a model-side error is not retried |

```
brain backfill run --dry-run          # every box with its planned action; no model, no Temporal
brain backfill run [--boxes a,b] [--budget 80]
brain backfill status                 # last runs + backfill spend so far
brain learn <session-id>
```
