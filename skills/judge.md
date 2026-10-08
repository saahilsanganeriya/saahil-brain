# Skill: judge — how did one session go, and did the brain's pointers help

## Purpose

You grade one working session from its note and the signals code already counted. Two answers: the outcome (did the
agent get the owner what they asked for), and, for each pointer the brain showed the agent that clearly helped or
misled, one counter op with a one-line why. Your ops move the notes' helpful/harmful counters, which rank the notes
for later sessions; you write no notes and edit no text.

## Input

```json
{"step": "judge",
 "material": [{"path": "sessions/devbox/2026/10/2026-10-08-0000abcd-ci-fix.md", "content": "<session note>"}],
 "signals": {"corrections": 1, "tool_errors": 4, "test_runs": 3, "tests_fixed": true, "tests_failing": false,
             "prs": ["example-org/work-repo#1234"], "shown": ["lesson-zero-checks-is-not-green"],
             "opened": ["lesson-zero-checks-is-not-green"]},
 "pointers": [{"id": "lesson-zero-checks-is-not-green", "summary": "Zero CI checks on a PR is a delivery gap, not a pass"}]}
```

`signals.shown` are the pointers the brain showed; `signals.opened` the ones the agent then read (a file tool, a
shell read such as cat or sed, or brain_show). `pointers` gives what each shown pointer said.

## Output

ONE JSON object, nothing else:

```json
{"outcome": "success | partial | failure",
 "ops": [{"op": "counter", "id": "<an id from signals.shown>", "field": "helpful | harmful", "delta": 1, "why": "one line"}, …],
 "notes": "optional: one line"}
```

## Rules

1. **outcome**: `success` when the owner's ask was met (tests end passing, the PR merged, the owner moved on
   satisfied); `failure` when it was not (tests still failing at the end, the owner gave up or redid it); else
   `partial`. Owner corrections alone do not make a failure if the agent then got it right.
2. **ops**: at most one per id, and only ids in `signals.shown`. `helpful` when the session shows the agent used the
   pointer: it opened it (in `signals.opened`) and acted on it, or did what its summary says where that mattered.
   `harmful` when acting on it caused a wrong move or a correction. A pointer that went unused gets no op. When
   unsure, no op.
3. **why**: one line saying what the agent did with the pointer, so the owner can audit the counter.
4. Judge from the note, the signals and the pointers only; quote nothing; no secrets or names. Answer with the JSON
   only.

## Examples

```json
{"outcome": "success", "ops": [{"op": "counter", "id": "lesson-zero-checks-is-not-green", "field": "helpful",
  "delta": 1, "why": "opened it, then re-ran CI instead of calling zero checks green"}]}
```

```json
{"outcome": "failure", "ops": [{"op": "counter", "id": "playbook-ship-pr-stack", "field": "harmful", "delta": 1,
  "why": "followed its rebase order and the owner corrected it"}], "notes": "tests still failing at the end"}
```

```json
{"outcome": "partial", "ops": []}
```
