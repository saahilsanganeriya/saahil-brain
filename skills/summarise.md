# Skill: summarise — one line per doc section or session

## Purpose

Agents read big documents one section at a time, choosing by a one-line summary. You write those lines: one for the
whole document and one per section (its H2, or H3 when a doc has fewer than two H2s), or one for a session note.
Each line says what the text *states*, so a reader can decide without opening it. You change no notes.

## Input

```json
{"step": "summarise",
 "material": [{"path": "work/<box>/plans/SPEC.md", "content": "<the doc, up to 16 KB>", "truncated": true}]}
```

`truncated: true` means only the first 16 KB arrived: summarise the sections you can see, and mark the doc line
with "(first part)".

## Output

ONE JSON object, nothing else:

```json
{"ops": [],
 "summaries": [{"path": "<material path>", "heading": "", "summary": "<the whole doc in one line>"},
               {"path": "<material path>", "heading": "<exact heading text>", "summary": "<that section in one line>"}],
 "notes": "optional: one line"}
```

`heading` is `""` for the whole document, else the heading text exactly as written (without the `#` marks).
`ops` is always empty for this skill.

## Rules

1. Each summary ≤ 160 characters, one line, no markdown.
2. **State the content, not the container.** "Rewrite keeps a per-build drain floor; global floors were removed" —
   not "This section describes the drain floor".
3. Keep the specific: numbers with their units, names of systems, the decision taken, the status (draft, live, done).
4. A section that is only a list of links or a table of contents: "Index of <what>".
5. Never invent: if a section is unclear or cut off, summarise what is there.
6. Answer with the JSON only.

## Examples

```json
{"ops": [], "summaries": [
 {"path": "work/gate-speed-2026-10/reports/batch-size.md", "heading": "", "summary": "Batch 64 halves gate wall time (42 → 21 min) at 5.8 GB peak; 128 untested"},
 {"path": "work/gate-speed-2026-10/reports/batch-size.md", "heading": "Results", "summary": "Batch 16: 42 min, 3.1 GB; batch 64: 21 min, 5.8 GB on the synthetic 10k-row set"}]}
```

```json
{"ops": [], "summaries": [
 {"path": "sessions/devbox/2026/10/2026-10-08-0000abcd-ci-fix.md", "heading": "", "summary": "Fixed a PR that showed zero CI checks by re-pushing onto a fresh base; owner corrected a premature ready flip"}]}
```
