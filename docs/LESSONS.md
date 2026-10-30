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
| 27 | [Idle sessions were learned again in every dream](#27-idle-sessions-were-learned-again-in-every-dream) | dream |

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
- **Later (2026-10-27): a note too full for the second place.** From 2026-10-22 the merge narrowed the same playbook
  in 12 of 39 dreams, and the applier refused it each time: "note is 4228 bytes (> 4096)". The note was 4,052 bytes,
  so neither the line nor the link with the line as its why fitted, and the link was never checked before the try.
  4 current global notes were that full. **Fix:** such a note (not a lesson) goes to the bodies stage as a group of
  one, which writes its body shorter with every fact; code appends the line and adds the link. With no shorter body,
  the narrow waits. **Rule:** when the second place is full too, make room by the brain's own rule (compress the
  words, keep the facts) instead of trying the same refused write each run.
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
- **Later (2026-10-26): curate.hubs lost edits while the run had room.** In the dreams of 2026-10-16 to 10-26,
  curate.hubs lost 83 edits to its share, most of them hub pages, and many of these runs ended far under the cap (the
  10-26T10 dream lost 10 and changed 31 files of 50). **Fix:** after summarise, the `held` step lands a lost edit when
  no later step wrote its file, in an equal share with verify of what is left. **Rule:** a share protects the steps
  after it; what they leave unused goes back to the work it held.
- **Later (2026-10-16): a dream call could use tools.** One dream skipped merge, cards, summarise and verify at the
  $8 cap: two reflect calls cost $1.39 and $1.73 and read 1.9 M and 2.7 M input tokens for a 6 KB session note.
  `--bare` still reads the user's permission mode, and it was `bypassPermissions`: a probe call ran Bash and read a
  file in the live brain. The model opened the reports the session wrote, and the notes earlier runs had learned
  from it. **Fix:** every call gets `--tools ""`. Reflect's input carries the session's produced documents and the
  notes that already cite the session. **Rule:** what a model call may use goes in its input; a call has no tools.
- **Later (2026-10-17): verify could not judge a busy dream.** Learn made more notes per dream (30 sessions each), and
  verify sent all claims of a run in one call. A dream with 40 changed claims went past the call's $2.50 budget. The
  call cost $2.74 and gave no verdicts. A dream with 57 claims skipped verify, because $5.72 spent + $2.50 was more
  than $8. So 2 of 3 dreams wrote 130 edits that verify did not check. **Fix:** verify judges at most 12 claims per
  call: only claim notes, each as the run last wrote it, with the evidence they cite. The budget of a call is what
  the $8 cap leaves, up to $2.50. The step reason says how many claims stay unverified. **Rule:** a step whose input
  grows with the day's work sends its input in parts, so one large day cannot take all of its budget.
- **Later (2026-10-18): one refusal at the cap skipped every later step.** Learn reflected on more sessions (13 to 15
  calls a dream, from 7 to 10) and cost $3.84 and $3.99 in two dreams. After hubs, $2.07 and $1.75 were left, and
  curate.merge's $2.50 call did not fit. The refusal stopped every later model step of the run, so cards ($0.02),
  summarise ($0.02) and verify (batches from $1.00) were skipped too, and 51 and 52 claims stayed unverified. The
  batches from the day before never got a chance. **Fix:** a refused call stops only its own step; each later step
  asks, and its activity checks its own smallest call against what is left. A replay of one of the two dreams: verify
  judged 10 of 51 claims for $1.07, and the run stayed at $7.04. **Rule:** a refusal for a budget stops only the calls
  that the budget refuses.
- **Later (2026-10-19): the cap cut one session in two.** Learn reached the 50 files in the middle of a session
  whose answer was 47 edits. Its new notes landed, but the status edits on the 11 notes they supersede did not, so
  the doctor's supersession check failed on both machines. The 31 held edits were `lost`, because the sessions
  watermark moved past them. Learn still paid to reflect on the 7 sessions after it, and verify counted their notes
  as claims. **Fix:** learn applies a session's edits whole. The first session the cap has no room for, and every
  session after it, wait for the next dream: the watermark stops before it, and learn stops. A session larger than
  the cap applies what fits. Replays of 12 dreams: lost edits 106 to 43 (learn 47 to 0), split supersessions 11 to 0,
  $1.79 of reflects not spent, and no change in the 8 dreams that did not reach the cap. **Rule:** a cap applies to
  the unit that must stay whole, and work it holds stays as input for the next run.

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

## 27. Idle sessions were learned again in every dream

- **Symptom.** On 2026-10-20 the dream ran six times in seven hours, and the day's spend was more than twice the day
  before. The same finished sessions came back in each dream. One session that ended on 2026-10-12 was learned in
  every dream for five days and gave 66 new notes, many of them near-copies (eight versions of one anti-pattern).
- **Cause.** An idle Claude Code process moves its transcript's mtime about once an hour, but writes no byte. The
  session note compared the transcript's size and mtime, so each move rewrote the note. A rewritten note is new
  material for the gate: it counted toward the 30 sessions that start a dream early, and learn reflected on it again
  (an Opus call, because the session had owner corrections).
- **Fix.** A note is current when the transcript's size and the redactor version are the same. A transcript only
  grows by appending, so its size shows new content; the mtime is recorded, not compared. A replay of 16 recorded
  dreams (2026-10-14 to 2026-10-19) gave 11 or 12 dreams, learn spend $20 to $23 in place of $41.49, and no learn on
  an unchanged transcript (125 before).
- **Rule.** Find new content by a value that only new content changes (here, the size of an append-only file), not by
  a time that another process can move. A second pass over the same input is not new knowledge: it makes near-copies.

## 28. Half of the owner eval was ten days old

- **Symptom.** For ten days the human eval gave the same numbers every day (top-3 15.9%, within 3 calls 46.4%), and
  each daily check said "unchanged". 43 of its 69 rows are the laptop's. They came from the laptop's report of
  2026-10-11, because no one ran the laptop's half after that day. The headline and the metrics snapshot showed the
  old rows as today's result.
- **Cause.** Each machine runs its own prompts and merges the other machine's latest report. The merge used that
  report at any age and recorded only its path, in a note under the report.
- **Fix.** The merged report records how many days old the other machine's rows are (a metric, so the daily snapshot
  shows it). The headline tells you when they are older than 36 hours. The care playbook runs the laptop's half
  (`brain peer step eval-human`) and a sync before the devbox's `brain eval human`. With fresh laptop rows, within 3
  calls was 47.8%, not 46.4%.
- **Rule.** A merged measurement records the age of each part. A number that does not change for days is a reason to
  check its inputs, not proof that nothing changed.

## 29. launchd stopped spawning the hourly sync, twice

- **Symptom.** The laptop's hourly sync stopped on 2026-10-12 and again on 10-21. Each time, only a care run's own sync
  ran, one to three days later. `sync.last` said "waiting (not running), last exit 78: EX_CONFIG".
- **Cause.** launchd sets exit 78 itself when it cannot spawn a job (`brain` never exits 78). After that it did not run
  the agent again until `brain peer step sync-enable` reloaded it. The first record (2026-10-22) shows `job state =
  spawn failed` and `needs LWCR update`: the agent is a managed background item, and its launch constraint (LWCR)
  pins the program's code hash. `brain self-update` swaps the binary, and each build has a new code hash. Loading
  the agent again makes a new constraint.
- **Fix.** The bridge, which launchd keeps alive, checks the agent every 10 minutes. When the agent is not running and
  its last exit is 78, the bridge writes launchd's view and its log lines about the agent to
  `sync-agent-reloads.log` in the state folder, then reloads the agent (at most once an hour). For one day after a
  reload, `sync.last` is a warning that names the reload.
- **Rule.** A scheduled job that can stop without an error gets a watcher in a process that stays alive. The watcher
  repairs the job and records why it stopped.

## 30. A sync conflict copy was read as brain content

- **Symptom.** On 2026-10-23 one sync left 58 conflict copies, most of them older versions of hubs and notes that only
  the devbox's dream writes. Three minutes later, 8 session notes existed only as `<note> (conflict_on_<date>).md`,
  and their real paths, which other notes cite, were gone (`graph dangling links`). The prompt hook also showed two
  conflict copies to agents as pointers.
- **Cause.** Two readers took a conflict copy for live content. The session-note writer finds a session's note by the
  glob `*-<id8>-*.md`, which also matches the copy (same `session_id`). It writes to the first path in sort order, and
  a space sorts before `.`, so it wrote the copy and removed the real note. The index read every `.md` file, so the
  copy became a ranked node with its own id.
- **Fix.** The scan never emits a sync conflict copy (`config.IsSyncConflictCopy`), so the index, search, pointers
  and graph checks never see one. The session-note writer skips conflict copies when it looks for the session's note.
  The copies stay on disk until `brain sync --archive-conflicts` moves them into `.archive/conflicts/`.
- **Rule.** A file the sync writes as a side effect is not brain content. Every reader that lists brain files by
  name filters it with the same predicate.

## 31. The link skill kept adding a sixth related link

- **Symptom.** On 2026-10-23, `curate.link` stayed `ok` in all 16 dreams, but the applier refused 60 of its link ops
  ("about and related hold ≤ 5 each"), up to 11 in one dream. One note was refused 15 times, because each dream that
  rewrote it handed it to the link step again.
- **Cause.** 58 of the 60 refused ops started at a note in `proposed`, where the skill sees the whole note and its 5
  `related` links. The skill named the 0–5 range only in its output sketch, with no rule for a note that is full.
- **Fix.** The link skill counts a note's links first: a note that already holds 5 of a kind gets none of that kind.
  It links from the other note only when that note is in `proposed` and has room. The cap is `config.LinksPerKindMax`,
  and a skills test checks that the link skill states the rule. In a replay of 4 dreams' exact inputs, the old skill
  put 26 of 87 related links over the cap and the new skill 0 of 71, with as many valid links (60 and 63).
- **Rule.** A skill must state every cap the applier refuses it for, and what to do when the cap is reached.

## 32. The dream gate walked every run output in work/

- **Symptom.** On 2026-10-24 the dream at 11Z failed: its gate step timed out three times (15 min each), so the dream
  lost the hour. The gate at 15Z took 26 min. Every dream listed about 64,000 changed docs, and 14,299 big docs waited
  for a summary. The docs watermark stayed at 2026-09-04.
- **Cause.** The gate walked all of `work/` and skipped only dot-folders. `work/` held 10 M entries, and 7.6 M of them
  were below `artifacts/` and other bulk folders. The index never reads those folders, but the gate listed their `.md`
  run outputs as docs. 8,870 of the waiting big docs were run outputs that no search can show.
- **Fix.** The gate walk skips the folders that the index never reads (`scan.NeverReadsBelow`). On the live brain the
  walk read 23,237 docs, not 64,334, and 5,488 big docs, not 14,370. It took 3.2 s, not 14.4 s, with a warm cache.
- **Rule.** Two readers that select the same content use the same predicate. The dream works only on files that the
  index can show.

## 33. Hub lines grew until curate.hubs ran out of budget

- **Symptom.** On 2026-10-24 `curate.hubs` failed in 3 of 5 dreams with `error_max_budget_usd` ($2.96 to $3.30 on a
  $2.50 budget), and its edits were lost. Its output grew from about 23,000 tokens (10-21) to 60,000 to 78,000. The
  largest hub was 46 KB in 34 lines. One Start here line had 7,560 characters. The start digest has 8,000 characters
  for all its blocks, so a session saw 1 or 2 of a hub's 7 Start here items.
- **Cause.** The skill limited Start here and Open questions by a count of lines, not by length. Each dream added
  facts to existing lines, and each `replace_body` rewrote the whole page. The step's cost grew with each page, and a
  dream that touched 11 to 13 pages went over the budget.
- **Fix.** The hubs skill limits each Start here and Open questions line to `config.HubLineMaxChars` (300). A line
  is a pointer: the facts stay in the note. A page with a longer line is written short in the next update. A skills
  test checks that the skill states the limit. In a replay of real dream inputs, the input that failed live at $2.96
  passed at $2.04, and pages went from 3 KB to 46 KB down to 2 KB to 3 KB. The navigation and owner evals, and the
  pointers shown, did not change.
- **Rule.** A cap that counts lines also needs a limit on line length when a model writes the lines.

## 34. The cap cut one supersede in two

- **Symptom.** On 2026-10-28 the doctor's supersession check failed on both machines: 1 of 220 supersedes edges
  pointed at a note still `current`. At 10:00Z `curate.merge` had a share of 1 new file. It landed the kept note of
  one duplicate group (with `supersedes: [old]`) and held the old note, and in another group it marked the old note
  `superseded` and held the kept note, so that note's facts no longer ranked and no note claimed to supersede it.
- **Cause.** A merge unit is an `update_note` plus a `supersede` op, and `delta.Apply` writes both files. The step
  then sends them to `ApplyEdits` as separate files, and the cap judged each file alone. Rule 11 made learn sessions
  whole, but the curation steps had no unit to keep whole.
- **Fix.** `delta.Apply` names the files that one op writes as a group (`File.Group`), and the edits carry it. The
  cap reserves a group's new files at its first edit: the group lands whole or is held whole. A replay of the merge
  step of two dreams through the real code (find and bodies answers from the ledger, a shadow brain) matched
  production with groups cleared, and with groups split 2 to 0 (10-28) and 1 to 0 (10-23), with the same new files
  used of the share.
- **Rule.** A cap applies to the unit that must stay whole, at every step that has one.

## 35. A dead process's git lock stopped 14 dreams

- **Symptom.** From 2026-10-29T09:00Z every hourly dream failed at `snapshot`, 14 in a row, with `git add: fatal:
  Unable to create '…/.git/index.lock': File exists`. No dream ran for 13 hours, and the doctor stayed green on both
  machines.
- **Cause.** A git process that ran under `~/brain` (the boxes in `work/` sit inside the brain's repository) was
  killed at 06:44Z and left an empty `index.lock`. Git does not remove the lock of a run that was killed. The dream
  waited about 1 minute for each lock (rule from 2026-10-25, for a process that still runs), then failed, and the next
  hour found the same lock. No doctor check read `.brain/dream-runs/`.
- **Fix.** The dream moves a git lock older than 10 minutes to `.archive/git-locks/` and tries again at once; a newer
  lock it waits for as before. A replay of the snapshot step on a copy of the brain's repository with its own lock:
  main failed after 62 s of waits, the fix committed (110 files) with no wait. Locks a live process holds were waited
  for and kept. The doctor's `dream.last` check warns on one failed dream and is red on two in a row.
- **Rule.** A lock with no time limit needs a rule for its dead holder. A repeated failure needs a doctor line.

## 36. The file step lost 35 notes to a full cap

- **Symptom.** On 2026-10-30 at 00:00Z a dream learned 60 sessions (a backlog from 14 failed dreams). learn changed 47
  of the 50 files. The file step then proposed 35 notes from 33 inbox captures, and the cap held all 35 as `lost`. No
  later dream filed them, and one landed note linked to a note that never came (a dangling link).
- **Cause.** learn applies each session whole and keeps its watermark before the first session that waits (rule 11).
  The file step applied its answer as one batch. Its held edits were `lost`, and its captures watermark moved past
  every capture, so the captures were never read again. Earlier runs lost 11 notes in the same way (10-17 to 10-19).
- **Fix.** The file step applies its edits one capture at a time, oldest first, each whole. An edit belongs to the
  oldest capture that its `sources` names. The first capture that does not fit, and every capture after it, wait for
  the next dream: the captures watermark stops before it. A replay of 5 real file steps (the answers from the ledger,
  a shadow brain from each snapshot) matched production on main (lost 35, 1, 3, 6, 1) and lost 0 with the fix; 42
  captures waited for the next dream.
- **Rule.** A step that uses up its input must keep the input that the cap did not let it apply.
