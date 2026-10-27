# Skill: merge — one rule written as several notes, and global rules that disagree

## Purpose

You audit the global baseline of a personal knowledge base for one engineer (the owner). Filing turns memories into
notes about one to one, so the same rule often lands as several notes, and an old rule can contradict a newer one.
You work in one of two stages, named by `stage` in the input. **find**: name the duplicate groups, the conflicting
pairs and the stale notes. **bodies**: for one duplicate group, write the body of the note to keep so it holds every
distinct fact of the group; a group of one note (no `supersede`) is a note too full for the narrowing line code will
append, so write its body shorter with every fact. Code applies your answer; nothing is ever deleted.

## Input

Stage find — every CURRENT global note: id, type, `as_of` (when it was true), `project` (the repo its source memory
came from; empty = not a project memory) and a one-line summary:

```json
{"step": "curate.merge", "stage": "find",
 "notes": [{"id": "lesson-stamp-log-entries-from-date-u", "type": "lesson", "as_of": "2026-09-23",
            "project": "work-repo", "summary": "Stamp every decision-log entry from date -u …"}]}
```

Stage bodies — one group to merge (one call per group), with the whole text of every note in it and the cap the kept
body must fit:

```json
{"step": "curate.merge", "stage": "bodies",
 "groups": [{"keep": "anti-pattern-x", "supersede": ["anti-pattern-y"], "type": "anti-pattern", "cap_chars": 3400,
             "notes": [{"id": "anti-pattern-x", "content": "<whole file>"}, {"id": "anti-pattern-y", "content": "…"}]}]}
```

## Output

ONE JSON object, nothing else. Stage find:

```json
{"duplicates": [{"keep": "<id>", "supersede": ["<id>"], "why": "one line"}],
 "conflicts": [{"newer": "<id>", "older": "<id>", "resolution": "supersede | narrow | owner", "why": "one line"}],
 "stale": [{"id": "<id>", "replaced_by": "<id>", "why": "one line"}]}
```

Stage bodies:

```json
{"bodies": [{"keep": "<id>", "body": "<the kept note's body, no frontmatter>"}]}
```

## Rules

1. Precision over recall. Every id you return must appear in the input exactly. Do not report notes that are merely
   related or complementary.
2. **duplicates**: notes that state the same rule or fact. Paraphrases count; a playbook that only restates a lesson's
   rule counts. Keep ONE: the newest `as_of`; on a tie, the most specific and complete. List the others to supersede.
3. **conflicts**: pairs whose rules contradict — one says do X, the other says do not, or they give different values
   for the same setting. Resolve by the newest owner statement (`as_of`): `supersede` when the newer fully replaces
   the older; `narrow` when the older is still right inside its own project or situation (say which in `why`, in a
   few words: the code writes "Narrowed <date> by <newer>: <why>" into the older note, or into its link to the newer
   note when the body is full); `owner` when the dates are equal or missing, or you cannot tell which situation each
   covers. Code sends a supersede or narrow between equal or missing dates to the owner anyway.
4. **stale**: notes that name a tool, path or practice that another note in the input shows replaced (one note says X
   was replaced by Y). Only when that other note proves it; `replaced_by` is that note.
5. **bodies**: hold every distinct fact of the whole group — dates, incident names, numbers, commands, the mechanism,
   the fix, the why. Compress the words, never the facts. Keep the kept note's style: a lesson reads "When X → do Y,
   because Z"; other types may use short labelled lines (Symptom / Wrong move / Right move / Why). Stay within
   `cap_chars`; a body over the cap is refused and the group is not merged. Return the body only, no frontmatter, with
   its `keep` id. A group of one: the same rules for that note alone; code appends the narrowing line after your body,
   so do not write it.
6. No secrets, no personal data beyond what the notes already hold. Answer with the JSON only.

## Examples

Stage find:

```json
{"duplicates": [{"keep": "anti-pattern-write-probe-in-bucket-you-cannot-clean",
                 "supersede": ["anti-pattern-write-probe-in-bucket-without-delete"],
                 "why": "same rule (check put and delete rights before a write probe); kept note names S3 and DeleteObject"}],
 "conflicts": [{"newer": "decision-haiku-allowed-for-bulk-work", "older": "concept-agents-run-opus-only",
                "resolution": "narrow", "why": "Opus-only still holds for judgment, design, review and code"},
               {"newer": "decision-owner-questions-on-by-default", "older": "playbook-driver-settles-design-gaps",
                "resolution": "owner", "why": "same date; unclear whether questions-on covers the driver's gaps"}],
 "stale": []}
```

Stage bodies:

```json
{"bodies": [{"keep": "anti-pattern-write-probe-in-bucket-you-cannot-clean",
  "body": "Symptom: you need to know whether your role can write to a production bucket.\nWrong move: put a test object (_probe.txt) to see if the write works. The role could put but not delete (DeleteObject denied), so the object stayed in a prod raw bucket until the owner found someone with DeleteObject.\nRight move: check delete rights as well as put rights before writing anything (policy simulation, or a prefix you know you can clean). Record any probe object you could not remove in the cleanup list straight away.\n"}]}
```
