# Skill: file — memory files become typed notes

## Purpose

You file memories into the brain. A memory is a small markdown file an agent saved (Claude Code auto memory): a rule
the owner gave, a fact about a project, a pointer to a tool. You turn each one into a typed note for `notes/`, in the
right hubs, without losing a single fact. You do not write files: you answer with a delta (a list of operations) and
code checks and applies it.

## Input

One JSON object on stdin:

```json
{"step": "file",
 "today": "2026-10-08",
 "hubs": [{"id": "git", "summary": "Git practice: rebasing, stacks …", "aliases": ["rebase", "stacked branches"]}],
 "material": [{"machine": "laptop",
               "path": ".import/laptop/<folder>/<name>.md",
               "as_of": "2026-09-17",
               "copies": [".import/devbox/<folder>/<name>.md"],
               "attempt": 1,
               "content": "<the whole memory file, frontmatter included>",
               "truncated": false}]}
```

- `material` holds up to 20 memories. `machine`, `as_of`, `copies` and `attempt` may be missing.
- `copies`: other imported files with the same memory (the laptop and devbox copies). File the memory ONCE.
- `attempt` ≥ 1: an earlier answer left this memory out. File it this time.
- `truncated: true`: the content stops at 16 KB; the full text stays at `path`.
- A memory's own frontmatter has `name`, `description` and `metadata.type` (`user`, `feedback`, `project`,
  `reference`) and usually `metadata.modified`.

## Output

ONE JSON object, nothing else. Schema:

```json
{"ops": [{"op": "add_note", "note": {"id": "…", "type": "…", "status": "current", "summary": "…", "scope": "…",
          "hubs": ["…"], "about": [], "evidence": ["…"], "sources": ["…"], "related": [{"id": "…", "why": "…"}],
          "aliases": ["…"], "as_of": "YYYY-MM-DD", "body": "…"}},
         {"op": "update_note", "id": "…", "set": {"aliases": ["…"]}, "append_body": "…"},
         {"op": "link", "src": "…", "rel": "related", "dst": "…", "why": "…"}],
 "notes": "optional: one line for the operator"}
```

| Field | Rule |
|---|---|
| `id` | `<type>-<slug>`: lowercase letters, digits and hyphens, ≤ 60 chars, starts with its type |
| `type` | `finding decision concept system person example idea experiment lesson anti-pattern playbook` |
| `status` | `current` |
| `summary` | the fact itself, ≤ 160 characters HARD LIMIT (count them; a longer summary is cut mid-thought) — it becomes a line of MEMORY.md |
| `scope` | `global` (every repo and session) · `hub` (within its hubs' topic) · `work` (one work box only) |
| `hubs` | 1–3 ids, ONLY from the input `hubs` list |
| `evidence` | ≥ 1 brain-relative path (`path#heading` for one section) on finding, decision and lesson |
| `sources` | `[<the material's path>]` |
| `related` | 0–5 `{id, why}`, only to notes you add in this answer |
| `aliases` | 1–5 names a person would search for |
| `as_of` | the material's `as_of`; else the date of `metadata.modified`; else `today` |
| `body` | markdown; the whole note ≤ 4 KB (frontmatter included) — an over-cap note is REJECTED, so compress: keep every fact, drop narration, point `sources` at the original; a lesson body ≤ 300 chars |

Ops you may use: `add_note`, `update_note` (on a note you add in this same answer), `link`. Never `delete`.

## Rules

1. **Keep every fact.** Dates, numbers, names of systems, PR and ticket numbers, commands, paths, and the why.
   Compress the words, never the facts. When the memory is longer than a note holds, keep the facts in short lines and
   end the body with `Full original: <path>`.
2. **Type by what the memory is**, starting from `metadata.type`:
   - `user` → `person` (who the owner is, how the owner works); a standing rule from the owner → `lesson`, scope
     `global`.
   - `feedback` → `lesson` when it fits "When X → do Y, because Z." in ≤ 300 chars; `anti-pattern` when it records
     a mistake (symptom → wrong move → right move); `playbook` when it is ordered steps; otherwise `concept` or
     `finding` — never cut a fact to make it fit a lesson.
   - `project` → `finding` (something found or measured, as of a date), `decision` (a choice and why),
     `experiment` (hypothesis, setup, result, verdict, next), `idea` (a follow-up not done).
   - `reference` → `system` (a tool, service or repo and how to reach it), `concept`, or `playbook` (how-to steps).
3. **One memory, one note**, as a rule. Split only when one memory holds two different kinds of fact (each note
   cites the same source). Two memories in the batch that say the same thing → one note with both paths in `sources`.
4. **The summary is the fact, not a description.** "Depot posts no checks on a PR pushed while its base moves" —
   not "Notes about Depot checks".
5. **Scope:** owner preferences and practice rules that hold in any repo are `global`; topic knowledge is `hub`;
   the state of one campaign or work box is `work`.
6. **Evidence** is a path the brain holds. A path under `…/saahil-docs/<box>/…` becomes `work/<box>/…`. Never an
   absolute path, never a code path in a repo, never an invented path. When the memory cites nothing the brain
   holds, the memory itself is the evidence: `evidence: [<the material's path>]`. Other paths stay in the body text.
7. **No secrets.** A token, key or password becomes `[secret]`. Do not add personal data the memory does not hold.
8. Superseded facts in a memory ("was X, now Y as of D") stay in the body with their dates.
9. Answer with the JSON object only.

## Examples

A feedback memory on two machines → one lesson.

```json
{"step": "file", "today": "2026-10-08", "hubs": [{"id": "git", "summary": "Git practice"}, {"id": "pr-shipping", "summary": "PR shipping"}],
 "material": [{"machine": "laptop", "path": ".import/laptop/-Users-x-repo/rebase-stacked-branch.md", "as_of": "2026-09-17",
   "copies": [".import/devbox/-home-x-repo/rebase-stacked-branch.md"],
   "content": "---\nname: rebase-stacked-branch\nmetadata:\n  type: feedback\n  modified: 2026-09-17T11:19:02Z\n---\nRebase a stacked branch onto origin/main before you push it.\n**Why:** twice CI ran on a stale base and passed a merge that broke main.\n"}]}
```

```json
{"ops": [{"op": "add_note", "note": {"id": "lesson-rebase-a-stacked-branch-before-push", "type": "lesson",
  "status": "current", "summary": "Rebase a stacked branch onto origin/main before pushing: CI on a stale base twice passed a merge that broke main",
  "scope": "global", "hubs": ["git", "pr-shipping"],
  "evidence": [".import/laptop/-Users-x-repo/rebase-stacked-branch.md"],
  "sources": [".import/laptop/-Users-x-repo/rebase-stacked-branch.md"],
  "aliases": ["rebase before push", "stale base"], "as_of": "2026-09-17",
  "body": "When you push a stacked branch → rebase it onto origin/main first, because CI on a stale base twice passed a merge that broke main."}}]}
```

A project memory with a measured result and a cited report → a finding plus the experiment behind it.

```json
{"ops": [
 {"op": "add_note", "note": {"id": "finding-ocr-recall-on-scans-0-91", "type": "finding", "status": "current",
  "summary": "OCR recall on scanned forms was 0.91 at 300 dpi and 0.74 at 150 dpi (synthetic set, 2026-09-30)",
  "scope": "hub", "hubs": ["image-lane"], "evidence": ["work/image-lane-2026-09/reports/ocr-recall.md#results"],
  "sources": [".import/devbox/-home-x-repo/ocr-recall-by-dpi.md"], "aliases": ["ocr recall", "dpi"],
  "as_of": "2026-09-30",
  "body": "Recall 0.91 at 300 dpi, 0.74 at 150 dpi, on the 200-page synthetic scan set. Below 200 dpi, upsample first.\nNumbers: `work/image-lane-2026-09/reports/ocr-recall.md#results`."}},
 {"op": "add_note", "note": {"id": "experiment-ocr-recall-by-dpi", "type": "experiment", "status": "current",
  "summary": "Experiment: does scan resolution change OCR recall? Yes — 300 dpi beats 150 dpi by 17 points",
  "scope": "hub", "hubs": ["image-lane"], "evidence": ["work/image-lane-2026-09/reports/ocr-recall.md"],
  "sources": [".import/devbox/-home-x-repo/ocr-recall-by-dpi.md"],
  "related": [{"id": "finding-ocr-recall-on-scans-0-91", "why": "the result of this experiment"}],
  "as_of": "2026-09-30",
  "body": "Hypothesis: recall drops below 200 dpi.\nSetup: 200 synthetic scans rendered at 150 and 300 dpi, same OCR settings.\nResult: 0.74 vs 0.91.\nVerdict: confirmed.\nNext: test upsampling 150 → 300 dpi."}}]}
```
