# Skill: card — one work box becomes its CARD

## Purpose

A work box is a folder of specs, plans, reports, tools and run outputs from one campaign. Its CARD is the first thing
an agent reads when it lands in that work: what the box is, where it stands, which files to open, what is next. You
write it from an inventory of the box. You do not see the files themselves, so never claim more than the inventory
shows.

## Input

One JSON object on stdin:

```json
{"step": "card",
 "hubs": [{"id": "image-lane", "summary": "the image anonymisation lane"}],
 "inventory": {
   "box": "image-gate-2026-10",
   "newest": "2026-10-06",
   "tree": ["STATE.md  2210 B  2026-10-06", "plans/SPEC.v2.md  30411 B  2026-10-04", "artifacts/  (bulk, not listed)  2026-10-05"],
   "docs": [{"path": "plans/SPEC.v2.md", "title": "Image gate spec v2", "summary": "…", "status": "live", "headings": ["Goal", "Design"]}],
   "heads": {"STATE.md": "<first 2 KB>", "README.md": "<first 2 KB>"},
   "sessions": ["2026-10-05 image-gate-f2 (PRs example-org/work-repo#3488) — sessions/devbox/2026/10/2026-10-05-0000abcd-image-gate.md"],
   "prs": ["example-org/work-repo#3488"]},
 "rejected": "only on a second try: why the first CARD was refused"}
```

## Output

ONE JSON object, nothing else:

```json
{"card": {"status": "live | paused | done", "summary": "≤ 160 chars", "hubs": ["1–3 ids from the input hubs"],
          "aliases": ["names the box answers to"]},
 "body": "## What it is\n…\n\n## Where it stands\n…\n\n## Key files\n- `plans/SPEC.v2.md` — …\n\n## Next\n…\n\n## Related\n…"}
```

Code adds `id: card-<box>`, `type: card`, `scope: work` and `as_of: <newest>`.

## Rules

1. **status**: `done` when STATE or the sessions say the work finished, landed or merged and nothing is open; `paused`
   when it stopped without finishing (newest file weeks old, open items left); else `live`.
2. **summary** says what the box is *for* and its state in one line, ≤ 160 chars.
3. **body**, whole file ≤ 3 KB, these five H2 sections in this order:
   - `## What it is` — 2–3 lines.
   - `## Where it stands` — as of the newest date; cite STATE's own words; PRs by id.
   - `## Key files` — ≤ 7 bullets, each a box-relative path in backticks that is in the tree, then a few words.
     Name the live version of each spec or plan (the highest version, `status: live`, not under `.archive/` or
     `log/`), never a superseded copy. Never list a bulk folder's contents.
   - `## Next` — open items, or "nothing open" for a done box.
   - `## Related` — other boxes or hubs by name, or "none".
4. **hubs**: 1–3 ids, only from the input `hubs`.
5. **aliases**: 2–5 short names an owner would type for this work (the campaign's nickname, its ticket, its lane).
6. When `rejected` is present, fix exactly what it names and keep the rest.
7. No secrets, no customer data, no personal names. Answer with the JSON only.

## Examples

A finished campaign:

```json
{"card": {"status": "done", "summary": "Zero-row parquet sweep: found and fixed empty-table outputs in the tabular lane; merged",
          "hubs": ["tabular-lane"], "aliases": ["zero row sweep", "empty parquet"]},
 "body": "## What it is\nA sweep for parquet outputs with zero rows in the tabular lane, and the fix that stops them.\n\n## Where it stands\nAs of 2026-10-02: done. STATE says \"fix merged, sweep clean\" (PR example-org/work-repo#1234).\n\n## Key files\n- `STATE.md` — the final state\n- `reports/sweep.md` — what the sweep found\n\n## Next\nNothing open.\n\n## Related\nHub tabular-lane."}
```

A live box with a versioned spec:

```json
{"card": {"status": "live", "summary": "Image gate redesign: spec v2 agreed, implementation half done, gate run pending",
          "hubs": ["image-lane", "anonymization-pipeline"], "aliases": ["image gate", "gate v2"]},
 "body": "## What it is\nThe redesign of the image lane's quality gate.\n\n## Where it stands\nAs of 2026-10-06: spec v2 agreed; two of four PRs open.\n\n## Key files\n- `plans/SPEC.v2.md` — the live spec\n- `STATE.md` — where it stands\n- `reports/gate-run.md` — last gate run\n\n## Next\nLand the remaining PRs, then rerun the gate.\n\n## Related\nBox image-lane-audit-2026-10-06."}
```
