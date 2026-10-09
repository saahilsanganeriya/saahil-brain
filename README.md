# saahil-brain: one memory for your coding agents, on your laptop and your devbox

**What it is, in plain words.** The brain is one folder, `~/brain`, kept the same on your laptop and on your
devbox (a Linux machine in the cloud that stays on). It holds three things:

- **your work**: the plans, specs, reports and results of each project, one box per project under `work/`;
- **what was learned**: small notes (facts, decisions, lessons, mistakes not to repeat), grouped by topic pages;
- **a one-page memory** that every Claude Code session loads, in every repo, on both machines.

This repository is the code that runs it: one Go program, `brain`, on both machines; a Temporal worker on the
devbox; the skills (prompts) that hold all judgment; the Claude Code mod; and adapters for Codex, Cursor, Prime and
omp. `brain` gives each agent session a short digest of the work at hand and up to three hints per prompt, offers
search tools, lets agents on either machine message each other, and checks its own health. Every hour the devbox
**dreams**: it turns new sessions and memories into notes, merges duplicates, refreshes each project's card and the
memory page, checks everything and commits. It never deletes anything. Once a day a care agent checks the brain,
fixes it and tries one measured improvement.

```
        LAPTOP (macOS)                                          DEVBOX (Linux, always on)
 ┌──────────────────────────────┐                     ┌────────────────────────────────────────────┐
 │ ~/brain   (text + your bulk)  │◄──── Unison ───────►│ ~/brain   (text + devbox bulk + history)    │
 │                               │  hourly, laptop-     │                                            │
 │ brain bridge (localhost only) │  started             │ Temporal (local) ── brain-worker           │
 │   ◄──── ssh reverse tunnel ───┼──────────────────────│   dream every hour · learn from sessions   │
 │ Claude Code · Codex · Cursor  │                      │   backfill · evals   (Opus + Haiku)        │
 │ Orca app ──── ssh tunnel ─────┼─────────────────────►│ orca serve: agents keep running            │
 └──────────────────────────────┘                      │ brain-care: a daily agent that checks,     │
                                                        │   fixes and improves the brain             │
   in every agent session, in any repo:                 └────────────────────────────────────────────┘
     L0  memory page + digest (loaded)      L1  ≤ 3 hints per prompt, or none
     L2  brain_search / show / sections / neighbors / note      L3  read the file
```

## What you get

| You get | What it means day to day |
|---|---|
| **One memory across repos and machines** | a rule you teach an agent in one repo is known in the next session in another repo, on either machine. Claude Code's own memory folder points into the brain |
| **Hints, not floods** | each prompt gets at most three pointers to notes or files that match, or nothing when nothing matches well. Hints that agents never open sink over time |
| **The dream** | every hour (when something new landed) the devbox files new memories into notes, learns lessons from sessions where you corrected an agent, links notes into topic pages, refreshes stale project cards, merges duplicate rules and re-renders the memory page. ≤ $8 a run; never deletes |
| **The mesh** | agents on the laptop and on the devbox list each other (`brain agents list`) and send notes to each other (`brain agents note devbox:<name> "…"`), delivered inside the target session |
| **The daily care agent** | at 15:00 Pacific, an agent checks both machines, fixes the brain's code test-first, tries one improvement and keeps it only if the numbers improve, then reports to you |
| **Proof it helps** | evals built from your own history (did the hint point at the file the session needed?) and a usage funnel (hints shown → opened) |

## Quick start

1. Read [docs/DEVBOX-AND-ORCA.md](docs/DEVBOX-AND-ORCA.md) and do its steps that need you: create the devbox, the ssh
   alias, the sign-ins, Orca on the devbox and pairing.
2. Get a release: `brain-setup-<version>.zip` is this repository plus prebuilt binaries (`bin/`) and `SHA256SUMS`.
   From a clone, `make dist` builds one under `dist/` (Go 1.26).
3. Give [SETUP.md](SETUP.md) to a coding agent on your laptop: "Follow SETUP.md in `<the unzipped release>`, with
   profile `<team>` (or none). Stop at every [HUMAN] step and ask me." It sets up the devbox, then the laptop, then
   the backfill and the daily care agent, and checks each step.
4. When it reports `brain doctor GREEN` on both machines, read [docs/OPERATIONS.md](docs/OPERATIONS.md).

**Who it is for.** You have a MacBook (Apple silicon), you use Claude Code and Orca, and you hand setup work to coding
agents. You run your own EC2 devbox with Orca's headless server, paired with the Orca app on your laptop.
**Laptop-only is not supported:** the dream, Temporal and the daily care agent run on the devbox.

## Prerequisites

| Need | Notes |
|---|---|
| MacBook, Apple silicon | the release ships `darwin-arm64` |
| An EC2 devbox you control | Ubuntu x86_64, ≥ 8 vCPU, ≥ 32 GB RAM, ≥ 200 GB disk, an Elastic IP, ssh open only to you ([docs/DEVBOX-AND-ORCA.md](docs/DEVBOX-AND-ORCA.md)) |
| ssh from the laptop to the devbox | a key-based alias in `~/.ssh/config` |
| Claude Code on both machines, signed in | the dream's model calls run the devbox's `claude` CLI with YOUR Claude login; no API key needed |
| Orca | the app on the laptop; `orca serve` on the devbox, paired with the app |
| On the devbox: `git`, `unison`, `gh` | `sudo apt-get install -y git unison`; `gh` signed in (the care agent pushes with it) |
| On the laptop: Homebrew | the setup installs Unison with it |
| Your team's sign-ins | GitHub and AWS, the way your team does them |
| A private GitHub repo (empty) | for your own copy of this code; the daily care agent commits to it |

## Settings: personal, team, and code defaults

```
 code defaults (generic: derived from your OS user; no team or person values)
        ▲ overridden by
 profiles/<team>.json   committed in this repo: team settings (SSO session, cert path, PR repo, work repo)
        ▲ overridden by
 ~/.config/brain/identity.json   YOURS, on both machines, never committed (homes, devbox host, Orca environment)
```

`brain identity --init --profile <team>` writes your `identity.json` with the team profile merged under your own
values; `scripts/setup-devbox.sh` and `scripts/setup-laptop.sh` take the same `--profile <team>`. `brain identity`
prints every effective value and where it came from. Details: [docs/CONFIG.md](docs/CONFIG.md).

## Costs

| What | Reference numbers (one person, two machines) |
|---|---|
| Building it the first time (backfill) | about $35 in one-time model calls: filing ~2,400 old memories ≈ $12, cards and campaign notes ≈ $20, eval sets ≈ $3; then the first dreams |
| The hourly dream | $0 when nothing is new; from cents to about $6 a run; hard cap $8 a run and $100 a day for all brain model calls |
| The daily care agent | about 90 minutes of an agent session and ≤ $5 of model calls a day |
| The devbox | EC2 compute while it runs (an 8-vCPU m7i.2xlarge ≈ $290 a month 24×7) plus the disk (≈ $0.08 per GB-month, billed while stopped). Prices: AWS us-west-2 on-demand, read 2026-09-30 |

`brain dream status` prints today's brain model spend; `brain metrics` prints the last 24 hours.

## Docs

| Read | For |
|---|---|
| [SETUP.md](SETUP.md) | the runbook a coding agent follows on both machines, a check after every step, [HUMAN] markers |
| [docs/DEVBOX-AND-ORCA.md](docs/DEVBOX-AND-ORCA.md) | creating the devbox, the bind mount, orca serve and pairing, tunnels, the devbox helper |
| [docs/BACKFILL.md](docs/BACKFILL.md) | bringing your history in: memories, sessions, doc folders, cards, evals, the first dream |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | daily life: doctor, dreams, Temporal UI, metrics, evals, the care agent, updates |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | how it is built: folder, notes and links, the four levels, hooks, tools, dream, Temporal, mesh, sync |
| [docs/LESSONS.md](docs/LESSONS.md) | what broke while it was built, and the rule each failure left |
| [docs/CONFIG.md](docs/CONFIG.md) | settings, profiles, environment variables, the files the brain owns |
| [FORMAT.md](FORMAT.md) | the file formats and code conventions every part reads and writes |
| [internal/dream/docs/dream.md](internal/dream/docs/dream.md), [backfill-and-learn.md](internal/dream/docs/backfill-and-learn.md) | the dream, the backfill and learning, in depth |

History, not setup: [PARITY.md](PARITY.md) (how `brain` replaced the original owner's `devbox-agent`) and
[internal/cutover/docs/cutover.md](internal/cutover/docs/cutover.md) (the tools that moved an existing docs tree
into a brain). Keep them for the record; a new setup does not need them.

## What is in this repository

```
cmd/brain/          the CLI: every command registers itself from cmd_<area>.go; main.go only dispatches
cmd/brain-worker/   the Temporal worker (devbox): dream, learn, backfill, evals
internal/           index, hooks, ranking, dream, mesh (agents, bridge, rpc), sync, install, doctor, lint, evals …
skills/             the prompts that hold judgment: file, distil, link, hubs, merge, card, summarise, judge, verify, wrap
mod/                the Claude Code mod `saahil-brain`: brain_* tools, pane, band, write guard, hooks
adapters/           Codex, Cursor, Prime and omp wiring
templates/          BRAIN.md, seed hubs, the care agent's goal and playbook
profiles/           committed team settings (profiles/<team>.json)
deploy/systemd/     unit templates: brain-temporal, brain-worker, brain-care (service + timer), orca-serve
scripts/            setup-devbox.sh · setup-laptop.sh · devbox (EC2 helper) · brain-care · publish.sh
testdata/           a tiny synthetic corpus (no real data)
```

## Build and test

```
make build       # bin/brain-linux-amd64, bin/brain-worker-linux-amd64, bin/brain-darwin-arm64 (CGO_ENABLED=0)
make test        # go test -p 6 ./...
make dist        # dist/brain-setup-<version>.zip: the repo, bin/, SHA256SUMS
make publish     # on the devbox: install its binaries, stage the laptop's (brain self-update pulls it)
```

Go 1.26, no cgo, builds for linux/amd64 and darwin/arm64. Tests are written failing first, use synthetic data
only, and name the defect each one catches ([FORMAT.md](FORMAT.md#code-conventions)).

## Code only

The brain's data (`~/brain`: docs, notes, memories, sessions, transcripts) may hold customer data and personal data.
It never enters this repository; `.gitignore` keeps the brain folder, databases, transcripts and session files out.
Your `identity.json` is personal and is never committed either. Brain data stays on your two machines and leaves
them only as input to model API calls, as every agent session already does. Nothing in the brain changes your
company repos: a `brain` link in a repo is optional, git-ignored by the global `/brain` rule, and never committed
([docs/LESSONS.md](docs/LESSONS.md#21-a-brain-link-in-a-repo-can-be-uploaded-or-crawled)).
