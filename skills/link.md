# Skill: link — connect new and orphan notes to the graph

## Purpose

A note nobody links to is never found. You read the notes this run wrote (and orphans the run hands you) and add the
edges they lack: what each is `about`, which other notes are `related` and why, and whether it sits in the right
`hubs`. You add edges only; you never create or rewrite notes.

## Input

```json
{"step": "curate.link",
 "today": "2026-10-08",
 "hubs": [{"id": "git", "summary": "Git practice …"}],
 "proposed": [{"path": "notes/lesson-x.md", "action": "write", "content": "<the whole note>"}],
 "candidates": [{"id": "system-depot", "type": "system", "summary": "…", "hubs": ["ci-depot"]}]}
```

`proposed` holds the notes to link. `candidates` are existing notes that share a hub or a word with them — the only
ids you may link to besides the proposed notes themselves.

## Output

ONE JSON object, nothing else:

```json
{"ops": [{"op": "link", "src": "…", "rel": "about|related|hubs", "dst": "…", "why": "…"},
         {"op": "update_note", "id": "…", "set": {"aliases": ["…"]}}],
 "notes": "optional: one line"}
```

- `rel: "about"` — `dst` is the system, concept, person or work the note is about (0–5 per note).
- `rel: "related"` — a weak link; `why` is one line and required (0–5 per note).
- `rel: "hubs"` — `dst` is a hub id from the input; a note sits in 1–3 hubs.
- `update_note` may only add `aliases` here. Never `add_note`, never `delete`.

## Rules

1. Link only to ids in `proposed` or `candidates`, and never a note to itself.
2. Prefer one precise `about` over several `related`. `related` needs a reason a reader would act on: "same OCR
   budget", "the fix for this symptom" — not "similar topic".
3. Every proposed note must end with ≥ 1 incoming or outgoing edge besides its hubs. If no candidate fits, say so in
   `notes` instead of forcing a link.
4. Add a hub only when the note is clearly about that hub's scope and has fewer than 3 hubs.
5. Add an alias when the note's subject has a common name the note does not use (a tool's short name, an acronym).
6. Answer with the JSON only.

## Examples

```json
{"ops": [{"op": "link", "src": "lesson-rebase-a-stacked-branch-before-push", "rel": "about", "dst": "system-gh-stack"},
         {"op": "link", "src": "lesson-rebase-a-stacked-branch-before-push", "rel": "related",
          "dst": "anti-pattern-reading-green-from-zero-checks", "why": "a stale base is one cause of missing checks"}]}
```

```json
{"ops": [{"op": "link", "src": "finding-batch-64-halves-gate-wall-time", "rel": "hubs", "dst": "eks-runs"},
         {"op": "update_note", "id": "finding-batch-64-halves-gate-wall-time", "set": {"aliases": ["gate batch"]}}],
 "notes": "experiment-gate-batch-size already links to the finding; no candidate fits it better"}
```
