# Operations: daily life with the brain

Read this after setup. Most days you do nothing: the brain runs itself. This page says what runs, how to look at
it, and what to do when something needs you.

**In plain words.** Every hour the devbox dreams if something new landed. Every day at 15:00 (Pacific) a care agent
checks both machines, fixes what is broken, tries one measured improvement, and writes a report. You read the
report, answer the rare question the brain cannot settle, and sign in again when a sign-in expires.

```
 every prompt    hooks: digest + ≤ 3 pointers  (both machines, every host)
 every hour      laptop: brain sync (Unison) · devbox: dream if something new landed
 every day 15:00 devbox: brain-care agent → doctor both · fix · one experiment · REPORT.md · (notification)
 every week      devbox: evals (brain-eval-weekly, once `brain eval schedule` created it)
 when you want   brain doctor · brain metrics · Temporal UI · brain view (Obsidian)
```

Contents: [health](#health-brain-doctor) · [dreams](#dreams) · [Temporal UI](#temporal-ui) · [metrics](#metrics) ·
[evals](#evals) · [the care agent](#the-daily-care-agent) · [owner questions](#owner-questions) ·
[Obsidian](#obsidian-view) · [agents and the mesh](#agents-and-the-mesh) · [updating](#updating-both-machines) ·
[turning it off](#turning-it-off) · [when something is wrong](#when-something-is-wrong)

## Health: `brain doctor`

Run it on either machine. Green means no check failed or warned (exit 0); otherwise exit 1.

```
$ brain doctor
ok    brain.skeleton        /Users/<user>/brain
ok    brain.work-root       <n> entries in work/, no .git and no link back into the brain
ok    graph caps            <n> of <n> baselines and notes within their caps (target 100%)
ok    graph dangling links  0 links and refs from notes and hubs resolve to nothing (target 0)
ok    graph duplicates      0 notes or hubs are byte copies of another (target 0)
ok    graph hub coverage    100.0% of <n> live notes sit in a resolving hub (target ≥ 90%)
ok    graph orphans         0 of <n> notes and hubs have no authored edge (target 0)
ok    graph supersession    0 of <n> supersedes edges point at a note still live or missing; 0 loops (target 0, 0)
ok    hooks latency         start p95 <ms> (budget 300) ; prompt p95 <ms> (budget 150)
ok    hooks mode            live: start digest and pointers are injected
info  hooks usage           pull rate <a> of <b> sessions · open rate <c> of <d> pointers shown · <e> ignored
ok    host.claude           claude <version> (Claude Code): rules ✓ · memory ✓ · mod ✓ · loaded ✓
ok    host.codex            codex-cli <version>: hooks ✓ · mcp ✓
skip  host.omp              not installed on this machine
ok    mesh.inbox-hook       PostToolUse, UserPromptSubmit, Stop
ok    mesh.peer             laptop reachable, same version <version>
ok    mesh.sessions         <n> live sessions on this machine
ok    owner questions       <n> wait in .brain/owner-questions.jsonl
ok    sync.conflicts        0 unhandled conflict copies in the brain (losers are kept in .archive/conflicts)
ok    sync.last             last sync <age> ago, exit <code>

brain doctor GREEN on devbox (<version>)
```

For the laptop from the devbox: `brain peer step doctor --wait 10m`.

| Line | Read it as |
|---|---|
| `graph …` | the note graph's health. Targets: orphans 0, dangling 0, duplicates 0, hub coverage ≥ 90%, caps 100% |
| `hooks mode` | must say `live`. `shadow` means `~/brain/.brain/live` is missing: hooks log but inject nothing |
| `hooks usage` | pull rate = sessions that called a brain tool; open rate = pointers opened per shown. The honest measure of use |
| `host.*` | each agent host is wired. `skip` = that host is not installed |
| `mesh.peer` | the other machine answers and runs the same version |
| `sync.last` | an exit of 1 with "skipped … maxsizethreshold" is normal: files over 2 MB stay on their machine |

## Dreams

```
$ brain dream status
last dreams:
  2026-10-09T080000Z-…  completed 50 edits  28 calls (2 cached)  $5.97  deletions 0
  2026-10-09T070000Z-…  completed 40 edits  17 calls (2 cached)  $3.78  deletions 0
lock: …
llm cost today: $20.00 of $100.00
schedule brain-dream-hourly: next 2026-10-09T10:00:00Z, 25 runs so far, paused=false
```

| Task | Command |
|---|---|
| run one now (devbox) | `brain dream run --now` |
| read a run's report | `~/brain/.brain/dream-runs/<run-id>.json` (steps, edits, rejected, cost); the last skip: `.brain/dream-last-skip.json` |
| what curate.merge would do | `brain dream merge` (dry run: never applies) |
| which CARDs it would refresh | `brain dream cards --dry-run` |
| recreate the schedule | `brain dream schedule` |
| pause or unpause | `temporal schedule toggle --schedule-id brain-dream-hourly --pause --reason <why> -n brain --address 127.0.0.1:7233` (`--unpause` to resume) |
| services | `systemctl --user status brain-temporal brain-worker` |
| undo a dream | `git -C ~/brain revert <commit>` on the devbox, or copy files back from `.archive/dream/<run-id>/before/` |

A dream never deletes. It writes at most 50 edits and spends at most $8. A step that fails is recorded and the rest
go on; a lint failure reverts every edit of that run.

## Temporal UI

The UI binds to the devbox's localhost. From the laptop:

```
ssh -N -L 8233:127.0.0.1:8233 <devbox-host>
```

Then open http://127.0.0.1:8233/namespaces/brain/workflows. Each dream is `dream/<UTC hour>`, each session's
learning is `learn/<session8>`. History is kept 30 days.

## Metrics

```
brain metrics                 # the last 24 h on this machine, and the change since the previous day
brain metrics --write         # also save a snapshot in ~/brain/.brain/metrics/<machine>-<date>.json
brain peer step metrics --wait 5m      # the laptop's numbers, from the devbox
```

It reports dreams (runs, failed steps, edits, deletions, spend), model spend by workflow, the prompt hook (prompts,
p95, abstain rate), use (sessions pulling, pointers shown / opened / ignored, brain tool calls by MCP and by CLI) and
the newest eval numbers.

## Evals

```
brain eval navigation     # first prompt → files the session used: in the top-3 pointers? reachable in ≤ 3 tool calls?
brain eval qa             # questions from notes: the right note in brain_search's top 3? abstains on unknowns?
brain eval ab             # the same navigation with the brain vs a same-budget keyword grep
brain eval human          # prompts you typed → the file that answered (the headline number)
brain eval all            # all of them; reports in ~/brain/.brain/evals/
```

Use `--split dev` to tune and `--split test` once to judge. Never edit eval sets, targets or marks. Reference marks:
navigation ≥ 70% top-3 and ≥ 80% within 3 calls; QA recall@3 ≥ 80%. The human eval is the closest measure of whether
the brain helps you; on the reference setup it was the weakest number (top-3 15.9%, within 3 calls 50.7%), which is
why the care agent works on it first.

## The daily care agent

**In plain words.** Every day at 15:00 America/Los_Angeles, the devbox starts a Claude Code agent in a fresh
worktree of the brain's code. It checks health on both machines, fixes the brain's code test-first, tries one
improvement and keeps it only if the numbers say so, ships it to both machines, writes a report and stops itself.

```
 brain-care.timer (15:00 America/Los_Angeles, follows daylight saving; a missed run starts at boot)
   └─ ~/.local/bin/brain-care
        ├─ brain agents new brain-care-<day> --repo <brain_repo> --base origin/main   (Orca worktree + Claude Code)
        ├─ brain agents goal brain-care-<day> ~/brain/work/brain-care/runs/<day>/GOAL.md
        └─ backstop: brain agents stop after 2.5 h
   the agent: HEALTH  doctor both · dream failures · services · sync
              IMPROVE metrics + evals → check yesterday's change (revert if it did not help)
                      → one general change on a shadow copy (DEV to tune, TEST once) → keep only if it passes
              SHIP    commit · push to main · make publish · brain install · laptop update + install · re-check
              REPORT  runs/<day>/REPORT.md · one line in EXPERIMENTS.md · a notification if a notifier is installed
              WIND DOWN  remove its worktree and branch · brain agents stop <itself>
```

| What | Where |
|---|---|
| the goal it gets | `templates/brain-care-goal.md`, rendered into `~/brain/work/brain-care/runs/<day>/GOAL.md` |
| its method | `templates/brain-care-playbook.md` (observe · check yesterday · choose one thing · hypothesis · keep only if · record) |
| its reports | `~/brain/work/brain-care/runs/<day>/REPORT.md` |
| the experiment ledger | `~/brain/work/brain-care/EXPERIMENTS.md`: every experiment, kept or not, with DEV and TEST numbers and the verdict. The agent reads it before it chooses, and never retries a rejected idea without new evidence |
| limits | about 90 minutes and $5 of model calls; never deletes brain data, never pushes brain data, never changes a company repo, starts no other agents |

Run it now: `systemctl --user start brain-care.service`. Watch it: `brain agents screen brain-care-<day>`. Stop it:
`brain agents stop brain-care-<day>`.

**Keep-it bar** (from the playbook): the target improves; the human eval's TEST numbers do not drop; navigation TEST
stays ≥ 70% / ≥ 80%; `brain hook bench` p95 ≤ 150 ms; pointers on your prompts do not grow; a dream stays ≤ $8.

## Owner questions

When the dream's merge step finds two global rules that conflict and their dates do not settle it, it does not
guess. It appends one line to `~/brain/.brain/owner-questions.jsonl` (`ids`, `as_of`, `question`, `created`), and
`brain doctor` shows how many wait.

To answer one: read the two notes (`brain show <id>`), decide which holds (one supersedes the other, one is narrowed
to a scope, or both hold), and have your agent write that as a delta. Apply it with `brain apply-delta <file.json>
--dry-run`, then without `--dry-run`. Then set `"answered"` on that line. Once one of the two notes is superseded,
`brain doctor` stops counting the question.

## Obsidian view

`brain view` (laptop) builds `~/brain-view`: read-only copies of the brain's text with `[[wikilinks]]`, plus the
markdown of `work/`, with no bulk. Open it as a vault in Obsidian. Every sync refreshes it. Do not edit files there;
edit in `~/brain` or let the dream do it. Obsidian is a viewer only: the brain works with no app open.

## Agents and the mesh

| Task | Command |
|---|---|
| every live session on both machines | `brain agents list` |
| message a session on either machine | `brain agents note <machine>:<name> "<text>"` (prints DELIVERED or QUEUED) |
| start an agent in its own worktree on the devbox | `brain agents new <name> [--repo PATH] [--base REF]` |
| give it a goal (full text, ≤ 4,000 chars, verified) | `brain agents goal <name> <file>` |
| see its screen | `brain agents screen <name> [--lines N]` |
| move a laptop session to the devbox (`/exit` it first) | `brain agents move <session> <name>` |
| resume a saved devbox conversation | `brain agents resume <name> <session>` |
| stop a finished agent (transcript kept) | `brain agents stop <name> [--rm-worktree]` |
| copy a box with its bulk to the devbox (never deletes) | `brain agents box <box>` |
| run an allowed step on the other machine | `brain peer step <name> [--wait 10m]`, `brain peer job <id>`, `brain peer status` |

Name a session with `/rename <name>` inside it; that name is its address.

## Updating both machines

From the brain's code checkout on the devbox (`brain_repo`):

```
git -C <brain_repo> pull --ff-only
make -C <brain_repo> publish            # installs the devbox binaries, restarts the worker, stages the laptop binary
brain install                           # devbox hosts
brain peer step update --wait 5m        # the laptop pulls the staged binary (sha256 checked) and restarts its bridge
brain peer step install --wait 10m      # laptop hosts
brain doctor && brain peer step doctor --wait 10m
```

The care agent does exactly this when it ships a change. `make publish` stages the laptop binary in the dist
folder your settings name (`~/.local/share/brain/dist` under your home). From a new release instead of the repo,
run its `scripts/setup-devbox.sh` again with the same flags (every step skips what is already done), then the update
and install steps above. Running sessions pick up a new mod or new hooks at their next restart.

## Turning it off

| Scope | How |
|---|---|
| one session | start it with `BRAIN_OFF=1` in its environment |
| one host on one machine | `brain install --off <host>`, where the host is `claude`, `mesh`, `codex`, `cursor`, `prime`, `omp` or `git` (takes out exactly what it added) |
| the dream | pause `brain-dream-hourly` (see Dreams) |
| the daily care agent | `systemctl --user disable --now brain-care.timer` |

## When something is wrong

| Symptom | First command | Usual cause and fix |
|---|---|---|
| doctor red on `sync.last` | `brain sync status` | the laptop slept or the tunnel is down: `launchctl kickstart -k gui/$(id -u)/com.saahil.brain-tunnel`, then `brain sync` |
| doctor red on `sync.conflicts` | `brain sync --archive-conflicts` | both machines wrote the same file; the newer copy stays live |
| `mesh.peer` unreachable | `brain peer status` | the laptop is asleep (steps queue), or the bridge or tunnel stopped: see `~/Library/Logs/brain-bridge.log` and `brain-tunnel.log` |
| dream step failed | `.brain/dream-runs/<run-id>.json` | a model auth error (sign in to Claude on the devbox), the cost cap, or a bug: the care agent picks it up |
| no pointers ever | `brain doctor` (`hooks mode`) | `~/brain/.brain/live` is missing, or `BRAIN_OFF=1` is set |
| `brain_*` tools answer nothing in Claude Code | `brain doctor` (`host.claude`) | restart the session; check `claude plugin list` shows `saahil-brain` |
| AWS or GitHub errors in `devbox status` | `brain machine status` | a sign-in expired: `brain machine login aws` / `github` **[HUMAN]** |

More cases, each with its cause: [LESSONS.md](LESSONS.md).
