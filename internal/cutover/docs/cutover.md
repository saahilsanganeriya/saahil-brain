# Cut-over tooling: `brain cutover …`

The commands behind the saahil-docs → `~/brain/work` move (runbook: brain-redesign box, `plans/CUTOVER.md`, steps C1,
C2, C8, and the checks for C4/C5/C11). Every command is read-only unless `--apply` is given; every write keeps a backup.

```
C1  manifest ──► before.tsv ─┐
C4/C5  (the mv, by hand)     ├─► compare ──► same / changed-later / changed / missing / extra   (exit 1: missing or changed)
    manifest ──► after.tsv ──┘
C2  laptop.tsv + devbox.tsv ──► reconcile ──► report.md   (--apply --machine M: rename M's losing copies aside)
C8  rewrite-paths <work/box> --from OLD… --to NEW          (dry run: per-file counts; --apply: backup + atomic replace)
C11 find-names <roots…>                                     (names, text mentions and link targets still left)
```

## Manifest (`manifest <root> --out F`)

One TSV row per entry, in `filepath.WalkDir` order, after a `#` header:

| column | value |
|---|---|
| path | relative to the root, `/`-separated; Go-quoted when it holds a control char or `\`, is `-`, or starts with `#`/`"` |
| size | bytes (`-` for a directory) |
| mtime | RFC 3339 UTC, nanoseconds |
| sha256 | regular files ≤ 2 MB not under a `--skip-hash-under` glob; else `-` (compare then uses size + mtime) |
| type | `file` · `dir` · `symlink` · `other` |
| target | the symlink's target (never followed), else `-` |

A symlinked root (the compat link after the swap) is resolved; symlinked directories inside are rows, never walked.
Hashing runs on ≤ 8 workers with ≤ 32 rows in flight, so memory does not grow with the tree. Files that vanish
mid-walk are counted, not listed. The output is written as `F.partial` and renamed at the end; an existing `F`, or an
`F` inside the root, is refused.

`compare` and `reconcile` merge-join two manifests in that order (component-wise: `a/x` before `a-c`) and refuse an
out-of-order manifest. A move keeps sizes, hashes and mtimes, so any difference is either a later edit (newer mtime,
`--allow-newer` lists it as `changed-later`) or a failure.

## Reconcile (`reconcile <laptop.tsv> <devbox.tsv> --out R.md`)

Text files hashed on both sides with different content: the newer mtime wins (a tie goes to the devbox, which holds
the originals). The loser's plan is `<file>.conflict-<loser machine>-<loser mtime, 20060102T150405Z>`, derived from
the manifests only, so both machines compute the same plan. `--apply --machine M --root DIR` renames M's losing copies
aside, only when the file still has its manifest sha and the conflict name is free; the winner arrives by the normal
sync. Unhashed, binary and file-vs-directory differences are listed, not resolved.

## Rewrite (`rewrite-paths <root> --from P… --to P`)

Replaces every old prefix (`*` = one path component, e.g. a worktree slug; longest prefix first) in text files
≤ 10 MB and in symlink targets. Never touched: binaries (NUL in the first 8 KB, or a known binary extension),
`.env*` files (never even opened), and history — `.git/`, `log/`, `.archive/`, `transcripts/`, `<uuid>.jsonl`
session transcripts, `manifest*.{tsv,csv,json,jsonl,txt}`. Venv files are reported by role (`venv:pyvenv.cfg`,
`venv:activate`, `venv:script` for both `#!<venv>/bin/python` and pip's long-path `'''exec'` form, `venv:symlink`);
a rewrite that would make a `#!` line longer than 127 bytes is refused.

`--apply` (backup dir default `~/brain/.archive/cutover/rewrite-<UTC>/`, must be outside the root and fresh):

1. copy the original to `<backup>/files/<path>` with its mode and mtime;
2. write the new bytes to a temp file beside it, keep the mode, fsync;
3. rename over the original only if its size and mtime are unchanged since it was read (a live writer wins; the file
   is reported and left alone). The new file's mtime is now.
4. append `path kind occurrences mtime_before sha256_before sha256_after target_before target_after` to
   `<backup>/manifest.tsv`.

Undo a file: copy `<backup>/files/<path>` back. Undo a link: `ln -sfn <target_before> <path>`.

## Find (`find-names <root…>`)

Lists every non-history entry named `saahil-docs` (`name:dir|file|symlink`), every text file containing it (`text`,
with a count) and every symlink whose target contains it (`link-target`), then counts by kind and file category.
History and `.env*` are excluded as above.
