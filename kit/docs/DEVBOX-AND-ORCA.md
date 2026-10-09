# The devbox and Orca

Read this before step 0 of [AGENT-SETUP.md](../AGENT-SETUP.md). Step 0 is yours, not your agent's: it needs your
AWS account, `sudo` and the Orca app.

**In plain words.** A *devbox* is a Linux machine in the cloud (an AWS EC2 instance) that stays on while your laptop
sleeps. The brain needs one. The dream, the Temporal server, the daily care agent and most long agent runs live
there. Your laptop keeps its own copy of the brain and syncs with the devbox every hour. **Laptop-only is not
supported:** without the devbox nothing files new memories, refreshes CARDs or checks the brain's health.

*Orca* is the multi-agent IDE you use on the laptop. On the devbox, Orca runs headless as `orca serve`, and the
laptop's Orca app connects to it as a remote *environment*. Agents started there keep running when the laptop
sleeps.

```
 LAPTOP (macOS)                                              DEVBOX (EC2, Ubuntu)
 ┌───────────────────────────────┐                         ┌──────────────────────────────────────────┐
 │ Orca app ── local runtime     │  ssh -L 16768:…:6768    │ orca serve :6768 (systemd --user)         │
 │   environment "<devbox>" ─────┼────────────────────────►│   └ agents in worktrees, keep running     │
 │ brain bridge 127.0.0.1:7722   │◄── ssh -R 7722 ─────────│ brain peer / agents note laptop:<name>    │
 │ brain sync (Unison) ──────────┼──── ssh ───────────────►│ /usr/bin/unison                           │
 │ browser ──────────────────────┼── ssh -L 8233 (on ask) ►│ Temporal UI 127.0.0.1:8233                 │
 │ scripts/devbox up|down|status ┼── AWS EC2 API ─────────►│ the instance itself                       │
 └───────────────────────────────┘                         │ /home/<devbox-user> ═ bind ═► /Users/<user>│
                                                           └──────────────────────────────────────────┘
```

Contents: [1 create the devbox](#1-create-the-devbox) · [2 the bind mount](#2-the-bind-mount-why-paths-must-match) ·
[3 orca serve and pairing](#3-orca-serve-and-pairing) · [4 tunnels](#4-tunnels) · [5 the devbox helper](#5-the-devbox-helper) ·
[6 sign-ins](#6-sign-ins) · [7 security](#7-security)

## 1. Create the devbox

**[HUMAN]** You create the instance with your own AWS access, the way your team does.

| Need | Minimum | Why |
|---|---|---|
| OS | Ubuntu (24.04 LTS on the reference setup), x86_64 | the kit's Linux binaries are `linux-amd64` |
| CPU / RAM | ≥ 8 vCPU, ≥ 32 GB | Temporal, the worker, Orca and a few agents at once |
| Disk | ≥ 200 GB gp3, `DeleteOnTermination=false` | boxes, worktrees and results grow; the disk must outlive the instance |
| Address | an Elastic IP | the ssh alias keeps working after a stop and start |
| Security group | inbound tcp/22 from your own addresses only | every other service (Orca 6768, Temporal, the bridge) is reached through ssh |
| Packages | `git`, `unison` (apt, 2.52 or newer, at `/usr/bin/unison`), Claude Code in `~/.local/bin/claude` | the sync calls `/usr/bin/unison` on the devbox; the dream calls `claude -p` |

Sizing guide (AWS us-west-2 on-demand Linux prices, read 2026-09-30; compute is not billed while the instance is
stopped, the disk is):

| Type | vCPU | RAM GiB | ≈ $/month 24×7 | Good for |
|---|---:|---:|---:|---|
| m7i.2xlarge | 8 | 32 | 290 | the brain + 1–2 agents with tests (the minimum) |
| m7i.4xlarge | 16 | 64 | 581 | 3–4 agents |
| m7i.8xlarge | 32 | 128 | 1,161 | the reference setup: many agents and heavy builds |
| gp3 disk | — | — | 0.08 per GB-month | 600 GB ≈ $48/month, billed while stopped |

Stay on x86_64: a Graviton type (m7g, c7g, r7g) cannot boot an x86 disk.

**The ssh alias.** On the laptop, add a block to `~/.ssh/config`. The name is your `devbox_host` setting.

```
Host <devbox-host>
  HostName <elastic-ip>
  User <devbox-user>
  IdentityFile ~/.ssh/<your-key>
  ServerAliveInterval 30
  ServerAliveCountMax 3
```

On the devbox, let sshd drop dead clients within 3 minutes, so a dead tunnel frees its port by itself. Put this in
`/etc/ssh/sshd_config.d/10-devbox.conf` and reload sshd:

```
ClientAliveInterval 60
ClientAliveCountMax 3
```

**Check:** `ssh -o BatchMode=yes <devbox-host> 'uname -sm; nproc; free -g | head -2; df -h ~ | tail -1'` prints
`Linux x86_64`, ≥ 8, ≥ 32 GB and ≥ 200 GB without asking for a password.

## 2. The bind mount: why paths must match

**In plain words.** The devbox shows its own home folder a second time at your laptop's home path. Then
`/Users/<user>/brain/notes/x.md` is a real path on BOTH machines, so anything that stores an absolute path stays
valid on either one.

```
 devbox disk:   /home/<devbox-user>/brain/notes/x.md
                        ║  bind mount (same folder, second name)
 devbox path:   /Users/<user>/brain/notes/x.md   ◄── the spelling every stored path uses
 laptop path:   /Users/<user>/brain/notes/x.md   ◄── same string
```

| What stores an absolute path | What breaks without the mount |
|---|---|
| Claude Code transcript folders (named after the session's folder) and memory keys | a session moved to the devbox starts with no history and no memory |
| the brain's index, session notes, evidence links | every link written on one machine dangles on the other |
| hook commands in `settings.json`, the mod, the worker's units | hooks point at a binary path that does not exist |
| Orca worktrees and agent folders | `brain agents move` and `resume` cannot find the folder |

**How it is made** (`scripts/setup-devbox.sh` does this, with `sudo`):

```
/etc/fstab:   /home/<devbox-user> /Users/<user> none bind,nofail 0 0
sudo mkdir -p /Users/<user> && sudo mount /Users/<user>
```

**Check:**

```
$ findmnt -n -o TARGET /Users/<user>
/Users/<user>
$ test "$(stat -c %i /home/<devbox-user>)" = "$(stat -c %i /Users/<user>)" && echo same-folder
same-folder
```

**Rules that follow from it.**

- Start agents and register Orca projects from `/Users/<user>/…`, never from `/home/<devbox-user>/…`. Two spellings
  split Claude Code's memory keys and transcript folders.
- Never `mv` a big tree from one spelling to the other: the kernel sees two mounts, `rename()` fails with `EXDEV`,
  and `mv` silently copies and deletes instead ([LESSONS.md](LESSONS.md#1-a-move-across-the-two-path-spellings-copies-instead-of-renaming)).
- Remove a git worktree from the same spelling it was registered under.

## 3. orca serve and pairing

**[HUMAN]** Installing Orca and pairing it with the laptop app need you.

**Install.** Put the Orca Linux AppImage at `/opt/orca/orca-linux.AppImage` (root-owned, executable), from Orca's
official release. On Linux outside an Orca terminal, the CLI is `orca-ide`; a bare `orca` may be another program.

**Run it as a user service.** The kit ships `deploy/systemd/orca-serve.service` (rendered by `setup-devbox.sh`):

```
[Service]
WorkingDirectory=/Users/<user>
Environment=LIBGL_ALWAYS_SOFTWARE=1
ExecStart=/opt/orca/orca-linux.AppImage serve --port 6768 --pairing-address 127.0.0.1:16768 --json
Restart=on-failure
```

```
systemctl --user daemon-reload && systemctl --user enable --now orca-serve
sudo loginctl enable-linger <devbox-user>      # user services keep running with nobody logged in
```

**Check:** `systemctl --user is-active orca-serve` prints `active`; `loginctl show-user <devbox-user> -p Linger`
prints `Linger=yes`.

**Why port 16768.** The laptop's own Orca runtime already listens on `127.0.0.1:6768`, so the laptop forwards
`127.0.0.1:16768` to the devbox's `6768`, and the server advertises that address in its pairing link.

**The laptop tunnel.** Keep this forward up with a LaunchAgent (KeepAlive), so it comes back after sleep:

```
/usr/bin/ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o BatchMode=yes \
    -L 16768:127.0.0.1:6768 <devbox-host>
```

**Pair.** The server prints its pairing link once at start, and that line does not reach the journal. Mint it by hand:

1. On the devbox: `systemctl --user stop orca-serve`.
2. In a devbox terminal: `script -q -f -c '/opt/orca/orca-linux.AppImage serve --port 6768 --pairing-address 127.0.0.1:16768 --json' ~/orca-pair.log`
3. Copy the `orca://pair?code=…` link from the output.
4. On the laptop: `orca environment add --name <devbox-host>-server --pairing-code '<link>'` (or add the
   environment in the Orca app).
5. On the devbox: stop the foreground server (Ctrl+C), delete `~/orca-pair.log` (it holds a pairing secret), then
   `systemctl --user start orca-serve`.
6. On the laptop: `orca environment list --json`. The new environment's id is your `orca_env` setting.

**Register projects on the devbox server** with the laptop spelling of the path (in an Orca terminal on the devbox):

```
orca repo add --path /Users/<user>/Documents/<work-repo>      # your work_repo
orca repo add --path /Users/<user>/Documents/saahil-brain     # your brain_repo (the daily care agent needs it)
```

**Check:** the laptop's Orca app shows the devbox environment connected and both projects listed.

## 4. Tunnels

Every connection between the machines rides on ssh. Nothing listens on a public port.

| Tunnel | Command shape | Started by | What uses it |
|---|---|---|---|
| Orca | laptop `ssh -N -L 16768:127.0.0.1:6768 <devbox-host>` | your LaunchAgent (step 0) | the laptop Orca app → `orca serve` |
| brain bridge | laptop `ssh -N -R 127.0.0.1:7722:127.0.0.1:7722 <devbox-host>` | `com.saahil.brain-tunnel` (`brain bridge install`) | the devbox → the laptop's `brain bridge` (mesh, peer steps, sync) |
| sync | laptop `ssh <devbox-host>` running `/usr/bin/unison` | `com.saahil.brain-sync`, `brain sync` | Unison |
| Temporal UI | laptop `ssh -N -L 8233:127.0.0.1:8233 <devbox-host>` | you, when you want the UI | http://127.0.0.1:8233/namespaces/brain/workflows |

**Why the laptop dials out.** A company-managed Mac has its ssh server off by policy, and turning on Remote Login
does not start it. The devbox therefore cannot ssh into the laptop. The laptop opens every connection; the reverse
forward (`-R`) is how the devbox talks back, and only to the token-checked bridge on the laptop's loopback.

If a tunnel is down: `launchctl kickstart -k gui/$(id -u)/com.saahil.brain-tunnel` on the laptop, and read
`~/Library/Logs/brain-tunnel.log`. A stale `7722` listener on the devbox clears by itself within 3 minutes
(section 1, sshd keepalive).

## 5. The devbox helper

`scripts/devbox` is a small laptop script that controls the instance through the AWS EC2 API. `setup-laptop.sh`
installs it as `~/.local/bin/devbox`. `brain machine up|down|status|sync` on the laptop run this same script.

| Command | Does |
|---|---|
| `devbox up` | start the instance (~1–2 min), wait for ssh, run `brain sync` |
| `devbox status` | instance state, uptime and load, GitHub certificate expiry, AWS SSO state |
| `devbox sync` | `brain sync` |
| `devbox down [--force]` | refuse while heavy processes (`go test`, `kubectl`, `depot`) or Claude sessions run; else `brain sync`, then stop the instance |

It reads three optional keys from `~/.config/brain/identity.json`: `devbox_instance_id`, `devbox_region` and
`devbox_aws_profile` (region and profile fall back to the AWS CLI defaults); see [CONFIG.md](CONFIG.md).
`setup-devbox.sh` fills the instance id and region from the EC2 instance metadata, and the profile from its
`--aws-profile` flag.

**Guards that must stay.**

- `down` refuses while work runs. Stopping the instance ends every agent on it; transcripts survive, and
  `brain agents resume <name> <session>` brings one back.
- A failed instance-state read is fatal: the helper names the sign-in to renew and exits 1. It never prints a blank
  "already" ([LESSONS.md](LESSONS.md#24-devbox-down-said-already-when-aws-failed)).
- Test `down` with a stub `aws` on `PATH`, never against the real instance.

## 6. Sign-ins

| Sign-in | Lifetime | Renew | Symptom when it lapses |
|---|---|---|---|
| GitHub (SSH certificate, `github_cert`) | hours (company flow) | `brain machine login github`, or your team's flow | `git push`: `Permission denied (publickey)` |
| AWS SSO (`aws_sso_session`) | the SSO session length | `brain machine login aws` | `devbox up`/`status`: `Token has expired`; `brain machine status` shows it |
| gcloud | until Google asks again | `brain machine login gcloud` | `Reauthentication required` |
| Claude (for the dream's model calls) | persistent | an API key in the worker's environment, `~/.local/bin/devbox-exec`, or `Environment=BRAIN_LLM_LOGIN=1` on `brain-worker` with Claude Code signed in ([CONFIG.md](CONFIG.md#2-environment-variables)) | dream steps fail with an auth error in `.brain/dream-runs/` |

`brain machine login` drives the same sign-in helpers as your team's devbox setup. Each sign-in is a browser flow:
**[HUMAN]**.

## 7. Security

- No public ports. Orca's `6768` listens on all interfaces (it has no bind flag), so the security group must keep it
  closed; reach it through the ssh forward only.
- The bridge listens only on the laptop's `127.0.0.1`, needs the token, answers an allow-list of named steps, and
  logs every request without its text. It is not a shell.
- Temporal and its UI bind to the devbox's `127.0.0.1`. The brain's Temporal is local and personal; never point it
  at a company Temporal Cloud account.
- Brain data (docs, notes, sessions) never leaves the two machines, except as input to model API calls, as every
  agent session already does.
