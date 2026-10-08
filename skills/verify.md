# Skill: verify — re-read the files behind every new claim

## Purpose

Before a dream commits, you check each new or changed finding, decision, lesson, anti-pattern and playbook against
the files it cites. A note passes only if its evidence really says what the note claims. You return a verdict per
note — keep, fix or reject — and a delta that applies the fixes. Rejected notes are retired, never deleted.

## Input

```json
{"step": "verify",
 "today": "2026-10-08",
 "proposed": [{"path": "notes/finding-x.md", "action": "write", "content": "<the whole note>"}],
 "evidence": [{"path": "work/<box>/reports/result.md", "content": "<the cited file, up to 16 KB>", "truncated": false}]}
```

`evidence` holds the files that the proposed notes cite in `evidence` and `sources` (sessions, reports, memories).
A path missing from `evidence` could not be read.

## Output

ONE JSON object, nothing else:

```json
{"verdicts": [{"id": "<note id>", "verdict": "keep|fix|reject", "reason": "<one line>"}],
 "ops": [{"op": "update_note", "id": "<note id>", "set": {"summary": "…"}, "replace_body": "…"},
         {"op": "update_note", "id": "<note id>", "set": {"status": "retired"}, "append_body": "Retired by verify on <date>: <reason>"}],
 "notes": "optional: one line"}
```

One verdict per proposed note of the types above; other notes need none.

## Rules

1. **keep** — the evidence states the claim, with the same numbers, dates and scope. No op.
2. **fix** — the claim is right in substance but a number, date, scope, `as_of` or wording is off, or the summary
   is a description instead of the fact. Emit `update_note` with the corrected fields (and `replace_body` when the
   body is wrong). Never weaken a correct claim; never add a fact the evidence does not hold.
3. **reject** — the evidence does not support the claim, contradicts it, or cannot be read. Emit `update_note` with
   `set: {"status": "retired"}` and append the reason. Never `delete`.
4. A lesson must generalise what happened ("When X → do Y, because Z") without contradicting its evidence; a lesson
   drawn from one success with no failure or correction behind it is `fix` → `status: "proposed"`.
5. Judge only against the given files, not against what you believe. Quote nothing longer than a line in reasons.
6. Answer with the JSON only.

## Examples

```json
{"verdicts": [{"id": "finding-batch-64-halves-gate-wall-time", "verdict": "fix",
               "reason": "report says 22 min, not 21; peak memory 5.9 GB"}],
 "ops": [{"op": "update_note", "id": "finding-batch-64-halves-gate-wall-time",
          "set": {"summary": "Gate wall time fell from 42 to 22 minutes with batch size 64 instead of 16 (synthetic 10k-row set, 2026-10-08)"},
          "replace_body": "42 → 22 minutes, memory peak 3.1 → 5.9 GB. Numbers: `work/gate-speed-2026-10/reports/batch-size.md#results`.\n"}]}
```

```json
{"verdicts": [{"id": "lesson-rebase-a-stacked-branch-before-push", "verdict": "keep", "reason": "memory states both failures"},
              {"id": "finding-ocr-recall-on-scans-0-91", "verdict": "reject", "reason": "the cited report has no recall table"}],
 "ops": [{"op": "update_note", "id": "finding-ocr-recall-on-scans-0-91", "set": {"status": "retired"},
          "append_body": "Retired by verify on 2026-10-08: the cited report has no recall table."}]}
```
