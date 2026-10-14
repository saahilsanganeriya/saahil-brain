# Brain care playbook

The daily brain-care agent reads this at the start of every run. It is the method; the run's GOAL.md is the contract.
A run that finds a better method changes this file in a commit, like any other code.

## 1. Observe

- `brain metrics --write` on the devbox and `brain peer step metrics --wait 5m` for the laptop: the last 24 h of
  dreams (runs, failed steps, units refused inside ok steps, edits, edits held by the cap and how many of them are lost, deletions, spend), model spend by workflow, the
  prompt hook (prompts, p95, abstain rate), use (sessions pulling from the brain, pointers shown, opened, ignored,
  brain tool calls through MCP and the CLI) and the newest eval numbers, each with its change since the previous
  day.
- The evals, on the devbox: `brain eval navigation`, `brain eval qa`, `brain eval ab`, `brain eval human` (the devbox
  writes the merged human report; the laptop's rows come from its last run).
- History: the last 7 snapshots in `~/brain/.brain/metrics/`, the last reports in `~/brain/work/brain-care/runs/`,
  and `~/brain/work/brain-care/EXPERIMENTS.md` (every experiment so far, kept or not).

## 2. Check yesterday's change

The newest kept experiment in EXPERIMENTS.md names the metric it should move. If the live snapshot shows it did not,
or a guard regressed (any eval number below the bar in section 5, prompt p95 over 150 ms, spend up by more than half, pointers
opened or sessions pulling falling three days running), revert it (`git revert`, push, deploy) and record
"reverted" with the numbers.

## 3. Choose one thing to improve

In this order: (a) anything broken (doctor, failed dream steps, units a dream refuses again and again, edits its cap
holds run after run, deletions, a missing record); (b) the owner-typed eval (human within 3 calls, then top-3), the closest measure of whether the
brain helps its owner; (c) use: pointers opened per shown, sessions pulling, MCP calls; (d) spend per dream; (e)
navigation and QA. Read EXPERIMENTS.md first and never retry a rejected idea without new evidence.

## 4. Hypothesis, build, measure

One general change: no item ids, no box or path special cases, never an edit to the eval sets, targets or marks.
Write down which metric it should move and by how much. Build it test-first. Measure on a shadow copy of the brain
(symlinks to the live folders, `sqlite3 .index/brain.db ".backup <shadow>/.index/brain.db"`, a copy of `.brain`, and
`BRAIN_ROOT=<shadow>`; `sessions/` as real directories), never on the live brain. Tune on DEV; run TEST once.

## 5. Keep it only if

The target improves; human TEST top-3 and within 3 calls do not drop; navigation TEST stays >= 70% top-3 and >= 80%
within 3 calls; `brain hook bench` p95 <= 150 ms; pointers on owner prompts do not grow; a dream stays <= $8.
Otherwise drop it. Either way, record it.

## 6. Record

Append to EXPERIMENTS.md: date, hypothesis, change, DEV and TEST numbers before and after, verdict (kept, dropped,
reverted), commit. A question this loop cannot answer from the brain's records (dream runs, llm_calls, lookups,
node_stats, eval reports, metrics snapshots) is itself worth fixing: adding the record is a valid improvement.
