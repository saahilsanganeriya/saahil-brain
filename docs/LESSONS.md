# Lessons: what broke while the brain was built, and the rule each one left

Read this before you change the setup, and when something looks wrong. Each lesson happened on the reference setup
(2026-10-08 and 2026-10-09) and cost real time. Each one is written as **symptom → cause → fix → rule**. The fix is
already in this repository's code or scripts unless the lesson says otherwise; the rule is what you must keep doing.

| # | Lesson | Area |
|---|---|---|
| 1 | [A move across the two path spellings copies instead of renaming](#1-a-move-across-the-two-path-spellings-copies-instead-of-renaming) | bind mount |
| 2 | [The git snapshot hashed all of work/](#2-the-git-snapshot-hashed-all-of-work) | git |
| 3 | [Unison: bulk, the 2 MB limit, half-copies, a "dry run" that was live, a renamed laptop](#3-unison-bulk-the-2-mb-limit-half-copies-and-a-dry-run-that-was-live) | sync |
| 4 | [A link that loops back into work/ arrived by sync](#4-a-link-that-loops-back-into-work-arrived-by-sync) | sync |
| 5 | [launchd jobs hang on ~/Documents (TCC)](#5-launchd-jobs-hang-on-documents-tcc) | macOS |
| 6 | [The claude CLI hangs under launchd](#6-the-claude-cli-hangs-under-launchd) | macOS |
| 7 | [MEMORY.md was cut at 24.4 KB](#7-memorymd-was-cut-at-244-kb) | memory |
| 8 | [SQLITE_BUSY_SNAPSHOT on the dream's reindex](#8-sqlite_busy_snapshot-on-the-dreams-reindex) | index |
| 9 | [The MCP tools answered nothing (tool-name prefix)](#9-the-mcp-tools-answered-nothing-tool-name-prefix) | hosts |
| 10 | [CARDs went stale (the dream did not refresh them)](#10-cards-went-stale) | dream |
| 11 | [One rule, many copies; old rules contradicting new ones](#11-one-rule-many-copies-old-rules-contradicting-new-ones) | dream |
| 12 | [Evals that leak or misdate their targets](#12-evals-that-leak-or-misdate-their-targets) | evals |
| 13 | [A path rewrite reset 15,340 file times](#13-a-path-rewrite-reset-15340-file-times) | cut-over |
| 14 | [Cost caps: why they exist and where](#14-cost-caps) | cost |
| 15 | [A secrets guard blocks any command that names a secrets file](#15-a-secrets-guard-blocks-any-command-that-names-a-secrets-file) | safety |
| 16 | [Kill by PID, never by pattern](#16-kill-by-pid-never-by-pattern) | safety |
| 17 | [One worktree per task](#17-one-worktree-per-task) | git |
| 18 | [The dream reverted its own run (lint from the wrong folder)](#18-the-dream-reverted-its-own-run) | dream |
| 19 | [A token in a transcript reached a session note](#19-a-token-in-a-transcript-reached-a-session-note) | safety |
| 20 | [An unanchored ignore line hid code folders](#20-an-unanchored-ignore-line-hid-code-folders) | git |
| 21 | [A `brain` link in a repo can be uploaded or crawled](#21-a-brain-link-in-a-repo-can-be-uploaded-or-crawled) | repos |
| 22 | [Claude Code refused a symlinked plugin](#22-claude-code-refused-a-symlinked-plugin) | hosts |
| 23 | [`agents move` failed three ways before it worked](#23-agents-move-failed-three-ways-before-it-worked) | mesh |
| 24 | [`devbox down` said "already" when AWS failed](#24-devbox-down-said-already-when-aws-failed) | devbox |
| 25 | [Moving a cited file turned doctor red](#25-moving-a-cited-file-turned-doctor-red) | notes |
| 26 | [Gaps a fresh install exposed](#26-gaps-a-fresh-install-exposed) | setup |

---

## 1. A move across the two path spellings copies instead of renaming

- **Symptom.** A probe `mv` from `/home/<devbox-user>/…` to `/Users/<user>/…` on the devbox did not rename. It
  started to copy byte by byte.
- **Cause.** `/Users/<user>` is a bind mount of `/home/<devbox-user>`. The kernel treats the two spellings as two
  mounts, so `rename()` between them fails with `EXDEV` (cross-device link). `mv` then falls back to copy + delete.
  For a 528 GB docs folder that is hours of copying and a delete at the end.
- **Fix.** The devbox swap spelled source AND target under one spelling (`/home/<devbox-user>/…`), so it was one
  atomic rename in one process (rmdir + rename + symlink, about 10 µs apart).
- **Rule.** On the devbox, every `mv` of a big tree spells both paths the same way. Check first with
  `stat -c %d <source> <target-parent>` (same device number). Never move across the two spellings.

## 2. The git snapshot hashed all of work/

- **Symptom.** The 13:00 dream's snapshot step ran for a long time and `~/brain/.git` grew fast.
- **Cause.** The snapshot ran `git add -A` over the brain root. `work/` holds about 600 GB of results on the devbox,
  and git started to hash all of it into `.git`.
- **Fix.** The git process was stopped by PID. The brain's `.gitignore` now tracks only `CARD.md` under `work/`
  (`/work/*`, `!/work/*/`, `/work/*/*`, `!/work/*/CARD.md`). `.git` went from 6.4 GB to 5.7 MB.
- **Rule.** Git history covers brain text only. Never widen the brain's `.gitignore` to include box contents.

## 3. Unison: bulk, the 2 MB limit, half-copies, and a "dry run" that was live

- **Symptom A.** The first Unison "dry run" copied files. **Symptom B.** Each sync prints hundreds of "skipped:
  would transfer a file of size greater than maxsizethreshold" lines. **Symptom C.** `brain doctor` on the laptop
  went red two hours after a sync, on files named `.unison.*`. **Symptom D** (2026-10-10). Conflict copies of files
  that only one machine wrote, after the laptop moved to a different network.
- **Cause A.** `-noupdate` on both roots still CREATES new files. **Cause B.** By design: the sync carries text up to
  2 MB (`-maxsizethreshold 2048`, in KB); bulk stays on the machine that made it (the boxes hold ~600 GB on the
  devbox and ~220 GB on the laptop). **Cause C.** An interrupted sync leaves Unison temp files behind. **Cause D.**
  Unison names its archive after the local hostname. macOS changes the laptop's hostname with the network, so the
  devbox had two archives (`~/.unison/ar*`), one for each name. After a change, the sync used the other, older
  archive, and a file that changed after that archive was written read as changed on both replicas.
- **Fix.** `brain sync --dry-run` sets `-nocreation -noupdate -nodeletion` on both replicas (proven by a probe file
  that was skipped). Bulk folders (`artifacts`, `.venv`, `node_modules`, …) and everything but text under `work/` are
  ignored. Every sync archives a `.unison.*` file older than 2 h (`config.UnisonTempGrace`). `brain sync` runs
  Unison with `UNISONLOCALHOSTNAME` set to the machine name. The first sync with a new name has no archive, so each
  file that is different on the two replicas at that time becomes a conflict copy, one time only.
- **Rule.** Skip lines are normal; read `brain sync status` for the exit, not the scrollback. Put results that must
  reach the other machine in a text file, or copy a box on purpose with `brain agents box`. Never run Unison with
  `-watch` (editors already crawl these trees). Run Unison only through `brain sync`: a manual run uses the
  hostname and makes a second archive.

## 4. A link that loops back into work/ arrived by sync

- **Symptom.** `work/<old-docs-name>` appeared on the devbox: a symlink pointing back at the old path, which now
  resolved into `work/` itself.
- **Cause.** The laptop still had a compat link inside the tree; Unison carried it over.
- **Fix.** The link moved to `.archive/cutover/stray-links/`. `brain doctor` has a check, `brain.work-root`: no `.git`
  and no link back into the brain at the top of `work/`.
- **Rule.** Never put a link to the brain inside the brain. Keep compat links outside `~/brain`.

## 5. launchd jobs hang on ~/Documents (TCC)

- **Symptom.** A bridge step that walked `~/Documents/…` sat at 0 bytes for 3 minutes. A 1-second `sample` showed one
  thread in `__open` the whole time. It never failed; it just waited.
- **Cause.** macOS privacy control (TCC) guards `~/Documents`. A process started by launchd (the bridge, any
  LaunchAgent) has no terminal to show the permission prompt, so `open()` blocks forever instead of failing.
- **Fix.** The job was stopped by PID and run again from an interactive laptop terminal, which TCC allows. The brain
  lives at `~/brain`, outside `~/Documents`, so its own jobs never hit this.
- **Rule.** Laptop steps that read under `~/Documents` (moving old doc folders in, manifests of them) run from an
  interactive terminal, never through the bridge or a LaunchAgent. Keep `~/brain` outside `~/Documents`.

## 6. The claude CLI hangs under launchd

- **Symptom.** `brain install` on the laptop hung when the bridge ran it. A related case: after a launchd job ran
  `claude -p`, interactive sessions skipped an MCP server for 15 minutes.
- **Cause.** The `claude` CLI waits on things launchd cannot give it (the keychain, a full `PATH` with node). Claude
  Code also caches MCP connect failures in one file that every session shares, so one failed launchd run spoils the
  others.
- **Fix.** `brain doctor` checks Claude Code by reading its plugin registry and settings files, not by running
  `claude`. Paid model steps on the laptop run from an interactive shell (`BRAIN_LLM_LOGIN=1`). The brain's own
  model calls pass `--strict-mcp-config` or `--bare`, so they load no MCP servers.
- **Rule.** Never run the `claude` CLI from launchd. If you must, set `PATH` to include node and pass
  `--strict-mcp-config`. If an interactive session lost a server, run `/mcp` and reconnect.

## 7. MEMORY.md was cut at 24.4 KB

- **Symptom.** Claude Code printed a truncation warning, and rules at the end of `memory/MEMORY.md` never reached
  sessions.
- **Cause.** Claude Code loads only about 24.4 KB of `MEMORY.md` and cuts the rest, whatever the line count.
- **Fix.** The render cap is 200 lines AND 24 KiB (`config.MemoryIndexMaxBytes`). Lines are ordered so standing rules
  come first; a new rule that supersedes an old one takes its predecessor's place in the render. Each line carries
  the fact itself, not a description of it (a line that only describes needs a tool call to be useful).
- **Rule.** Never edit `MEMORY.md` by hand; the dream renders it. Watch its size in `brain doctor` (graph caps).

## 8. SQLITE_BUSY_SNAPSHOT on the dream's reindex

- **Symptom.** The 00:00 dream's reindex step failed with SQLite error 517 (`SQLITE_BUSY_SNAPSHOT`).
- **Cause.** A hook committed to the index between the reindex's read and its write. A deferred transaction cannot
  upgrade from a stale read snapshot, so SQLite refused it at once.
- **Fix.** Write transactions now begin `IMMEDIATE` (a test failed first). A failed dream step writes its last line to
  the worker log.
- **Rule.** Any new code that reads then writes the index opens its transaction `IMMEDIATE`.

## 9. The MCP tools answered nothing (tool-name prefix)

- **Symptom.** `brain_search` and the other tools returned nothing in Claude Code, for a whole day, with no error.
- **Cause.** Claude Code calls a mod tool as `mcp__saahil-brain__brain_search`. The mod looked up the bare name.
- **Fix.** The mod strips only its own prefix. Another server's tool with the same short name (for example a company
  MCP's `brain_search`) stays theirs. Verified in a fresh session.
- **Rule.** After any change to the mod, prove a tool call in a FRESH session, not in a running one (running
  sessions pick up the mod only at restart).

## 10. CARDs went stale

- **Symptom.** A box's CARD said "nothing merged" while its STATE listed 10 merged PRs. Every session in that box
  got the stale CARD in its digest.
- **Cause.** Only the backfill wrote CARDs; the hourly dream never refreshed them, although the design says it does.
  An earlier bug made every CARD look stale: the box's own `CARD.md` counted as its newest file.
- **Fix.** The dream step `curate.cards` refreshes up to 4 CARDs per dream whose box has work ≥ 1 h newer than the
  CARD (Haiku, inside the $8 cap). `CARD.md` itself is not work. `brain dream cards --dry-run` shows what it would pick.
- **Rule.** A baseline that a design says is kept current needs a step that keeps it current, and a dry run that
  shows it. Check new design claims against the dream's step list.

## 11. One rule, many copies; old rules contradicting new ones

- **Symptom.** `MEMORY.md` held the same rule three times; one global rule contradicted a newer one; an agent
  found the conflict only from Claude Code's truncation warning.
- **Cause.** Filing made about one note per old memory. The duplicate check caught byte copies only, and nothing
  checked global rules against each other.
- **Fix.** The dream step `curate.merge` (Opus) finds duplicate groups and conflicts among current global notes. It
  keeps the newest note with every distinct fact of the group, supersedes the rest (never deletes), narrows a conflict
  when dates settle it, and queues a pair it cannot settle in `.brain/owner-questions.jsonl`. `brain doctor` shows how
  many questions wait. `brain dream merge` is a dry run.
- **Rule.** Merge, don't copy. When a question waits, answer it (see [OPERATIONS.md](OPERATIONS.md#owner-questions));
  the newer dated rule wins only when the dates differ.
- **Later (2026-10-11): a narrowing that never landed.** For 12 dreams the merge narrowed the same lesson, and the
  applier refused it each time: "a lesson body is ≤ 300 chars". The narrowing line goes at the end of the older
  note's body. A lesson body holds at most 300 chars, and 108 of the 109 global lessons had no room for the line. The
  step stayed `ok`, and no metric counted the refusal. **Fix:** when the body has no room, the narrowing goes in the
  older note's `related` link to the newer note, as its why (`Narrowed <date>: <why>`). Each dream step record has a
  `refused` count, and `brain metrics` shows "dream units refused". **Rule:** a write that must fit a cap needs a
  second place to go. Count the units a step refused, not only the steps that failed.
- **Later (2026-10-12): the merge landed nothing at all.** The fix above worked in a replay, but the three dreams of
  the next day still changed no merged note. Each dream hit its 50-edit cap in learn, file and curate.link, and the cap
  went first come, first served, so every curate.hubs, curate.merge, curate.cards and verify edit was held (96 edits;
  $10 of the day's $13.54 bought edits that never landed). The step reasons still said "5 files" and "refreshed 2".
  **Fix:** learn and file keep first claim (they use up their input); the five curation steps share what is left
  equally. A held edit is marked `held`, and `brain metrics` shows "dream edits held by the cap". **Rule:** a shared
  budget spent in a fixed order needs shares, or the last steps never run.
- **Later (2026-10-13): verify paid for fixes that could not land.** The applier refused 109 of the 254 ops that
  verify proposed in its first 32 runs (43%), each for a cap: a summary over 160 chars, a lesson body over 300, a
  note over 4 KB. The verify skill named none of them, and its rule "append the reason" can never fit a lesson. So
  its status changes on lessons (retired, proposed) never landed, and it proposed the same ones again each run.
  "dream units refused" read 0, because only curate.merge counted refusals. **Fix:** verify states the caps and sets
  only the status of a lesson; every skill step counts its refused ops in `refused`; a test checks that each skill
  that writes note text states the three caps. **Rule:** a prompt that writes into a checked format states the
  checks, and a test keeps the two the same.
- **Later (2026-10-14): held edits that never came back.** Each dream of the day held curate.link edits (2 and 4)
  with "run edit cap 50 reached; waits for the next dream", but each run ended 13 to 14 edits under the cap. The
  edits were over curate.link's share, not the cap. They also never came back: link, hubs and verify judge only the
  proposals of their own run. Only curate.merge and curate.cards rebuild from the whole brain. **Fix:** a held edit
  names the share that held it and says whether it waits or is `lost`; `brain metrics` shows "dream edits lost to the
  cap". **Rule:** a record of a deferral says whether the deferred work can return.
- **Later (2026-10-15): the cap counted a rewrite as a new edit.** Both dreams of the day lost curate.link edits to
  its share (5 and 10), and each lost edit was a link on a note that learn or file had written earlier in the same
  run. The runs used 50 and 49 edits but changed only 31 and 28 files: verify and curate.link rewrite the run's own
  new notes, and each rewrite used the share again. **Fix:** the cap counts files. A write to a file the run already
  changed costs nothing; a step's share is in new files; `brain dream status` shows "N edits on M files". **Rule:** a
  cap counts the unit it protects.
- **Later (2026-10-16): a dream call could use tools.** One dream skipped merge, cards, summarise and verify at the
  $8 cap: two reflect calls cost $1.39 and $1.73 and read 1.9 M and 2.7 M input tokens for a 6 KB session note.
  `--bare` still reads the user's permission mode, and it was `bypassPermissions`: a probe call ran Bash and read a
  file in the live brain. The model opened the reports the session wrote, and the notes earlier runs had learned
  from it. **Fix:** every call gets `--tools ""`. Reflect's input carries the session's produced documents and the
  notes that already cite the session. **Rule:** what a model call may use goes in its input; a call has no tools.

## 12. Evals that leak or misdate their targets

- **Symptom.** Early navigation numbers looked better than real use felt. After the cut-over, the same evals dropped
  for no real reason.
- **Cause.** Targets leaked: a file the session itself wrote counted as "found"; an echoed brief counted as a target;
  bulk folders counted; the ranking used the brain as of today, not as of the session. After the cut-over, dates, old
  path spellings and namesake folders confused the matcher.
- **Fix.** A target must have existed before the session (birth time, mtime or an earlier session's touch); bulk is
  never a target; ranking runs as of the session's start; old spellings map to new paths. DEV is for tuning; TEST is
  run once. The headline eval is real: prompts you typed and the file that answered (`brain eval build --human`,
  Opus-judged, never storing prompt text).
- **Rule.** Never edit eval sets, targets or marks to pass. Judge a change on TEST once, and on the owner-typed eval.

## 13. A path rewrite reset 15,340 file times

- **Symptom.** After the cut-over's path rewrite, every rewritten file looked new, so every CARD looked stale and
  "newest file" logic broke.
- **Cause.** The rewrite replaced files with new bytes and kept the new mtime.
- **Fix.** `brain cutover restore-mtimes` put the old times back from the rewrite manifests (only on files unchanged
  since). `rewrite-paths` now keeps each file's mtime.
- **Rule.** Any tool that rewrites files in place keeps their mtime. Moves (rename) keep mtimes for free; prefer a
  move to a copy.

## 14. Cost caps

- **Symptom (prevented).** A loop or a retry storm in an unattended job can spend without limit.
- **Cause.** Dreams, learning and the backfill call models with nobody watching.
- **Fix.** Three caps in code: ≤ $8 per dream run (each step must fit its budget first: Opus $2.50, Haiku $0.50); ≤
  $100 per UTC day for all brain model calls, failed attempts included; backfill ≤ $80 in total (`--budget`). Every
  answer is cached by an idempotency key, so a retry or rerun never pays twice. On the reference setup the most
  expensive dream cost $5.97, and the busiest 24 hours (backfill included) about $75.
- **Rule.** Keep the caps. Raise the backfill `--budget` on purpose, not by removing the guard. Interactive and Orca
  agent sessions are not in this ledger.

## 15. A secrets guard blocks any command that names a secrets file

- **Symptom.** On the reference laptop a guard mod refused commands, even a `grep`, that named a secrets file. An
  earlier probe had printed a key's prefix and hash.
- **Cause.** Secrets files must never enter a conversation or a log; the guard blocks any command that names one.
- **Fix.** The brain's file tools never open `.env*` files (`rewrite-paths` skips them unread), and setup takes
  credentials only through the tools that own them (SSO, the GitHub certificate flow).
- **Rule.** Never open, print or copy a secrets file. Refer to them generically. Never print a key's prefix, length
  or hash.

## 16. Kill by PID, never by pattern

- **Symptom.** `pkill -f <pattern>` killed the agent's own shell (exit 144). Wait loops on `pgrep -f` never ended.
- **Cause.** An agent's Bash tool wraps each command in `bash -c '<command>'`, so the pattern always matches a live
  process: the agent's own.
- **Fix.** Stop a process by the PID a tool reported, or pick PIDs by an exact field match (`ps -eo pid,args` and
  test the program field).
- **Rule.** Never `pkill -f`. Wait on a PID (`while kill -0 $PID`), a marker file or a log line.

## 17. One worktree per task

- **Symptom.** Two tasks in one checkout mixed into one diff; a `git checkout -b` in a shared worktree moved another
  live session's checkout.
- **Cause.** Agents start in whatever checkout they were launched in, often one that another session uses.
- **Fix.** Each net-new task gets its own worktree off fresh `origin/main` (`brain agents new` does this through
  Orca). The daily care agent works the same way and removes its worktree and branch at the end.
- **Rule.** One task, one worktree, removed when its branch lands. Never `checkout -b` inside a worktree you were
  launched in.

## 18. The dream reverted its own run

- **Symptom.** A dream spent $3.78 and every edit was reverted.
- **Cause.** The check step ran `brain lint` on brain-relative paths from another working folder, so lint failed
  (exit 2) and the safety net restored every file.
- **Fix.** Dream commands run from the brain root, and lint resolves brain-relative paths. The cached answers made the
  rerun free.
- **Rule.** The revert-on-lint-failure net works; keep it. A dream that reverts everything is a tooling bug, not a
  content bug: read the run report in `.brain/dream-runs/` first.

## 19. A token in a transcript reached a session note

- **Symptom.** A session note held a bare 43-character local MCP token from an old transcript.
- **Cause.** The first redactor caught known key shapes only, not a bare high-entropy token.
- **Fix.** The redactor redacts bare high-entropy tokens; every session note was rewritten; the token was purged from
  the brain's local git history; lint warns (never errors) on random-looking tokens, so a document id cannot revert
  a dream. The original transcript still held the token, so it had to be rotated at its source.
- **Rule.** Treat any secret found in the brain as leaked: rotate it at the source, then clean the copies.

## 20. An unanchored ignore line hid code folders

- **Symptom.** `cmd/brain` vanished from `git status` in the brain's code repo.
- **Cause.** The global git ignore line `brain` (to keep `brain` links out of commits) also matched every folder named
  `brain`.
- **Fix.** `brain install` writes the anchored form `/brain` (repo root only) and drops the old forms. A test pins it.
- **Rule.** Anchor ignore lines that target one root entry.

## 21. A `brain` link in a repo can be uploaded or crawled

- **Symptom (found before it happened).** `gcloud builds submit` from a work repo would have uploaded the whole brain.
  An editor's TypeScript server held 888k of 1.03M inotify watches by following an old docs link.
- **Cause.** gcloud follows symlinks when it collects files, unless `.gcloudignore` names the link. Language servers
  and file watchers follow links too.
- **Fix.** The reference setup keeps a `brain` link in every repo root (git-ignored by `/brain`) by the owner's choice.
  Agents do not need it: BRAIN.md is loaded everywhere and paths are absolute.
- **Rule.** Before you add `brain` links to a repo, check that repo's upload tools (`.gcloudignore`,
  `.dockerignore`) and editor watchers. Never commit the link. Never edit a company repo's ignore files for the brain.

## 22. Claude Code refused a symlinked plugin

- **Symptom.** The mod did not load when its folder was a symlink to the code repo.
- **Cause.** Claude Code refuses plugin files that resolve outside their marketplace folder. A directory marketplace
  plugin also needs `claude plugin install` before it loads.
- **Fix.** `brain install` writes the mod's files into the marketplace folder as ordinary files and runs
  `claude plugin install` for it.
- **Rule.** Install the mod with `brain install`, not by linking.

## 23. `agents move` failed three ways before it worked

- **Symptom.** Moving a laptop session to the devbox failed with `selector_not_found`, then stopped at a trust
  question, then aborted on one file.
- **Cause.** Orca opens terminals only at a project root; a folder new to the devbox stops at Claude Code's
  workspace-trust question; the notes sync aborted on a file another build was writing.
- **Fix.** `move` opens the Orca terminal at the project root and starts the session in its subfolder, pre-trusts
  the target folder, and tolerates a file in flight. Then it ran live: resumed with history, renamed, listed, stopped.
- **Rule.** Prove a mesh command with a live run on a throwaway session before you rely on it.

## 24. `devbox down` said "already" when AWS failed

- **Symptom.** With an expired AWS sign-in, `devbox down` printed `already ` and exited 0. It stopped nothing.
- **Cause.** The instance-state read returned an empty string on error, and the script treated it as a state.
- **Fix.** A failed state read is fatal: the helper prints "cannot read the instance state", names the sign-in to
  renew, and exits 1. Tested with a stub `aws` on `PATH`, never against the real instance.
- **Rule.** A helper that controls a machine fails loudly when it cannot read the machine. Test stop paths with stubs.

## 25. Moving a cited file turned doctor red

- **Symptom.** `brain doctor` went red on both machines for 20 minutes after a test memory was moved to `.archive/`.
- **Cause.** A filed note cited that memory as evidence, so the link dangled.
- **Fix.** The memory went back.
- **Rule.** Before you move a file out of the live tree, run `brain show <path>` and check what links to it.
- **Later (2026-10-12): code moved cited files too.** A session note's file name held the session's name, and a
  `/rename` moved the note. Three renamed sessions left 103 dangling citations in 56 notes. The applier checks every
  path in a note it edits, so each later edit of those notes was refused, merges into them included. **Fix:** a
  session note keeps the path of its first write (a rename changes `name:`), and the applier moves a cited session
  path with an old slug onto the session's one note when it edits the note. An empty `update_note` delta repaired
  the 56 notes.

## 26. Gaps a fresh install exposed

The reference brain went through a shadow phase and a hand-run cut-over, on one person's machines. Packaging it for
a new person showed steps that only ever happened by hand, and values that only fit the first owner:

- **The live marker.** Nothing wrote `~/brain/.brain/live`; it was written by hand at the cut-over. Without it the
  hooks stay in shadow (they log and inject nothing), `brain install` never sets Claude Code's `autoMemoryDirectory`,
  `brain dream schedule` defaults to shadow, and the backfill stages CARDs instead of writing them. **Fix:** `brain
  init` marks a fresh brain live when the marker is absent and `shadow_docs` is empty (a new brain has nothing to cut
  over). **Check:** `cat ~/brain/.brain/live` prints a UTC time.
- **The mod's marketplace.** `brain install` wired the Claude Code mod only when the owner's local marketplace folder
  already existed and was registered with Claude Code. A new person has neither, so Claude Code would get the rules
  file and the mesh hooks but no digest, pointers, guard or `brain_*` tools. **Fix:** `brain install` creates
  `~/mods/saahil-mods/.claude-plugin/marketplace.json` when it is missing, registers it, then installs the mod.
  **Check:** `claude plugin list` shows `saahil-brain`, and `brain doctor` shows `host.claude … mod ✓ · loaded ✓`.
- **The worker's model login.** The worker ran `claude --bare -p`, which needs an API key, or a team wrapper
  (`devbox-exec`) that the first owner's devbox had. A systemd unit gets no shell profile, so on a new devbox the
  dream's model calls would fail auth. **Fix:** the `brain-worker` unit template sets `BRAIN_LLM_LOGIN=1`: the
  calls use the person's own Claude login on the devbox; no API key. **Check:** `systemctl --user show brain-worker
  -p Environment` contains `BRAIN_LLM_LOGIN=1`.
- **The care agent's notification.** The care goal ended with a notifier command that only the first owner had
  installed. **Fix:** the notify step runs only when a notifier is installed; the report is the record either way.
- **Seed hubs and code defaults.** The seed hubs were the first owner's topics, and code defaults held one team's
  SSO session, certificate path and PR repo. **Fix:** `brain init` seeds only generic hubs (the person adds their
  own domains); code defaults are generic; team values live in a team profile (`profiles/<team>.json`, kept out of
  git and built into the binary, merged by `brain identity --init --profile <team>`); personal values stay in the
  never-committed `identity.json`. The
  Makefile's dist path comes from settings, not a fixed home.
- **Rule.** A step that ran by hand once is a step a new install will miss, and a value that fits one person is a
  bug for the next. Put the step in code with a check, and the value in settings.
