GOAL: Daily brain care {{DATE}}: check that Saahil's brain works on both machines, fix what is broken in the brain's code or state, report, and wind down. You are {{NAME}}, an Orca session on the devbox in a fresh worktree of saahil-brain off main (this folder). Read ~/brain/BRAIN.md and this repo's FORMAT.md first.

CHECK (paste each command and its result into the report):
1. brain doctor (devbox); brain peer step doctor --wait 10m (laptop, through the bridge).
2. brain dream status: every dream of the last 24 h completed, 0 deletions; any failed step in ~/brain/.brain/dream-runs/ of the last 24 h.
3. systemctl --user is-active brain-temporal brain-worker; the brain-dream-hourly schedule is not paused.
4. brain peer step sync --wait 20m; no unhandled conflict copies.
5. brain hook bench (p95 <= 150 ms); brain search on a hub's name answers with the hub.
6. The newest report in ~/brain/work/brain-care/runs/: what it left open.

FIX each failure at its root. A code defect: a failing test first, then the fix; check the test by putting the defect back; go test -p 4 ./... green; commit (end each message with: Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>); git fetch -q origin && git rebase origin/main && git push origin HEAD:main; make publish; brain install; brain peer step update --wait 5m; brain peer step install --wait 10m; re-run the failed check. A state problem (stale index, conflict copies, a wrong note): the brain's own commands (brain index, brain sync --archive-conflicts, brain apply-delta). Anything only the owner can do (a sign-in, a decision): list it in the report; do not wait for it.

LIMITS: about 60 minutes; at most $5 of model calls. Never delete brain data (move it into ~/brain/.archive/), never push brain data, never change a company repo (work-repo or any other), never read .env files, kill only your own processes and only by PID, never call AskUserQuestion (decide by BRAIN.md and ~/brain/work/brain-redesign-2026-10-07/plans/DESIGN.md, and log each decision in the report), start no other agents or workers.

REPORT: ~/brain/work/brain-care/runs/{{RUN}}/REPORT.md with each check (command, result), each fix (commit, test) and what stays open. Then one line: happier notify -t brain-care -p "<all green | fixed N: ... | needs you: ...>".

WIND DOWN: once main has your commits (or you made none): b=$(git branch --show-current); git -C /Users/test-user/Documents/saahil-brain pull --ff-only -q; orca-ide worktree rm --worktree path:<this folder> --force; git -C /Users/test-user/Documents/saahil-brain branch -D "$b" (its commits are on main); then, as your very last command, brain agents stop {{NAME}}.

DONE when: the report holds every check's output; brain doctor is green on both machines or the report says why not; every fix is on main and installed on both machines; the owner is notified; this worktree is removed and {{NAME}} is stopped.
