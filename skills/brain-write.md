# Skill: brain-write — write a doc the brain can use

Use this for any new spec, plan, report, review, experiment, handoff or readme under `~/brain/work/<box>/`.
Anything is accepted; this format makes it findable, readable one section at a time, and linked to its results.

**Start with this header:**

```yaml
---
type: spec            # spec | plan | report | review | experiment | handoff | readme
status: live          # live | draft | superseded | done
summary: one line, ≤ 160 chars — the doc's point, not its topic
hubs: [image-lane]    # 1–3 hub ids: the file names in ~/brain/hubs/
---
```

**Then:**

- One `# Title`, then `## Sections` at H2. Keep each section ≤ ~20 KB: agents read one section, chosen by its
  summary. Split a section that grows past that.
- Link to docs and results by **relative path** (`../reports/run-7.md#results`, `.data/run-7/result.json`), so the
  links become real edges. No absolute paths, no "see the doc in the other folder".
- Put numbers next to the file that holds them, with a date ("as of 2026-10-08").
- A new version replaces the old one: name it, set the old one's `status: superseded`, and keep the old file.
- `STATE.md` (where the work stands) stays ≤ 8 KB and is overwritten, never appended to. History goes to
  `log/YYYY-MM-DD.md`. `CARD.md` is written by the dream.
- Bulk outputs (data, runs, binaries) go under `.data/`, not next to the docs.

The write guard warns when a new doc lacks the header or has a section over 20 KB, and blocks a STATE.md over 8 KB.
