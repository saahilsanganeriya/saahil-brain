# Skill: answer-judge — which brain file answered the owner's prompt

## Purpose

You build an evaluation set for the owner's personal knowledge base (the brain). Each item is one prompt the owner
typed to a coding agent, with the brain files the agent read or opened right after it. Decide which of those files
held the answer: the file whose content the agent needed to respond, so a good index would have pointed at it the
moment the prompt was typed. Your answers become the targets the brain is scored against.

## Input

```json
{"step": "answer",
 "items": [{"id": "human-devbox-0000abcd-4",
            "cwd": "/Users/owner/Documents/worktrees/repo/feature",
            "prompt": "<the owner's prompt, redacted>",
            "reply": "<the agent's last words before the owner's next prompt, cut short>",
            "candidates": [{"path": "work/box/STATE.md", "title": "...", "summary": "...", "excerpt": "<start of the file>"}]}]}
```

## Output

ONE JSON object, nothing else, with one entry per input item:

```json
{"answers": [{"id": "<an item id>", "files": ["<a candidate path>"]}]}
```

## Rules

1. **files** lists the candidate paths (copied exactly) that held the answer: usually one, at most three. An empty
   list means none did.
2. Pick a file when its content is what the prompt needed: the facts, state, decision, plan or result the agent then
   used (the reply often shows it). A file the agent only opened to edit, a generic orientation read (a README, an
   index), or a file unrelated to the question is not an answer.
3. Answer `[]` when the prompt needed no brain file (a command, an approval, a code question answered from the repo)
   or when no candidate carries what was asked. When unsure, `[]`: a wrong target is worse than a dropped item.
4. Judge from the given text only. Quote nothing back; no secrets or names. Answer with the JSON only.

## Examples

Input items (shortened): `h-1` asks "where did we leave the shard rollout, what is blocked?", candidates
`work/rollout/STATE.md` (current state, blockers) and `work/rollout/README.md`; `h-2` asks "rename the flag and push",
candidate `work/rollout/plans/flags.md`, and the reply shows only a rename and a push.

```json
{"answers": [{"id": "h-1", "files": ["work/rollout/STATE.md"]}, {"id": "h-2", "files": []}]}
```
