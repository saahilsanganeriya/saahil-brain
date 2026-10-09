# Backfill: bring your history into the brain

Read this for step 3 of [AGENT-SETUP.md](../AGENT-SETUP.md). Do it once, after both machines are set up and
`brain peer status` answers from the devbox.

**In plain words.** A new brain is empty. Your history is not: Claude Code already keeps memories per repo,
transcripts of every session, and you have folders of plans and notes. The backfill brings all of that in, in a
fixed order, so the first dream starts from your real past. Nothing is deleted and nothing is copied twice: memories
are copied (the originals stay), doc folders are moved (with a proof that nothing was lost).

```
 1 memories  ─► brain import memory ─► .import/<machine>/ ─► brain file ─► notes/  (+ MEMORY.md)      $ (Haiku, some Opus)
 2 sessions  ─► brain session backfill ─► sessions/<machine>/…                                         free
 3 doc folders ─► MOVE into work/<box>/ with manifests before and after                                free
 4 boxes     ─► brain backfill run ─► a CARD per box + notes for finished campaigns                    $ (Haiku, Opus)
 5 index     ─► brain index (both machines)                                                            free
 6 evals     ─► brain eval build · brain eval build --human                                            $ (Opus judge)
 7 first dream ─► brain dream run --now ─► brain doctor (both) ─► brain view                          $ (≤ $8)
```

**Where to run.** Model steps run on the devbox (it has the provider and the Temporal worker). The laptop runs its
own copy steps (import, session notes, moves of laptop folders) from an **interactive terminal**: a launchd job
cannot open files under `~/Documents` ([LESSONS.md](LESSONS.md#5-launchd-jobs-hang-on-documents-tcc)).

## Costs

**[HUMAN] spend.** Steps 1, 4, 6 and 7 call models and cost money. Say yes to each budget before the agent runs it.

| Step | Reference cost (one person, ~2,400 memories, 83 boxes, ~1,400 transcripts) | Cap |
|---|---|---|
| 1 file memories | $2.94 Haiku (80 batches) + $7.99 Opus (the 38 hardest) ≈ $11 | `brain file --budget` (default $15 per run) |
| 2 session notes | $0 (no model) | — |
| 3 move doc folders | $0 | — |
| 4 backfill CARDs + campaign notes | $1.91 (82 CARDs, 17 notes) + $18.08 with `--include-paused` (189 notes) = $20.00 | `--budget` (default $80, all runs together) |
| 6 human eval set | about $1–1.50 per machine (Opus judge) | small, per call |
| 7 first dream | $1–5 | $8 per run, $100 per day |

## Step 1 — memories → notes

1. On each machine, copy every Claude Code memory folder into the brain. The originals are only read.

   ```
   brain import memory
   ```

   Expect: `imported <N> memory files from <machine> into /Users/<user>/brain/.import/<machine> (manifest …)`.
   Check: `wc -l ~/brain/.import/<machine>-manifest.tsv` equals `<N>`.

2. On the laptop, push the copies to the devbox: `brain sync`. Check on the devbox: `ls ~/brain/.import/` shows
   both `devbox` and `laptop`.

3. On the devbox, seed the hubs (the topic pages notes join). Edit the scope line of each seeded hub to fit your work
   before you file; delete nothing.

   ```
   brain file --seed-hubs
   ```

   Expect: `seed hubs: <n> created, 0 already there (never overwritten)`.

4. Plan, then file (Haiku, in batches of 20):

   ```
   brain file --dry-run            # the batch plan and the reconcile; calls no model, writes nothing
   brain file                      # [HUMAN] spend: ≈ $3 per 2,000 memories; stops at --budget (default $15)
   brain file --model opus --retry-skipped     # the memories Haiku left out or wrote over the cap
   ```

   Expect the reconcile line at the end of each run to balance:

   ```
   <imported> imported = <filed> filed + <this run> this run + <waiting> waiting + <dup> duplicates + <skipped> skipped   balanced ✓
   ```

   Check: `brain doctor` shows `graph orphans 0`, `graph dangling links 0`, `graph hub coverage ≥ 90%`. `head -30
   ~/brain/memory/MEMORY.md` shows your standing rules as compressed facts.

**Why copy, not move.** Claude Code keeps writing into its own memory folders until `brain install` points
`autoMemoryDirectory` at `~/brain/memory`. Copies keep the originals as the record; the manifest proves every file
was filed, skipped with a reason, or found to be a duplicate.

## Step 2 — transcripts → session notes

On each machine (no model, a few minutes):

```
brain session backfill
```

Check: the number of files under `~/brain/sessions/<machine>/` equals the number of transcripts
(`ls ~/.claude/projects/*/*.jsonl | wc -l`), minus transcripts with no messages. Session notes hold your prompts
with secrets redacted, the files each session touched, and its PRs. They are history: never auto-loaded, but the
dream learns from them and the evals are built from them.

## Step 3 — move existing doc folders in as boxes

**In plain words.** If you keep plans and notes in folders (inside a repo, under `~/Documents`, anywhere), each folder
becomes a box under `~/brain/work/`. You MOVE it, you never copy it and delete the original. A manifest before and
after proves that every file arrived.

```
 before.tsv ◄── brain cutover manifest <old folder>
     mv <old folder> ~/brain/work/<box>      (one rename: same disk, same path spelling)
 after.tsv  ◄── brain cutover manifest ~/brain/work/<box>
 brain cutover compare before.tsv after.tsv --allow-newer   →  same / changed-later / changed / missing / extra
```

For each folder, on the machine that holds it (laptop: an interactive terminal):

1. Choose the box name: lower case, words joined by `-`, ideally with a date (`audio-lane-2026-09`).
2. Check that nothing is writing into the folder (no agent, build or editor working there), and that git tracks
   nothing in it (`git -C <repo> ls-files -- <folder> | wc -l` prints `0`). If git tracks files, STOP: that folder
   belongs to the repo. Never change a company repo for the brain.
3. Manifest before:

   ```
   brain cutover manifest <old folder> --out ~/brain/.archive/cutover/<box>-before.tsv \
       --skip-hash-under '**/artifacts/**' --skip-hash-under '**/.venv/**' --skip-hash-under '**/venv/**'
   ```

4. Check source and target are on the same disk, then move with ONE rename:

   ```
   stat -f %d <old folder> ~/brain/work          # laptop (macOS); devbox: stat -c %d …   → the same number twice
   mv <old folder> ~/brain/work/<box>
   ```

   On the devbox, spell both paths the same way (both `/Users/<user>/…`). A move between `/home/<devbox-user>/…`
   and `/Users/<user>/…` is not a rename: it copies and deletes
   ([LESSONS.md](LESSONS.md#1-a-move-across-the-two-path-spellings-copies-instead-of-renaming)).

5. Manifest after, then compare:

   ```
   brain cutover manifest ~/brain/work/<box> --out ~/brain/.archive/cutover/<box>-after.tsv \
       --skip-hash-under '**/artifacts/**' --skip-hash-under '**/.venv/**' --skip-hash-under '**/venv/**'
   brain cutover compare ~/brain/.archive/cutover/<box>-before.tsv ~/brain/.archive/cutover/<box>-after.tsv --allow-newer
   ```

   Expect: `missing 0` and `changed 0` (exit 0). `changed-later` and `extra` are files written after the first
   manifest. Any `missing` or `changed`: STOP and find out why before the next folder.

6. Fix old absolute paths inside the box (dry run first; it keeps mtimes and backs up every file):

   ```
   brain cutover rewrite-paths ~/brain/work/<box> --from <old folder>/ --to /Users/<user>/brain/work/<box>/
   brain cutover rewrite-paths ~/brain/work/<box> --from <old folder>/ --to /Users/<user>/brain/work/<box>/ --apply
   ```

   It never opens `.env*` files, binaries or history (`.git/`, `log/`, `.archive/`, transcripts), and refuses a venv
   shebang that would grow over 127 bytes.

7. If a running tool still uses the old path, leave a link at the OLD place pointing to the new one. Never put a link
   to the brain inside the brain ([LESSONS.md](LESSONS.md#4-a-link-that-loops-back-into-work-arrived-by-sync)).

8. A `STATE.md` over 8 KB: `brain cutover lift-state <box>` maps its sections; `--keep "<heading>" --apply` moves the
   old file to `log/` unchanged and lifts the chosen sections into a new capped STATE.md.

**What reaches the other machine.** Unison carries the box's text (≤ 2 MB; `md txt py sh js toml yml yaml` and files
with no extension). Bulk stays where it was made. To give the devbox a box's bulk on purpose, run
`brain agents box <box>` on the laptop (it never deletes).

## Step 4 — a CARD for every box, notes for finished campaigns

On the devbox (Temporal):

```
brain backfill run --dry-run                 # every box with its planned action; no model, no Temporal
brain backfill run --budget 20               # [HUMAN] spend: CARDs (Haiku) + campaign notes (Opus)
brain backfill run --include-paused --budget 40     # also older, paused campaigns
brain backfill status                        # last runs and the backfill's total spend
```

Check: every box has `work/<box>/CARD.md` (`ls ~/brain/work/*/CARD.md | wc -l` equals the number of boxes);
`brain lint ~/brain/work/*/CARD.md` exits 0 or 1 (warnings), never 2. A finished campaign (CARD `done`, or quiet for
14 days with STATE saying finished) gets at least one "what we built and why" note plus lessons. The `--budget` cap
counts all backfill runs together; a rerun pays only for boxes that changed.

## Step 5 — index

On both machines: `brain index`. Expect a line like `indexed <N> paths: <c> changed, 0 deleted, … pagerank <k>
iterations`. Check: `brain index --stats`.

## Step 6 — evals

Evals tell you whether the brain helps. They are built from your own history, so they mean more after a few weeks of
sessions.

```
brain eval build                      # devbox: navigation and QA sets from sessions and notes
brain eval build --human              # each machine: prompts you typed → the file that answered (Opus judge)  [HUMAN] spend
brain eval all                        # devbox: run every eval, write reports
```

On the laptop, run `brain eval build --human` from an interactive terminal with `BRAIN_LLM_LOGIN=1` (launchd cannot
read the keychain). The judge reads transcripts and stores only the turn and the target file, never your prompt text.
Pass marks on the reference setup: navigation ≥ 70% top-3 and ≥ 80% within 3 tool calls; QA recall@3 ≥ 80%.

## Step 7 — first dream, health, Obsidian

```
brain dream run --now                 # devbox: one dream now, under the $8 cap
brain dream status                    # devbox: the run, its edits, cost, deletions 0
brain doctor                          # both machines: must end GREEN
brain view                            # laptop: a read-only copy for Obsidian at ~/brain-view
```

Check: `brain dream status` shows the run `completed` with `deletions 0`; `brain doctor` prints
`brain doctor GREEN on <machine>` on both. Open `~/brain-view` as a vault in Obsidian; it is rebuilt by
`brain view` and by every sync.

## After the backfill

The hourly dream takes over: new memories, session lessons, CARD refreshes, merges. Read
[OPERATIONS.md](OPERATIONS.md) for daily life.
