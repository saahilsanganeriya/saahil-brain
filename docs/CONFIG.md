# Configuration: settings, team profiles, environment variables, and the files the brain owns

Read this when you need to know which value comes from where, or which file a command writes.

**In plain words.** The brain reads one small settings file, `identity.json`. It holds the values that differ from
person to person: your user name, your two home folders, the name of your devbox, your Orca environment and your
repos. Both machines keep the same copy, and it is never committed anywhere. A team can share its common values in a
**profile**, a small file beside the code that the build puts into the binary; git keeps only the example profile. A
value in neither place gets a generic default that `brain` works out by itself. Everything else (the caps, the ports, the folder layout) is fixed in the code.

```
 code defaults            generic, derived from your OS user; no team or person values
      ▲ overridden by
 profiles/<team>.json     what a whole team shares (SSO session, cert, PR repo, work repo, ticket keys); kept out of git
      ▲ overridden by
 identity.json            yours, ~/.config/brain/identity.json, the same on both machines, NEVER committed

 brain identity --init --profile <team>   writes identity.json: your flags, then the file's own values, then the profile
 brain identity                            prints every effective value and where it came from
```

## 1. Settings: `identity.json` and team profiles

Location: `~/.config/brain/identity.json` on BOTH machines, with the same content. `brain identity --init` writes it
(the setup scripts call it); `setup-laptop.sh` copies the devbox's file. `brain identity` prints every value that is
in effect, and says where each value came from.

```json
{
  "user": "<user>",
  "laptop_home": "/Users/<user>",
  "devbox_home": "/home/<devbox-user>",
  "devbox_host": "<devbox-host>",
  "orca_env": "<Orca environment id of the devbox's orca serve, as the laptop app sees it>",
  "work_repo": "/Users/<user>/Documents/<work-repo>",
  "brain_repo": "/Users/<user>/Documents/saahil-brain",
  "aws_sso_session": "<your SSO session name>",
  "github_cert": "/Users/<user>/.ssh/<your-cert>.pub",
  "default_pr_repo": "<org>/<repo>",
  "shadow_docs": [],
  "ticket_prefixes": ["<KEY>"],
  "legacy_docs": [],
  "devbox_instance_id": "i-<instance-id>",
  "devbox_region": "<aws-region>",
  "devbox_aws_profile": "<aws-profile>"
}
```

| Key | What it is | Used by | Default when missing (and no profile sets it) |
|---|---|---|---|
| `user` | your login name on the laptop | stopwords for the prompt hook, defaults below | `$USER` |
| `laptop_home` | your macOS home folder. **Every path the brain stores uses this spelling**, on both machines | every stored path, the index, hooks | on the laptop: the macOS home; on the devbox: `/Users/` + `user` |
| `devbox_home` | your Linux home folder on the devbox | the bind mount, path mapping (`/home/...` → `/Users/...`) | on the devbox: the Linux home; on the laptop: `/home/` + `user` |
| `devbox_host` | the ssh alias of the devbox in the laptop's `~/.ssh/config` | the bridge tunnel, `brain sync` (Unison), `brain agents`, `brain machine` | `user` + `-devbox` |
| `orca_env` | the id of the devbox's `orca serve` environment, as the laptop's Orca app knows it (`orca environment list --json` on the laptop) | `brain agents` from the laptop (screen, new, resume, move) | none: set it, or the laptop's `brain agents` commands cannot name the devbox's Orca runtime |
| `work_repo` | absolute path of your main work repo checkout | default repo of `brain agents new` | empty: `brain agents new` then needs `--repo`; features that need it say which key to set |
| `work_repo_orca` | the Orca repo selector for `work_repo` (optional) | `brain agents new` | `path:` + `work_repo` |
| `brain_repo` | absolute path of your copy of this repository on the devbox | `brain-care` and its goal template (`{{BRAIN_REPO}}`), `brain doctor` | `laptop_home` + `/Documents/saahil-brain` |
| `aws_sso_session` | the name of your AWS SSO session | `brain machine login aws`, `brain machine status` | empty: the features that need it say which key to set |
| `github_cert` | your GitHub SSH certificate | `brain machine status` (expiry) | empty: the features that need it say which key to set |
| `default_pr_repo` | the repo a bare PR number refers to in session notes (`#1234`) | session notes, index mentions | empty: the features that need it say which key to set |
| `shadow_docs` | old doc roots to index READ-ONLY before a cut-over. A new brain has none | index, dream, backfill, evals | `[]` (empty: nothing to shadow) |
| `ticket_prefixes` | your tracker's team keys: `ABC` makes `ABC-123` a ticket mention | index mentions | `[]`: no ticket mentions |
| `legacy_docs` | old doc-root spellings that history keeps, mapped under `work/`. Each entry is a path fragment (`*` is one folder, `**` any number), then optionally `=<box>`; without a box the first folder below the fragment is the box. Example: `Documents/<work-repo>/saahil-docs` | pointers, the link graph | `[]`: no mapping |

Three OPTIONAL keys are read only by `scripts/devbox` (the EC2 helper on the laptop). The `brain` loader ignores
keys it does not use; `brain identity` lists them as `file (scripts)`. `setup-devbox.sh` fills the first two from
the EC2 instance metadata and the third from its `--aws-profile` flag.

| Key | What it is | Default when missing |
|---|---|---|
| `devbox_instance_id` | the EC2 instance id of your devbox (`i-…`) | none: `devbox up/down/status` cannot reach the instance without it |
| `devbox_region` | the AWS region of that instance | the AWS CLI default region |
| `devbox_aws_profile` | the laptop AWS CLI profile allowed to start, stop and describe it | the AWS CLI default profile |

### Team profiles (`profiles/<team>.json`)

A profile holds only the values a whole team shares. It must never hold a personal value (a name, a home path, a
host or an id). Its paths are relative to the person's laptop home, and `--init` writes them with your own home. Git
keeps only the template, `profiles/example.json`:

| Key | Value in the profile |
|---|---|
| `aws_sso_session` | `Example-Session` |
| `github_cert` | `<laptop_home>/.ssh/work_github-cert.pub` |
| `default_pr_repo` | `example-org/work-repo` |
| `work_repo` | `<laptop_home>/Documents/work-repo` |

`brain identity --init --profile <team>` merges the profile UNDER your own keys: a value you pass as a flag, or one
already in your `identity.json`, wins over the profile. `scripts/setup-devbox.sh --profile <team>` and
`scripts/setup-laptop.sh <devbox-host> --profile <team>` pass it on. To start a profile for your team, copy
`profiles/example.json`, change its values and save it as `profiles/<team>.json` in your checkout. Git ignores it
(`.gitignore`), and `make build` puts every `profiles/*.json` into the binary, so a release kit built from that
checkout carries the profile while the repository does not. A profile at `~/.config/brain/profiles/<team>.json` wins
over the built-in one.

**Rules.**

- Spell every path as an absolute path. Never use `~` inside the file.
- Keep both copies of `identity.json` equal. A different `laptop_home` on the two machines splits every stored path
  in two.
- Never commit `identity.json` or a team profile. Only `profiles/example.json` is committed.
- `brain identity --init` keeps the keys that are already in the file. Use `--force` to replace them.
- Product names do not move into settings: the binary `brain`, the Claude Code mod `saahil-brain`, its MCP tool
  names `mcp__saahil-brain__brain_*`, the launchd labels `com.saahil.*` and the Go module path stay as they are.

### `brain identity` flags

```
brain identity                        print every effective value and its source
brain identity --init                 write ~/.config/brain/identity.json: these flags, then the file's values,
                                      then the profile, then derived ones
    [--profile <team>]                merge profiles/<team>.json under your own values
    [--user U]                        your LAPTOP login (on the devbox, $USER is the devbox login)
    [--devbox-host H] [--orca-env E] [--work-repo P] [--brain-repo P]
    [--devbox-home P] [--laptop-home P] [--aws-sso-session S] [--github-cert P] [--pr-repo R]
    [--force]                         a flag replaces a value the file already holds
```

## 2. Environment variables

You do not need any of them for normal use. They exist for tests, for one-off checks and for the model calls.

| Variable | Effect | Typical use |
|---|---|---|
| `BRAIN_OFF=1` | every brain hook and the mod's tools do nothing in that environment | a control run without the brain: `BRAIN_OFF=1 claude -p …` |
| `BRAIN_HOME` | replaces the home folder for every brain path (state, config, the brain itself) | tests in a temp folder |
| `BRAIN_ROOT` | puts the brain folder somewhere other than `<home>/brain` | a shadow copy for an experiment (the brain-care playbook does this) |
| `BRAIN_MACHINE` | `laptop` or `devbox`, instead of the answer from the OS | tests |
| `BRAIN_LLM_LOGIN=1` | model calls use the `claude` CLI's own sign-in (your Claude login) instead of an API key (`--bare`) | set in the `brain-worker` unit on the devbox; in an interactive laptop shell for a paid step (for example `brain eval build --human`) |

**How the brain calls a model.** Every LLM step runs `~/.local/bin/claude -p` in an empty temp folder, with no hooks,
no MCP servers and no settings. The devbox's `brain-worker` unit sets `BRAIN_LLM_LOGIN=1`, so the dream, learning and
the backfill run on YOUR Claude login there: sign in to Claude Code on the devbox once; no API key is needed. (When
`~/.local/bin/devbox-exec` exists, a wrapper some team devboxes have, calls go through it instead and get the same
provider and key as an interactive session.) A launchd job on the laptop cannot read the macOS keychain, so paid
steps on the laptop run from an interactive shell with `BRAIN_LLM_LOGIN=1`.

## 3. Fixed values (in the code, not in settings)

| What | Value |
|---|---|
| Models | judgment `claude-opus-5-5`, bulk `claude-haiku-5-5` |
| Cost guards | ≤ $8 per dream run; ≤ $100 per UTC day for all brain model calls; backfill ≤ $80 in total (`--budget`) |
| Caps | MEMORY.md ≤ 200 lines and ≤ 24 KiB · start digest ≤ 8,000 chars · ≤ 3 pointers, ≤ 300 tokens · hot.md ≤ 500 words · hub ≤ 200 lines · STATE.md ≤ 8 KB · CARD.md ≤ 3 KB · note ≤ 4 KB · lesson ≤ 300 chars · summary ≤ 160 chars |
| Hook budgets | start ≤ 300 ms; prompt p95 ≤ 150 ms; every hook fails open (no output, exit 0) |
| Bridge | laptop `127.0.0.1:7722`, Bearer token in `~/.config/brain/bridge.token` (mode 600, ≥ 32 chars) |
| Temporal | `127.0.0.1:7233`, web UI `127.0.0.1:8233`, namespace `brain`, history kept 30 days |
| Sync | Unison, laptop-started; files over 2 MB are skipped; under `work/` only text (`md txt py sh js toml yml yaml` and files with no extension) |

## 4. Files the brain owns

### On both machines

| Path | What | Written by |
|---|---|---|
| `~/brain/` | the brain itself (layout in [ARCHITECTURE.md](ARCHITECTURE.md#1-the-folder)) | you, agents, the dream |
| `~/brain/.index/brain.db` | the SQLite index. Per machine, never synced | `brain index`, hooks |
| `~/brain/.brain/live` | the brain is live: hooks inject, Claude Code memory points here, the dream writes in place | `brain init` (a fresh brain with no `shadow_docs`) |
| `~/.local/bin/brain` | the binary | setup scripts, `brain self-update` (laptop) |
| `~/.config/brain/identity.json` | your settings (section 1); never committed | `brain identity --init` |
| `~/.config/brain/bridge.token` | the bridge's shared secret, mode 600 | setup-devbox.sh makes it; setup-laptop.sh copies it |
| `~/.local/state/brain/` | logs and small state: hook timings, `sync-last.json`, `unison.log`, `bridge.log`, jobs | `brain` |
| `~/.local/state/agents/inbox/<session-id>/` | mesh notes waiting for one session; `delivered/` holds receipts | `brain agents note`, `brain hook inbox` |
| `~/.claude/rules/brain.md` | one line that @-imports `~/brain/BRAIN.md` into every Claude Code session | `brain install` |
| `~/.claude/settings.json` | `autoMemoryDirectory` = `<laptop_home>/brain/memory`; the mesh inbox hooks; the mod enabled | `brain install` (merges keys, keeps a backup) |
| `~/mods/saahil-mods/` | the local Claude Code marketplace that holds the `saahil-brain` mod | `brain install` (creates and registers it when missing) |
| `~/.codex/hooks.json`, `~/.codex/config.toml` | Codex hooks and the `brain` MCP server | `brain install` (when Codex is present) |
| `~/.cursor/hooks.json`, `~/.cursor/mcp.json` | Cursor hooks and MCP | `brain install` (when Cursor is present) |
| `~/.prime/agent/extensions/`, `~/.omp/agent/extensions/` | the Prime and omp extensions | `brain install` (when present) |
| global git ignore | the line `/brain` (root-anchored), so a `brain` link in a repo is never committed | `brain install` |

`brain install --off <host>` takes out exactly what it added for that host. `brain install --dry-run` shows the
changes first. Every changed file gets a backup.

### Only on the devbox

| Path | What |
|---|---|
| `~/.local/bin/brain-worker`, `~/.local/bin/temporal` | the Temporal worker and the Temporal CLI (dev server) |
| `~/.config/systemd/user/brain-temporal.service`, `brain-worker.service` | the local Temporal server and the worker |
| `~/.config/systemd/user/brain-care.service`, `brain-care.timer` | the daily care agent (15:00 America/Los_Angeles) |
| `~/.config/systemd/user/orca-serve.service` | Orca's headless runtime server |
| `~/.local/state/brain/temporal.db` | Temporal's state. Losing it loses only run history: every input is a file |
| `~/.local/bin/brain-care` | starts the care agent |
| `~/.local/share/brain/dist/` | the laptop binary, its `.sha256`, and `setup-laptop.sh`, for the laptop to fetch |
| `~/brain/.git` | the brain's history (text only; under `work/` only `CARD.md`) |
| `/etc/fstab` line `<devbox_home> <laptop_home> none bind,nofail 0 0` | the bind mount (see [DEVBOX-AND-ORCA.md](DEVBOX-AND-ORCA.md#2-the-bind-mount-why-paths-must-match)) |

### Only on the laptop

| Path | What |
|---|---|
| `~/Library/LaunchAgents/com.saahil.brain-bridge.plist` | `brain bridge serve` on `127.0.0.1:7722`, KeepAlive |
| `~/Library/LaunchAgents/com.saahil.brain-tunnel.plist` | `ssh -N -R 127.0.0.1:7722:127.0.0.1:7722 <devbox-host>`, so the devbox reaches the bridge |
| `~/Library/LaunchAgents/com.saahil.brain-sync.plist` | `brain sync` every hour and at load |
| `~/Library/Logs/brain-bridge.log`, `brain-tunnel.log` | the two agents' logs |
| `~/brain-view/` | the read-only Obsidian copy (`brain view`) |
