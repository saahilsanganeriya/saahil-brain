# devbox-agent → brain parity

Every subcommand of `~/.local/bin/devbox-agent` (541-line Python) and its `brain` equivalent. `scripts/devbox-agent-alias`
maps each old command line to the new one unchanged; it is not installed over the real `devbox-agent` until the live
parity runs below pass.

Recorded runs: 2026-10-08 on the devbox, read-only.

**Naming.** An agent name may be prefixed `devbox:` or `laptop:`; a bare name is a devbox agent, as it always was for
`devbox-agent` (unlike `brain agents note`, where a bare name is this machine — the alias adds `devbox:` for `note`).
A name resolves, in order, through the shared registry `~/.local/state/agents/orca.json` (name → Orca tab and
folder, same file and shape as `devbox-agent`), then a live session `/rename`d to it that runs in an Orca terminal,
then a Happier (tmux) agent in `names.json`.

**Where it runs.**

```
 from \ target    devbox agent                                   laptop agent
 laptop           ssh devbox-host 'bash -lc …'                 local bash + orca
                  + orca --environment <ORCA_ENV>
 devbox           local bash + orca (in Orca) | orca-ide         bridge: rpc op "screen" only (read-only);
                                                                 every other command refuses with a message
```

| devbox-agent | brain | how verified |
|---|---|---|
| `list` | `brain agents list` (mesh, already shipped) — live sessions on BOTH machines as `<machine>:<name>` from the session registries; Happier agents are listed only as the live Claude sessions they are | `TestListSkipsDeadSessions`; recorded run on the devbox (`brain agents list --local`, 11 sessions) |
| `screen <name> [--lines N]` | `brain agents screen [machine:]<name> [--lines N]` — Orca terminal tail, else the Happier tmux pane; prints how many background agents it waits on | recorded run on the devbox: `brain agents screen image-unified-f1 --lines 5` printed its 5-line screen (found through its `/rename` name; orca.json is empty); `laptop:` from the devbox reaches the bridge (answers `unknown op "screen"` until the laptop runs this build) |
| `attach [name]` | `brain agents attach [name]` — replaces this process with `agent-attach` (ssh -t from the laptop, direct on the devbox); an Orca agent is refused with the same "open it in the Orca app" message | build only (it takes over the terminal); coordinator check |
| `new <name> [--base REF] [--dir D]` | `brain agents new <name> [--base REF] [--dir D]` — `git fetch` + `orca worktree create --no-parent --agent claude`, registers the name, `/rename`s the session | coordinator live parity run on a throwaway agent (workers never start agents) |
| `goal <name> <file> [done]` | `brain agents goal <name> <file> [done]` — Orca: types `/goal `, the FULL text (≤ 4,000 chars, refused above), Enter; success only on a NEW `Goal set: <probe>` line. Happier: one short `/goal` line pointing at the file | `TestGoalSetsFullTextVerified` (new line required, older line not counted, > 4,000 refused before typing); coordinator live run |
| `note <name> "…"` | `brain agents note <machine>:<name> "…"` (mesh, already shipped) — **semantics replaced**: the Happier chat send + typed prompt become an inbox file keyed by the target's session id, delivered by the inbox hook, plus an Orca nudge for an idle terminal; still prints DELIVERED / QUEUED. `--wait` is a duration (`360s`), not a number of seconds | `TestDeliversOnceBySessionID`, `TestNoteNeverReachesAnotherSessionInSameFolder`, `TestNudgesOnlyIdleOrcaTerminals` |
| `resume <name> <session> [--force]` | `brain agents resume <name> <session> [--force]` — exactly one devbox transcript, refused if written < 120 s ago, resumed in Orca in the transcript's recorded folder | `TestResumeRefusesFreshTranscript` (119 s refused, 119 s + `--force` and 121 s start); coordinator live run |
| `move <session> <name> [--force]` | `brain agents move <session> <name> [--force]` — laptop only: worktree via `devbox-handoff`, transcript + subagents + file history + session env via rsync, `devbox sync`, resume in Orca | refusal recorded on the devbox ("run it on the laptop"); first laptop run by the owner |
| `stop <name> [--rm-worktree]` | `brain agents stop [machine:]<name> [--rm-worktree]` — closes the Orca terminal and drops the name (Happier: `agent-stop`); removes only a worktree strictly under `~/Documents/worktrees/work-repo` after cleaning the path, never with `--force` | `TestStopRemovesOnlyWorktreesUnderRoot` (real git: a sibling `<root>-old`, a `<root>/../…` spelling and a dirty worktree survive); coordinator live run |
| `box <box> [--exclude P]… [--with-venv]` | `brain agents box <box> [--exclude P]… [--with-venv]` — laptop only; `rsync -a --update --compress`, venvs skipped unless asked, never `--delete` | `TestBoxNeverDeletes` (no `--del*` flag even for `--exclude --delete`); refusal recorded on the devbox |
| `login [github\|aws\|gcloud]…` | `brain machine login [github\|aws\|gcloud]…` — laptop: the same ssh flows (gcloud remote bootstrap through a devbox tmux session; the account comes from `gcloud config get-value account` instead of being written in the code); devbox: the same sign-ins in this terminal | refusal of an unknown service recorded; sign-ins not run (they renew real credentials) |
| `up` | `brain machine up` — laptop: `exec ~/.local/bin/devbox up …`; devbox: says it is already up | build only |
| `down` | `brain machine down [--force]` — laptop: `exec devbox down …`; devbox: refused (it must sync to the laptop first) | refusal recorded on the devbox |
| `status` | `brain machine status` — laptop: `exec devbox status`; devbox: host, uptime, load, GitHub cert expiry, AWS SSO expiry | recorded run on the devbox (cert valid 15 h, SSO valid 26 min) |
| `sync` | `brain machine sync` — laptop: `exec devbox sync` (to be replaced by `brain sync`, Unison, in its own task); devbox: refused | build only |
| — | `brain machine resize …` — laptop: `exec devbox resize …`; devbox: refused | build only |

Every command was also checked by `make build` (linux/amd64 and darwin/arm64) and `go vet` for both targets.
