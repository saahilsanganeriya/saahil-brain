# Skill: hubs — keep the hub pages useful

## Purpose

A hub page is the entry point to one topic (image lane, git, CI …). You keep each hub's page current: its "start
here" list, its open questions, and — rarely — a proposal for a new hub, a split, or a retirement. The page's
member list is a query and is never kept by hand. You answer with a delta; code checks the caps and applies it.

## Input

```json
{"step": "curate.hubs",
 "today": "2026-10-08",
 "hubs": [{"id": "image-lane", "summary": "…", "aliases": ["ocr"]}],
 "pages": [{"path": "hubs/image-lane.md", "content": "<the whole hub page>"}],
 "members": {"image-lane": [{"id": "finding-x", "type": "finding", "status": "current", "summary": "…",
                              "as_of": "2026-10-01", "works": ["image-lane-2026-10"]}]},
 "proposed": [{"path": "notes/…", "action": "write", "content": "<a note this run wrote>"}],
 "orphans": [{"id": "idea-y", "summary": "…", "words": ["…"]}]}
```

`members` lists each hub's live notes (newest first, capped); `orphans` are notes whose hubs fit poorly.

## Output

ONE JSON object, nothing else:

```json
{"ops": [{"op": "update_note", "id": "<hub id>", "replace_body": "<the new page body>"},
         {"op": "update_note", "id": "<hub id>", "set": {"aliases": ["…"], "summary": "…"}},
         {"op": "propose_hub", "note": {"id": "…", "type": "hub", "status": "proposed", "summary": "…",
          "aliases": ["…"], "broader": ["…"], "body": "…"}, "members": ["…"], "why": "…"},
         {"op": "link", "src": "<note id>", "rel": "hubs", "dst": "<hub id>"}],
 "notes": "optional: one line"}
```

## Rules

1. **Body layout** (keep this order and these headings): `# Title`, the scope paragraph unchanged, `## Start here`
   (≤ 7 lines: `- [id](../notes/id.md) — why read it first`), `## Members` (the query sentence, unchanged),
   `## Child hubs` (only if any), `## Open questions` (≤ 5 lines). The page stays ≤ 200 lines.
2. **Start here** = the notes a newcomer needs first: current decisions, then the top lessons and playbooks, then
   key findings. Only ids from `members`. Superseded or retired notes never appear.
3. **Open questions** come from members that say something is unknown, contested or untested. Drop an answered one
   and name the note that answered it in `notes`.
4. **Propose a hub** only when ≥ 8 live notes from ≥ 2 works share a topic that no hub covers; list them in
   `members`, give `broader` (1–2 existing hubs, depth ≤ 3) and a `why`. Never create a live hub yourself.
5. **Split** a hub above ~50 live members with two clear sub-topics: propose the children (rule 4). **Retire** a hub
   after 60 days with < 3 live members: `set: {"status": "retired"}`, with the reason in `notes`. Never delete.
6. Re-home an orphan with a `link` op (`rel: "hubs"`) when a hub clearly fits; a note holds 1–3 hubs.
7. Change a hub's summary or aliases only when its members show the old ones are wrong or missing.
8. Answer with the JSON only.

## Examples

```json
{"ops": [{"op": "update_note", "id": "ci-depot", "replace_body": "# CI and Depot\n\nContinuous integration on Depot: lane shapes and walls, missing or late checks, receipts, flaky tests, image builds.\n\n## Start here\n\n- [anti-pattern-reading-green-from-zero-checks](../notes/anti-pattern-reading-green-from-zero-checks.md) — zero checks is not green\n- [finding-go-test-lane-wall-is-the-cache](../notes/finding-go-test-lane-wall-is-the-cache.md) — where the lane's time goes\n\n## Members\n\nA query, never a hand-kept list: every live note whose `hubs` includes `ci-depot`.\n\n## Open questions\n\n- Does a re-run on the same SHA reuse the test cache?\n"}]}
```

```json
{"ops": [{"op": "propose_hub", "note": {"id": "temporal-practice", "type": "hub", "status": "proposed",
  "summary": "Temporal practice: workflow determinism, versioning, workers and schedules across projects",
  "aliases": ["temporal", "workflows", "activities"], "broader": ["index"],
  "body": "# Temporal practice\n\nHow to write and run Temporal workflows safely in any project.\n\n## Start here\n\n## Members\n\nA query, never a hand-kept list: every live note whose `hubs` includes `temporal-practice`.\n\n## Open questions\n"},
  "members": ["lesson-a", "lesson-b", "finding-c", "finding-d", "anti-pattern-e", "decision-f", "system-g", "playbook-h"],
  "why": "8 live notes from 3 works sit in anonymization-pipeline and brain only because no hub covers Temporal"}]}
```
