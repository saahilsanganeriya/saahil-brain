# The brain: a setup kit

**What it is, in plain words.** The brain is one folder, `~/brain`, kept the same on your laptop and on your
devbox (a Linux machine in the cloud that stays on). It holds three things:

- **your work**: the plans, specs, reports and results of each project, one box per project under `work/`;
- **what was learned**: small notes (facts, decisions, lessons, mistakes not to repeat), grouped by topic pages;
- **a one-page memory** that every Claude Code session loads, in every repo, on both machines.

A program called `brain` runs on both machines. It gives each agent session a short digest of the work at hand and
up to three hints per prompt, offers search tools, lets agents on either machine message each other, and checks its
own health. Every hour, a job on the devbox called the **dream** turns new sessions and memories into notes, merges
duplicates, refreshes each project's summary card and the memory page, checks everything and commits. It never
deletes anything. Once a day, a care agent checks the brain, fixes it and tries one measured improvement.

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

## Who it is for

You have a MacBook, you use Claude Code and Orca, and you hand setup work to coding agents. You create your own
devbox on EC2 and run Orca's headless server there, paired with the Orca app on your laptop.

**Laptop-only is not supported.** The dream, Temporal and the daily care agent run on the devbox. Without it, nothing
files new memories, refreshes cards or checks the brain's health.

## Prerequisites

| Need | Notes |
|---|---|
| MacBook, Apple silicon | the kit ships `darwin-arm64` |
| An EC2 devbox you control | Ubuntu x86_64, ≥ 8 vCPU, ≥ 32 GB RAM, ≥ 200 GB disk, an Elastic IP, ssh open only to you ([docs/DEVBOX-AND-ORCA.md](docs/DEVBOX-AND-ORCA.md)) |
| ssh from the laptop to the devbox | a key-based alias in `~/.ssh/config` |
| Claude Code on both machines, signed in | the dream's model calls run the devbox's `claude` CLI, with an API key or its sign-in ([docs/CONFIG.md](docs/CONFIG.md#2-environment-variables)) |
| Orca | the app on the laptop; `orca serve` on the devbox, paired with the app |
| On the devbox: `git`, `unison` | `sudo apt-get install -y git unison` |
| On the laptop: Homebrew | the setup installs Unison with it |
| Your team's sign-ins | GitHub and AWS, the way your team does them |
| A private GitHub repo (empty) | for your copy of the brain's code; the daily care agent commits to it |

## Costs

| What | Reference numbers (one person, two machines) |
|---|---|
| Building it the first time (backfill) | about $35 in one-time model calls: filing ~2,400 old memories ≈ $12, cards and campaign notes ≈ $20, eval sets ≈ $3; then the first dreams |
| The hourly dream | $0 when nothing is new; from cents to about $6 a run; hard cap $8 a run and $100 a day for all brain model calls |
| The daily care agent | about 90 minutes of an agent session and ≤ $5 of model calls a day |
| The devbox | EC2 compute while it runs (an 8-vCPU m7i.2xlarge ≈ $290 a month 24×7) plus the disk (≈ $0.08 per GB-month, billed while stopped). Prices: AWS us-west-2 on-demand, read 2026-09-30 |

Interactive agent sessions are paid as you pay for them today; they are not part of the brain's own ledger.
`brain dream status` prints today's brain model spend; `brain metrics` prints the last 24 hours.

## What is in this kit

```
brain-setup-<version>/
  README.md              this page
  AGENT-SETUP.md         the runbook your coding agent follows, step by step, with a check after each step
  docs/ARCHITECTURE.md   how it is built: folder, notes and links, the four levels, hooks, tools, dream, Temporal,
                         mesh and bridge, sync, history
  docs/DEVBOX-AND-ORCA.md  creating the devbox, the bind mount, orca serve and pairing, tunnels, the devbox helper
  docs/BACKFILL.md       bringing your history in: memories, sessions, doc folders, cards, evals, first dream
  docs/OPERATIONS.md     daily life: doctor, dreams, Temporal UI, metrics, evals, the care agent, updates
  docs/LESSONS.md        what broke while it was built, and the rule each failure left
  docs/CONFIG.md         settings (identity.json), environment variables, the files the brain owns
  bin/                   brain-darwin-arm64 · brain-linux-amd64 · brain-worker-linux-amd64
  SHA256SUMS             checksums of the kit's files (check them before you run anything)
  scripts/               setup-devbox.sh · setup-laptop.sh · devbox (EC2 helper) · brain-care
  deploy/systemd/        unit templates: brain-temporal, brain-worker, brain-care (service + timer), orca-serve
  templates/             brain-care-goal.md · brain-care-playbook.md
  identity.example.json  the settings file, with placeholders
  source/                saahil-brain-<version>.tar.gz: the code (Go, the Claude Code mod, skills, adapters)
```

The kit holds no brain data, no tokens or keys, and no one's personal names or paths. Your brain starts empty and
fills from your own history.

## Where to start

1. Read [docs/DEVBOX-AND-ORCA.md](docs/DEVBOX-AND-ORCA.md) and do its steps that need you: create the devbox, the ssh
   alias, the sign-ins, Orca on the devbox and pairing.
2. Give [AGENT-SETUP.md](AGENT-SETUP.md) to a coding agent on your laptop: "Follow AGENT-SETUP.md in
   `<path to this kit>`. Stop at every [HUMAN] step and ask me." It sets up the devbox, then the laptop, then the
   backfill and the daily care agent, and checks each step.
3. When it reports `brain doctor GREEN` on both machines, read [docs/OPERATIONS.md](docs/OPERATIONS.md).

## Privacy

- Brain data (your docs, notes, session notes) stays on your two machines. It leaves them only as input to model API
  calls, as every agent session already does. There is no cloud sync and no hosting.
- Session notes redact secrets, and the prompt hook never stores your prompt text. Never put secrets in `~/brain`.
- The brain's code repo holds code only. Never commit `~/brain` anywhere.
- Nothing in the brain changes your company repos. A `brain` link in a repo is optional, git-ignored by the global
  `/brain` rule, and never committed ([docs/LESSONS.md](docs/LESSONS.md#21-a-brain-link-in-a-repo-can-be-uploaded-or-crawled)).
