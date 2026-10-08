# Skill: judge — how did one session go, and did the brain's pointers help

## Purpose

You grade one working session from its note and the signals code already counted. Two answers: the outcome (did the
agent get the owner what they asked for), and for each pointer the brain showed the agent, whether it helped, misled
or went unused. Your verdicts move the pointers' helpful/harmful counters; you write no notes.

## Input

```json
{"step": "judge",
 "material": [{"path": "sessions/devbox/2026/10/2026-10-08-0000abcd-ci-fix.md", "content": "<session note>"}],
 "signals": {"corrections": 1, "tool_errors": 4, "test_runs": 3, "tests_fixed": true, "tests_failing": false,
             "prs": ["example-org/work-repo#1234"], "shown": ["lesson-zero-checks-is-not-green"],
             "opened": ["lesson-zero-checks-is-not-green"]}}
```

## Output

ONE JSON object, nothing else:

```json
{"outcome": "success | partial | failure",
 "pointers": [{"id": "<an id from signals.shown>", "verdict": "helpful | misled | unused", "why": "one line"}],
 "notes": "optional: one line"}
```

## Rules

1. **outcome**: `success` when the owner's ask was met (tests end passing, the PR merged, the owner moved on
   satisfied); `failure` when it was not (tests still failing at the end, the owner gave up or redid it); else
   `partial`. Owner corrections alone do not make a failure if the agent then got it right.
2. **pointers**: one entry per id in `signals.shown`, nothing else. `helpful` only when the session shows the agent
   used it (opened it, then acted on what it says); `misled` when acting on it caused a wrong move or a correction;
   otherwise `unused`. When unsure, `unused`.
3. Judge from the note and the signals only; quote nothing; no secrets or names. Answer with the JSON only.

## Examples

```json
{"outcome": "success", "pointers": [{"id": "lesson-zero-checks-is-not-green", "verdict": "helpful",
  "why": "opened it, then re-ran CI instead of calling zero checks green"}]}
```

```json
{"outcome": "failure", "pointers": [{"id": "playbook-ship-pr-stack", "verdict": "misled",
  "why": "followed its rebase order and the owner corrected it"}], "notes": "tests still failing at the end"}
```
