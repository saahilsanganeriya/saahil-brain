# SETUP: install the brain on a laptop and a devbox

**For the coding agent that runs this.** Follow the steps in order. Each step says where to run it (LAPTOP or
DEVBOX), what to run, what you should see, and the check that proves the step worked. Do not skip a check. When a
check fails, stop, read [section 7](#7-when-a-step-fails), fix the cause, and run the check again before you go on.

**For the human who hands this to an agent.** The agent can do most of it. It stops at every **[HUMAN]** marker:
those steps need you (a sign-in in a browser, a `sudo` password, a click in the Orca app, or a yes to spend money).
Read [README.md](README.md) first, then [docs/DEVBOX-AND-ORCA.md](docs/DEVBOX-AND-ORCA.md) for step 0.

```
 0 HUMAN    devbox exists · ssh alias · sign-ins · Orca on the devbox, paired with the laptop app
 1 DEVBOX   scripts/setup-devbox.sh  → bind mount · binaries · identity · brain init · Temporal + worker
                                       · dream schedule · bridge token · brain install · laptop files staged
 2 LAPTOP   scripts/setup-laptop.sh <devbox-host> → binary · token · identity · bridge + tunnel · Unison
                                       · brain init · brain install · the devbox sees the laptop
 3 BOTH     backfill: memories · sessions · doc folders · CARDs · index · evals · first dream · doctor GREEN
 4 DEVBOX   daily care agent: brain code repo · brain-care timer · first run
```

Supported setup: a MacBook (Apple silicon) plus a Linux devbox on EC2, Orca, Claude Code. **Laptop-only is not
supported:** the dream, Temporal and the daily care agent run on the devbox.

**What you run it from.** A release, `brain-setup-<version>.zip`: this repository plus prebuilt binaries in `bin/`
and their `SHA256SUMS`. Unzip it on the laptop. From a clone of the repository, `make dist` (Go 1.26) builds the
same zip under `dist/`. Below, "the release folder" is the unzipped `brain-setup-<version>/`.

## Conventions

| Mark | Meaning |
|---|---|
| **[HUMAN]** | stop and ask the human to do this, then wait for them to say it is done |
| **[HUMAN] spend** | the command calls paid models; tell the human the expected cost and wait for a yes |
| **Run** (LAPTOP / DEVBOX) | the commands, on that machine. "DEVBOX" means in a shell on the devbox (`ssh <devbox-host>`) |
| **Expect** | what the command prints when it works (`<…>` parts vary) |
| **Check** | a command whose output proves the step. If it differs, the step failed |

Placeholders: `<user>` your laptop login (`id -un` on the laptop) · `<devbox-user>` your Linux user on the devbox ·
`<devbox-host>` the ssh alias of the devbox · `<version>` the release version · `<orca-env>` the Orca environment id
of the devbox · `<work-repo>` absolute path of your main work repo · `<brain-repo>` absolute path where your copy of
the brain's code will live on the devbox (default `/Users/<user>/Documents/saahil-brain`) · `<profile>` the name of a
team profile in `profiles/` (your own `profiles/<team>.json`, see docs/CONFIG.md), or nothing.

**Team profile.** A profile is `profiles/<profile>.json`, committed in the repository: the settings a whole team
shares (SSO session name, GitHub certificate path, the default PR repo, the usual work repo). Pass `--profile
<profile>` to both setup scripts; the profile fills only the keys the human did not set. Without a profile, the
code's defaults are generic and the features that need a team value say which key to set
([docs/CONFIG.md](docs/CONFIG.md#1-settings-identityjson-and-team-profiles)). Leave out every `--profile <profile>`
below when there is none.

Ground rules for the agent: never open, print or copy a secrets file (`.env*`, keys, tokens; the bridge token is
copied by the scripts, never shown). Never delete anything in `~/brain`; move it into `~/brain/.archive/`. Kill a
process only by the PID you were given. Never change a company repo for the brain. Spell stored paths as
`/Users/<user>/…` on both machines.

---

## 0. Before you start (HUMAN)

**[HUMAN]** Do these with the human. The full how-to is [docs/DEVBOX-AND-ORCA.md](docs/DEVBOX-AND-ORCA.md).

| # | What | Done when |
|---|---|---|
| 0.1 | Create the devbox: Ubuntu x86_64, ≥ 8 vCPU, ≥ 32 GB RAM, ≥ 200 GB gp3 disk, an Elastic IP, ssh (tcp/22) open only to the human's addresses | the instance runs |
| 0.2 | Add the ssh alias `<devbox-host>` to the laptop's `~/.ssh/config`; key-based login, no password | check 0.a passes |
| 0.3 | On the devbox: `sudo apt-get install -y git unison`; Claude Code installed at `~/.local/bin/claude` and signed in with the human's own Claude login (the dream's worker uses that login; no API key); `gh` signed in; GitHub and AWS sign-ins the way the team does them | check 0.b passes |
| 0.4 | On the laptop: Homebrew, Claude Code, the Orca app | `brew --version`, `claude --version` print versions |
| 0.5 | Orca on the devbox: the AppImage at `/opt/orca/orca-linux.AppImage`; the `orca-serve` user unit rendered from the release (below); linger on | check 0.c passes |
| 0.6 | Pair the laptop's Orca app with the devbox's `orca serve` (pairing link → `orca environment add`), and keep the laptop tunnel `ssh -N -L 16768:127.0.0.1:6768 <devbox-host>` up with a LaunchAgent | check 0.d passes |
| 0.7 | Collect the values below | the table is filled |

Render the Orca unit from the release. First put the release on the devbox and check it (the commands of
[step 1.1](#11-put-the-release-on-the-devbox-and-check-it)). Then, on the DEVBOX in `~/brain-setup-<version>`
(`setup-devbox.sh` renders the unit again the same way later):

```
mkdir -p ~/.config/systemd/user
sed -e 's|@LAPTOP_HOME@|/Users/<user>|g' -e 's|@DEVBOX_HOME@|/home/<devbox-user>|g' -e 's|@USER@|<devbox-user>|g' \
    deploy/systemd/orca-serve.service > ~/.config/systemd/user/orca-serve.service
systemctl --user daemon-reload && systemctl --user enable --now orca-serve
sudo loginctl enable-linger <devbox-user>                 # [HUMAN] sudo
```

**Values to collect** (ask the human; write them down for steps 1 and 2):

| Value | How to find it |
|---|---|
| `<user>` | LAPTOP: `id -un` |
| `<devbox-host>` | the alias from 0.2 |
| `<devbox-user>` | DEVBOX: `id -un` |
| `<orca-env>` | LAPTOP: `orca environment list --json` → the id of the devbox environment |
| `<work-repo>` | the absolute path of the main work repo checkout, laptop spelling (`/Users/<user>/…`) |
| `<brain-repo>` | where the brain's code will be checked out on the devbox (default `/Users/<user>/Documents/saahil-brain`) |
| `<profile>` | the team's profile name, if the team has one in `profiles/` (`ls profiles/` in the release folder) |
| devbox instance id, region, AWS profile | for the laptop's `devbox` helper; the human's AWS console or team docs |

**Checks:**

```
0.a  LAPTOP  ssh -o BatchMode=yes <devbox-host> 'uname -sm; nproc; free -g | awk "/Mem:/{print \$2}"; df -h ~ | tail -1'
     Expect: Linux x86_64 · a number ≥ 8 · a number ≥ 30 (GiB) · a disk with ≥ 200G      (no password prompt)
0.b  LAPTOP  ssh <devbox-host> '/usr/bin/unison -version; ~/.local/bin/claude --version; git --version; gh auth status'
     Expect: unison version 2.5x… · <version> (Claude Code) · git version … · Logged in to github.com …
0.c  LAPTOP  ssh <devbox-host> 'systemctl --user is-active orca-serve; loginctl show-user $(id -un) -p Linger'
     Expect: active · Linger=yes
0.d  LAPTOP  orca environment list --json
     Expect: an environment for the devbox, with the id you wrote down as <orca-env>
```

---

## 1. Devbox (`scripts/setup-devbox.sh`)

### 1.1 Put the release on the devbox and check it

**Run** (LAPTOP, where the human unzipped the release):

```
cd ~/Downloads/brain-setup-<version>        # wherever the release was unzipped
shasum -a 256 -c SHA256SUMS
rsync -a ./ <devbox-host>:brain-setup-<version>/
```

**Expect:** every line of `shasum` ends in `OK`.

**Check** (DEVBOX): `cd ~/brain-setup-<version> && sha256sum -c SHA256SUMS` → every line ends in `OK`.

### 1.2 Dry run, then run

**Run** (DEVBOX, in `~/brain-setup-<version>`):

```
scripts/setup-devbox.sh --dry-run --profile <profile> --laptop-home /Users/<user> --devbox-host <devbox-host> \
    --orca-env <orca-env> --work-repo <work-repo> --brain-repo <brain-repo> --aws-profile <aws-profile>
scripts/setup-devbox.sh --profile <profile> --laptop-home /Users/<user> --devbox-host <devbox-host> \
    --orca-env <orca-env> --work-repo <work-repo> --brain-repo <brain-repo> --aws-profile <aws-profile>
```

`--laptop-home` is required (the devbox cannot guess the laptop's login). `--work-repo` may be left out when the
profile sets it. `--aws-profile` is optional: it is the laptop AWS profile the `devbox` helper uses.

**[HUMAN]** The script uses `sudo` twice and says why before each: to add the bind mount to `/etc/fstab`, and to turn
on linger. If `sudo` asks for a password, the human types it.

**Expect:** the dry run starts with `DRY RUN: nothing below is changed` and prints `would: …` for each change. The
real run prints one `== <step>` header per step, in this order: bind mount · binaries · settings · brain skeleton ·
Temporal CLI · systemd units (with linger) · dream schedule and retention · bridge token · agent hosts (brain
install) · laptop files staged · daily care · next, on the laptop. Under each header it prints what it did
(`installed`, `added`, `created`, `set`, `sha256 ok`) or that it was already done (`present`, `unchanged`,
`running`). The daily care step says `todo` on the first run: that is step 4. The script is safe to run again.

A `SKIPPED … (differs from …; --force replaces it, keeping a .bak)` line means a file on the devbox differs
from the release's copy. Show it to the human before you rerun with `--force`.

### 1.3 Check every part the script did

Run each check on the DEVBOX. All must match before step 2.

| Part | Check | Expect |
|---|---|---|
| bind mount | `findmnt -n -o TARGET /Users/<user>` | `/Users/<user>` |
| | `test "$(stat -c %i /home/<devbox-user>)" = "$(stat -c %i /Users/<user>)" && echo same-folder` | `same-folder` |
| | `grep -c ' /Users/<user> none bind' /etc/fstab` | `1` |
| binaries | `brain version` | `brain <version> (devbox)` |
| | `test -x ~/.local/bin/brain-worker && echo worker-ok` | `worker-ok` |
| identity | `brain identity` | every key with its value; `devbox_host`, `orca_env`, `work_repo`, `brain_repo` come from the file (the profile's values are merged into it by `--init`), not `derived` |
| | `grep -c devbox_instance_id ~/.config/brain/identity.json` | `1` (the script reads the instance id and region from EC2 instance metadata) |
| brain skeleton | `brain init` | `brain skeleton ready at /Users/<user>/brain` |
| live marker | `cat ~/brain/.brain/live` | a UTC time. `brain init` writes it for a fresh brain (no `shadow_docs`) |
| Temporal CLI | `temporal --version` | `temporal version 1.7.<n> (Server …, UI …)` or newer |
| services | `systemctl --user is-active brain-temporal brain-worker` | `active` twice |
| | `systemctl --user show brain-worker -p Environment` | contains `BRAIN_LLM_LOGIN=1` (model calls use the human's Claude login) |
| linger | `loginctl show-user <devbox-user> -p Linger` | `Linger=yes` |
| dream schedule | `brain dream status` | its last line: `schedule brain-dream-hourly: next <time>, <n> runs so far, paused=false` |
| retention | `temporal operator namespace describe -n brain --address 127.0.0.1:7233` | a line `Config.WorkflowExecutionRetentionTtl  720h0m0s` |
| bridge token | `stat -c '%a %s' ~/.config/brain/bridge.token` | `600` and a size of at least 32 bytes (never print the token itself) |
| hosts wired | `brain install --verify` | `ok` for `claude: rules, memory, mod`, `mesh: hooks`, `git: ignore`; `ok` or `skip` for the others |
| Claude Code mod | `claude plugin list` | lists `saahil-brain` (enabled). `brain install` creates and registers the local marketplace `~/mods/saahil-mods` when missing |
| memory folder | `grep -o '"autoMemoryDirectory": *"[^"]*"' ~/.claude/settings.json` | `"autoMemoryDirectory": "/Users/<user>/brain/memory"` |
| laptop files staged | `ls ~/.local/share/brain/dist/` | `brain-darwin-arm64`, `brain-darwin-arm64.sha256`, `devbox`, `setup-laptop.sh` |
| seed hubs | `ls ~/brain/hubs/` | the generic hubs `brain init` seeded (the home `index.md` and practice topics) |
| health so far | `brain doctor` | every line `ok`, `skip` or `info`, except the lines about the laptop (`mesh.peer`, `sync.last`) and lines that say there is no data yet |

---

## 2. Laptop (`scripts/setup-laptop.sh`)

Run every laptop step in an **interactive Terminal window**, not through a LaunchAgent or the bridge: macOS blocks
launchd jobs from reading `~/Documents` and they hang
([docs/LESSONS.md](docs/LESSONS.md#5-launchd-jobs-hang-on-documents-tcc)).

### 2.1 Run the script

**Run** (LAPTOP, in the release folder; the devbox also stages the same script, see the last lines of step 1.2):

```
scripts/setup-laptop.sh <devbox-host> --profile <profile> --dry-run
scripts/setup-laptop.sh <devbox-host> --profile <profile>
```

**[HUMAN]** macOS may ask to allow Terminal to access a folder, and Homebrew may ask for the password. The human
clicks Allow and types it.

**Expect:** one `== <step>` header per step: the devbox answers over ssh · binaries (`brain`, sha256 checked, and
the `devbox` helper) · settings (the bridge token and `identity.json`, both copied from the devbox) · the bridge and
its reverse tunnel · unison and the hourly brain sync · brain skeleton · agent hosts (brain install, then `ok  claude
plugin list shows saahil-brain`) · the devbox reaches this laptop. The last lines are the devbox's `brain peer
status` JSON and `BRIDGE OK: the devbox reaches this laptop through the bridge`.

### 2.2 Check every part

Run each check on the LAPTOP unless the row says DEVBOX.

| Part | Check | Expect |
|---|---|---|
| binary | `brain version` | `brain <version> (laptop)`, the same `<version>` as `ssh <devbox-host> '~/.local/bin/brain version'` |
| token | `stat -f '%Lp' ~/.config/brain/bridge.token` | `600` |
| identity | `cmp <(ssh <devbox-host> cat .config/brain/identity.json) ~/.config/brain/identity.json && echo same-identity` | `same-identity` |
| bridge + tunnel | `launchctl list com.saahil.brain-bridge` and `launchctl list com.saahil.brain-tunnel` | each prints a block with a `"PID" = <n>;` line (it runs) |
| Unison | `unison -version` | `unison version 2.5x…` |
| hourly sync | `launchctl list com.saahil.brain-sync` | a block with `"Label" = "com.saahil.brain-sync";` |
| first sync | `brain sync status` | `"exitCode": 0`, or `1` when the only problems are files over 2 MB (`maxsizethreshold`). The sync LaunchAgent runs one sync when it loads; if none is recorded yet, run `brain sync` |
| brain skeleton | `ls ~/brain` | `BRAIN.md`, `hubs`, `inbox`, `journal`, `memory`, `notes`, `sessions`, `work` |
| live marker | `cat ~/brain/.brain/live` | a UTC time |
| hosts wired | `brain install --verify` | `ok` for `claude: rules, memory, mod`, `mesh: hooks`, `git: ignore`; `ok` or `skip` for `codex`, `cursor`, `prime`, `omp` |
| Claude Code mod | `claude plugin list` | lists `saahil-brain` |
| memory folder | `grep -o '"autoMemoryDirectory": *"[^"]*"' ~/.claude/settings.json` | `"autoMemoryDirectory": "/Users/<user>/brain/memory"` |
| devbox → laptop (DEVBOX) | `brain peer status` | JSON with `"ok": true`, `"machine": "laptop"`, the same `"version"` |
| both machines' agents (DEVBOX) | `brain agents list` | sessions from both, as `laptop:<name>` and `devbox:<name>` (open one Claude Code session on the laptop first) |

### 2.3 The laptop's devbox helper

`brain machine up|down|status|sync` on the laptop run `~/.local/bin/devbox`, which `setup-laptop.sh` installed. It
reads `devbox_instance_id`, `devbox_region` and `devbox_aws_profile` from `identity.json`. If one is missing (the
script printed `unknown`), add it to `~/.config/brain/identity.json` on BOTH machines (same content), as in
`identity.example.json`.

**Check:** `brain machine status` prints the instance state (`running`), uptime, and sign-in expiry. If it prints
`instance: unknown (aws failed above)`, the laptop's AWS sign-in expired: **[HUMAN]** `brain machine login aws`.

### 2.4 A first live session

**Run** (LAPTOP): open a new Claude Code session in any repo and ask: "What does BRAIN.md say the five brain tools
are?".

**Expect:** the answer names `brain_search`, `brain_show`, `brain_sections`, `brain_note` and `/wrap` without a
tool call (BRAIN.md is loaded). **Check:** `brain doctor` → `hooks mode  live: start digest and pointers are
injected`.

---

## 3. Backfill (both machines)

Bring the human's history in. The full procedure, with every check, is [docs/BACKFILL.md](docs/BACKFILL.md). Do it in
this order and run each step's check before the next.

| # | Where | Command | Check | Cost |
|---|---|---|---|---|
| 3.1 | each machine | `brain import memory` | `imported <N> memory files …`; manifest has `<N>` lines | free |
| 3.2 | LAPTOP | `brain sync` | DEVBOX `ls ~/brain/.import/` shows `devbox laptop` | free |
| 3.3 | DEVBOX | `ls ~/brain/hubs/`; the human adds a hub for each domain of their work (BACKFILL step 1) | every topic the human works on has a hub | free |
| 3.4 | DEVBOX | `brain file --dry-run` | the batch plan and a balanced reconcile | free |
| 3.5 | DEVBOX | `brain file` | ends `… balanced ✓` | **[HUMAN] spend** ≈ $3 per 2,000 memories (Haiku) |
| 3.6 | DEVBOX | `brain file --model opus --retry-skipped` | skipped count falls | **[HUMAN] spend** ≈ $8 for the hardest few dozen (Opus) |
| 3.7 | each machine | `brain session backfill` | notes under `~/brain/sessions/<machine>/` ≈ transcripts | free |
| 3.8 | where the folder is | move each existing doc folder into `~/brain/work/<box>/` by MOVE, with `brain cutover manifest` before and after and `brain cutover compare` (see BACKFILL step 3) | `missing 0`, `changed 0` | free |
| 3.9 | DEVBOX | `brain backfill run --dry-run` | every box with its planned action | free |
| 3.10 | DEVBOX | `brain backfill run --budget <usd>` (add `--include-paused` for older campaigns) | every box has `CARD.md`; `brain backfill status` | **[HUMAN] spend** reference $2 (CARDs) to $20 (with paused campaigns) |
| 3.11 | each machine | `brain index` | `indexed <N> paths …` | free |
| 3.12 | DEVBOX | `brain eval build` | sets under `~/brain/.brain/evals/` | free |
| 3.13 | each machine | `brain eval build --human` (laptop: with `BRAIN_LLM_LOGIN=1`) | `judged … kept … cost $…` | **[HUMAN] spend** ≈ $1–1.50 per machine (Opus) |
| 3.14 | DEVBOX | `brain dream run --now`, then `brain dream status` | the run `completed`, `deletions 0` | **[HUMAN] spend** ≤ $8 |
| 3.15 | both | `brain doctor` (laptop also via DEVBOX `brain peer step doctor --wait 10m`) | `brain doctor GREEN on <machine>` on both | free |
| 3.16 | LAPTOP | `brain view` | `~/brain-view` exists; the human opens it in Obsidian (optional) | free |

Optional: `brain eval schedule` (DEVBOX) creates the weekly eval run `brain-eval-weekly`.

---

## 4. The daily care agent (devbox)

**In plain words.** Every day at 15:00 America/Los_Angeles, the devbox starts a Claude Code agent that checks the
brain on both machines, fixes the brain's code, tries one measured improvement, ships it, writes a report and stops.
It works in the human's own private copy of the brain's code. See [docs/OPERATIONS.md](docs/OPERATIONS.md#the-daily-care-agent).

### 4.1 The brain's code as the human's private repo

**[HUMAN]** The human creates an empty PRIVATE repository on GitHub for it (any name), says where, and makes sure
`gh` is installed and signed in on the devbox (`gh auth status`): the care agent pushes through it.

The release folder is the repository (a `git archive` of it) plus build outputs, so the human's repo starts from it.
If the human cloned the repository instead, use that clone as `<brain-repo>` and only point `origin` at the private
repo.

**Run** (DEVBOX):

```
mkdir -p <brain-repo>
rsync -a --exclude bin/ --exclude SHA256SUMS ~/brain-setup-<version>/ <brain-repo>/   # check: <brain-repo>/go.mod exists
git -C <brain-repo> init -b main
git -C <brain-repo> add -A && git -C <brain-repo> commit -m "brain <version> from the release"
git -C <brain-repo> remote add origin <the human's private repo URL>
git -C <brain-repo> config credential.helper '!gh auth git-credential'      # repo-local; the global git config is untouched
git -C <brain-repo> push -u origin main
```

Then register it with Orca on the devbox (in an Orca terminal there): `orca repo add --path <brain-repo>`.

**Check:** `git -C <brain-repo> status -sb` → `## main...origin/main` with no changes; the laptop's Orca app lists the
project on the devbox environment. The repo holds code and team profiles only: never commit brain data (`~/brain`)
or the personal `identity.json` into it.

### 4.2 Install the care agent

`setup-devbox.sh` installs the care agent once `<brain-repo>/.git` exists. Run it again with the same flags as in
step 1.2.

**Run** (DEVBOX, in `~/brain-setup-<version>`):

```
scripts/setup-devbox.sh --profile <profile> --laptop-home /Users/<user> --devbox-host <devbox-host> \
    --orca-env <orca-env> --work-repo <work-repo> --brain-repo <brain-repo> --aws-profile <aws-profile>
```

**Expect:** under `== daily care`: `installed  …/.local/bin/brain-care`, `present  credential.helper in <brain-repo>`,
the timer enabled, and `first run, when you want one: systemctl --user start brain-care.service`. Every other step
says `present`, `unchanged` or `running`.

**Check:** `test -x ~/.local/bin/brain-care && echo care-ok` prints `care-ok`, and
`systemctl --user list-timers brain-care.timer --no-pager` shows the next run at 15:00 America/Los_Angeles
(22:00 or 23:00 UTC, by daylight saving).

### 4.3 First run

**[HUMAN] spend** about 90 minutes of an agent session and ≤ $5 of brain model calls per day.

**Run** (DEVBOX): `systemctl --user start brain-care.service`

**Expect:** `journalctl --user -u brain-care.service -n 5 --no-pager` shows
`started brain-care-<day>; goal …/runs/<day>/GOAL.md; backstop stop in 2.5 h`.

**Check:** `brain agents list` shows `devbox:brain-care-<day>`. When it ends (≤ 2.5 h),
`~/brain/work/brain-care/runs/<day>/REPORT.md` exists, holds the health output and the metrics table, and
`brain agents list` no longer shows the agent. The care agent sends a notification at the end only when a
notifier is installed on the devbox; without one it skips that step, and the report is the record.

---

## 5. Final check (both machines)

All of these must hold. Paste their output to the human as proof.

```
DEVBOX  brain doctor                          → last line: brain doctor GREEN on devbox (<version>)
DEVBOX  brain peer step doctor --wait 10m     → the laptop's doctor, GREEN
DEVBOX  brain peer status                     → "ok": true, "machine": "laptop", same version
DEVBOX  brain agents list                     → sessions from laptop: and devbox:
DEVBOX  brain dream status                    → recent runs completed, deletions 0, paused=false
LAPTOP  brain sync status                     → a sync in the last 70 minutes
LAPTOP  brain install --verify                → ok for claude, mesh, git
DEVBOX  systemctl --user is-active brain-temporal brain-worker orca-serve brain-care.timer   → active ×4
```

---

## 6. What needs the human (summary)

| Step | Why the agent cannot do it |
|---|---|
| 0.1–0.6 | AWS access, sudo, Orca install and pairing in the app |
| 1.2, 0.5 | `sudo` for the fstab bind mount and linger |
| 2.1 | macOS folder-access prompts, the Homebrew password |
| 2.3, sign-ins | `brain machine login aws`, `github` and `gcloud` are browser flows |
| 3.5, 3.6, 3.10, 3.13, 3.14, 4.3 | they spend money on model calls |
| 0.7 | choosing the team profile (or none) |
| 3.3 | choosing and wording the hubs (the topics of the human's work) |
| 3.8 | which doc folders to bring in, and their box names |
| 4.1 | creating the private GitHub repo |

---

## 7. When a step fails

Find the symptom, fix the cause, run the step's check again. More causes are in [docs/LESSONS.md](docs/LESSONS.md).

| Step | Symptom | Cause | Fix |
|---|---|---|---|
| 0.a | ssh asks for a password or times out | wrong alias, key or security group | fix `~/.ssh/config`; open tcp/22 to your current address |
| 1.1 | a `FAILED` line in the checksum check | a damaged or changed file | get the release again; never run a binary that fails its checksum |
| 1.3 bind mount | `findmnt` prints nothing | the mount line is missing or `/Users/<user>` was not created | `sudo mkdir -p /Users/<user>`; add `/home/<devbox-user> /Users/<user> none bind,nofail 0 0` to `/etc/fstab`; `sudo mount /Users/<user>` **[HUMAN]** |
| 1.3 services | `brain-temporal` is `failed` | port 7233 or 8233 is taken, or the Temporal CLI is missing | `journalctl --user -u brain-temporal -n 50 --no-pager`; free the port or install the CLI, then `systemctl --user restart brain-temporal brain-worker` |
| 1.3 dream schedule | `temporal at 127.0.0.1:7233: …` | Temporal not up yet | wait 10 s after the services start; run `brain dream schedule` again |
| 1.3 live marker | `cat` fails | `brain init` ran before `identity.json` existed, or `shadow_docs` is not empty | check `brain identity` (`shadow_docs` must be `[]`), run `brain init` again |
| 1.3 mod | `claude plugin list` lacks `saahil-brain` | the `claude` CLI is not on `PATH` for the script, or it is not signed in | sign in to Claude Code, then `brain install` again |
| 2.1 | `sha256 mismatch` | the staged laptop binary changed mid-copy | DEVBOX: run `scripts/setup-devbox.sh` again (it restages), then 2.1 again |
| 2.2 bridge | `brain peer status` on the devbox fails | the tunnel is down, or the token differs | LAPTOP: `launchctl kickstart -k gui/$(id -u)/com.saahil.brain-tunnel`; read `~/Library/Logs/brain-tunnel.log` and `brain-bridge.log`; rerun `setup-laptop.sh` (it copies the token again) |
| 2.2 bridge | the tunnel log says `remote port forwarding failed for listen port 7722` | a stale listener from a dead tunnel | wait 3 minutes (sshd keepalive), or set `ClientAliveInterval 60` / `ClientAliveCountMax 3` on the devbox |
| 2.2 sync | `unison not installed` | Homebrew step skipped | `brew install unison`, then `brain sync --setup`, `brain sync --enable` |
| 2.2 sync | exit 2, "failed: <file>" | a file was being written during the sync | run `brain sync` again |
| any laptop step | the command hangs at 0% with no error | a launchd-started process tried to read `~/Documents` (macOS privacy control) | stop it by its PID; run the same command in an interactive Terminal |
| 3.5 | `no hubs under …/hubs` | `brain init` did not seed hubs (an older binary) | `brain file --seed-hubs` copies the binary's seed hubs where missing |
| 3.8 | `compare` reports `missing` or `changed` | something deleted or edited files during the move | stop; find the writer; never continue to the next folder until it is explained |
| 3.14 | a dream step `failed` with an auth error | the worker uses the human's Claude login (`BRAIN_LLM_LOGIN=1` in its unit), and Claude Code on the devbox is not signed in, or the unit lacks the line | **[HUMAN]** run `claude` once on the devbox and sign in; check `systemctl --user show brain-worker -p Environment`; then `systemctl --user restart brain-worker` and `brain dream run --now` |
| 3.15 | doctor red on `graph dangling links` | a note links to a file that moved | `brain show <path>` to see who links to it; move the file back or let the next dream relink |
| 4.3 | no agent appears | Orca does not know `<brain-repo>`, or `brain agents new` failed | `journalctl --user -u brain-care.service -n 30 --no-pager`; `orca repo add --path <brain-repo>` in an Orca terminal on the devbox |
