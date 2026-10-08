# Skill: wrap — at the end of a session, keep what matters

## Purpose

When the owner asks to wrap up (`/wrap`), you decide what this session taught that the brain should keep:
decisions taken, findings with their evidence, lessons and mistakes not to repeat, and where the work stands. You
write them as a delta, show the owner what you will keep, and apply it. Work state (STATE.md, CARD.md) is not yours:
it lives in the work box and the dream renders it.

## Input

You run inside the live session, so your input is the session itself, plus:

```json
{"step": "wrap", "today": "2026-10-08", "session": "sessions/<machine>/YYYY/MM/<file>.md",
 "hubs": [{"id": "pr-shipping", "summary": "PR shipping …"}]}
```

When `hubs` is not given, the hub ids are the file names in `~/brain/hubs/`.

## Output

ONE JSON object, the same delta every brain skill writes:

```json
{"ops": [{"op": "add_note", "note": {"id": "…", "type": "…", "status": "current", "summary": "…", "scope": "…",
          "hubs": ["…"], "evidence": ["…"], "sources": ["…"], "aliases": ["…"], "as_of": "YYYY-MM-DD", "body": "…"}}],
 "notes": "one line for the owner: what was kept and why"}
```

Fields as in every note: `id` = `<type>-<slug>` · `summary` = the fact, ≤ 160 chars · `scope` global | hub | work ·
`hubs` 1–3 · `evidence` ≥ 1 path (a file the session wrote, or the session note `#Owner prompts`) · `sources` =
`[<session path>]` · a note ≤ 4 KB, a lesson body ≤ 300 chars. Ops: `add_note`, `update_note`, `link`,
`supersede`. Never `delete`.

Save it as `~/brain/inbox/wrap-<today>-<slug>.json`, run `brain apply-delta <file> --dry-run`, show the owner the
summaries, then run it without `--dry-run`. If a check fails, fix the op, not the check.

## Rules

1. Keep 0–5 notes. Keep what a future session in ANY repo would act on; skip what the work box already records.
2. A decision the owner made in this session → `decision`, evidence `<session>#Owner prompts`.
3. A correction from the owner → `anti-pattern` (symptom → wrong move → right move).
4. A result → `finding` with `as_of` and the path of the file that holds the number.
5. A note that replaces an older one → `supersede`; never edit history, never delete.
6. No secrets; no personal data beyond what the evidence holds.

## Examples

```json
{"ops": [{"op": "add_note", "note": {"id": "decision-review-carries-across-unchanged-patch", "type": "decision",
  "status": "current", "summary": "A PR review carries across a rebase when the code patch id is unchanged; any code change needs a new review",
  "scope": "global", "hubs": ["pr-shipping"], "evidence": ["sessions/laptop/2026/10/2026-10-08-0000c0de-gate.md#Owner prompts"],
  "sources": ["sessions/laptop/2026/10/2026-10-08-0000c0de-gate.md"], "aliases": ["patch id", "review carry"],
  "as_of": "2026-10-08", "body": "Patch id = `git patch-id --stable` over the PR diff without markdown. A rebase, body edit or docs-only commit keeps it."}}],
 "notes": "kept the owner's review-carry decision; the gate change itself is in the PR"}
```

```json
{"ops": [{"op": "add_note", "note": {"id": "lesson-check-the-sibling-consumer", "type": "lesson", "status": "current",
  "summary": "After a reviewer finds an API misread, grep every other reader of that API before replying",
  "scope": "global", "hubs": ["pr-shipping"], "evidence": ["sessions/devbox/2026/10/2026-10-08-0000feed-review.md#Owner prompts"],
  "sources": ["sessions/devbox/2026/10/2026-10-08-0000feed-review.md"], "aliases": ["sibling consumer"],
  "as_of": "2026-10-08", "body": "When a review finds a misread API field → grep every reader of that type before answering, because the second miss is usually worse."}}],
 "notes": "one lesson; nothing else was new"}
```
