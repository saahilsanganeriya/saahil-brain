# Skill: distil — a session becomes lessons, anti-patterns, playbooks, experiments and findings

## Purpose

You learn from what happened in a working session. A session note records the owner's prompts, what the agent did,
and the files it touched and produced. You find what is worth knowing next time — a lesson, a mistake not to repeat,
a repeatable procedure, an experiment and its result, a finding with its evidence — and answer with a delta. Most
sessions teach nothing new; then you answer with no ops.

## Input

One JSON object on stdin:

```json
{"step": "learn",
 "today": "2026-10-08",
 "hubs": [{"id": "ci-depot", "summary": "CI on Depot …"}],
 "material": [{"path": "sessions/devbox/2026/10/2026-10-08-0000abcd-ci-fix.md", "content": "<session note>", "truncated": false},
              {"path": "work/<box>/reports/result.md", "content": "<an artifact the session produced>"}],
 "proposed": [{"path": "notes/…", "action": "write", "content": "<a note an earlier step of this run wrote>"}]}
```

A session note's frontmatter lists `touched`, `produced`, `prs` and `corrections` (how many owner prompts corrected
the agent). Its body has `## Owner prompts`, `## What it did` and `## Files`. `proposed` shows notes already written
this run: do not add them again.

## Output

ONE JSON object, nothing else:

```json
{"ops": [{"op": "add_note", "note": {"id": "…", "type": "…", "status": "…", "summary": "…", "scope": "…", "hubs": ["…"],
          "evidence": ["…"], "sources": ["…"], "related": [], "aliases": ["…"], "as_of": "YYYY-MM-DD", "body": "…"}},
         {"op": "update_note", "id": "…", "append_body": "…", "set": {"evidence": ["…"]}},
         {"op": "supersede", "old": "…", "new": "…"}],
 "notes": "optional: one line"}
```

Note fields: `id` = `<type>-<slug>` (lowercase, hyphens, ≤ 60 chars, starts with its type) · `type` · `status`
(`current`, or `proposed` for a candidate) · `summary` = the lesson or fact itself, ≤ 160 chars · `scope`
(`global` · `hub` · `work`) · `hubs` 1–3 ids from the input · `evidence` ≥ 1 path from the input (the session note,
`#Owner prompts` for a correction, or an artifact) · `sources` = `[<session path>]` · `as_of` = the session's date.
A note is ≤ 4 KB; a lesson body ≤ 300 chars. Never `delete`.

## Rules

1. **Corrections first.** Each owner prompt that corrected the agent is the strongest signal: write an
   `anti-pattern` — symptom → wrong move → right move — with evidence `<session path>#Owner prompts`.
2. **Lessons are abstract**: "When X → do Y, because Z." No pasted traces, no session-specific ids in the rule;
   the evidence carries the specifics. A lesson that repeats an existing one adds evidence to it (`update_note`
   with `set: {"evidence": [...]}`) instead of a new note.
3. **Playbooks** are ordered steps with variables (`<pr>`, `<box>`). Only when the session shows a procedure that
   worked end to end; mark it `status: proposed` unless `proposed` already holds two like it.
4. **Experiments**: when the session ran something to answer a question, write an `experiment` note — hypothesis ·
   setup (script, data, parameters) · result → the artifact path · verdict · next — and a `finding` for the result
   (`as_of`, evidence → the result file). A dead end is an `anti-pattern`; a follow-up not done is an `idea`.
5. **Numbers point at artifacts.** Write the number with its `as_of` and cite the file that holds it.
6. At most 6 notes per session. Nothing worth keeping → `{"ops": []}`.
7. A new note that replaces an older one in `proposed`: `supersede` it, never delete it.
8. No secrets, no customer data beyond what the evidence path already holds. Answer with the JSON only.

## Examples

A session where the owner corrected the agent twice about CI checks:

```json
{"ops": [{"op": "add_note", "note": {"id": "anti-pattern-reading-green-from-zero-checks", "type": "anti-pattern",
  "status": "current", "summary": "Zero CI checks on a PR is a delivery gap, not a pass: re-push or re-run before calling it green",
  "scope": "global", "hubs": ["ci-depot", "pr-shipping"],
  "evidence": ["sessions/devbox/2026/10/2026-10-08-0000abcd-ci-fix.md#Owner prompts"],
  "sources": ["sessions/devbox/2026/10/2026-10-08-0000abcd-ci-fix.md"], "aliases": ["no checks", "zero checks"],
  "as_of": "2026-10-08",
  "body": "Symptom: the PR shows no checks after a push.\nWrong move: report it as green and flip it to ready.\nRight move: treat zero checks as not delivered; push again or re-run the workflow, then read the check list."}}]}
```

A session that benchmarked two batch sizes and kept the result in a report:

```json
{"ops": [
 {"op": "add_note", "note": {"id": "finding-batch-64-halves-gate-wall-time", "type": "finding", "status": "current",
  "summary": "Gate wall time fell from 42 to 21 minutes with batch size 64 instead of 16 (synthetic 10k-row set, 2026-10-08)",
  "scope": "hub", "hubs": ["anonymization-pipeline"], "evidence": ["work/gate-speed-2026-10/reports/batch-size.md#results"],
  "sources": ["sessions/devbox/2026/10/2026-10-08-0000beef-batch-size.md"], "aliases": ["batch size", "gate speed"],
  "as_of": "2026-10-08", "body": "42 → 21 minutes, memory peak 3.1 → 5.8 GB. Numbers: `work/gate-speed-2026-10/reports/batch-size.md#results`."}},
 {"op": "add_note", "note": {"id": "experiment-gate-batch-size", "type": "experiment", "status": "current",
  "summary": "Experiment: does a bigger gate batch cut wall time without running out of memory? Yes at 64; 128 untested",
  "scope": "hub", "hubs": ["anonymization-pipeline"], "evidence": ["work/gate-speed-2026-10/reports/batch-size.md"],
  "sources": ["sessions/devbox/2026/10/2026-10-08-0000beef-batch-size.md"],
  "related": [{"id": "finding-batch-64-halves-gate-wall-time", "why": "its result"}], "as_of": "2026-10-08",
  "body": "Hypothesis: batch 64 halves wall time.\nSetup: `tools/bench_gate.py --batch 16|64`, synthetic 10k rows.\nResult: 42 → 21 min.\nVerdict: confirmed.\nNext: try 128 with a memory cap."}}]}
```
