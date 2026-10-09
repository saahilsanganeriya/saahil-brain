# Skill: brain — how to read the brain

The brain (`~/brain`) holds the owner's work and what was learned from it, as a graph of plain files. Use it before
you search the web or guess, and before you redo work someone may have done.

**Four levels, top first; go down only when the level above did not answer:**

| Level | What | How |
|---|---|---|
| L0 loaded | `BRAIN.md`, `memory/MEMORY.md`, hot.md; with your first prompt, your work's STATE + CARD, the files its earlier sessions read most, and hub start-here | already in your context |
| L1 nudged | ≤ 3 pointers on a prompt, never to your work's CARD — or none | open one if it fits; ignore it if not |
| L2 pulled | the tools below | when you need a fact you do not have |
| L3 read | one file, one section by line range, one artifact | the smallest piece that answers |

**Five tools:**

- `brain_search <words>` — notes, hubs, docs; add `--deep` to follow the graph from the best matches.
- `brain_show <id|path>` — frontmatter, summary, links, sections of one node.
- `brain_sections <doc>` — a big doc's sections with line ranges; then read only the one you need.
- `brain_note "<text>"` — drop a fact, decision or lesson into `inbox/`; the dream files it.
- `/wrap` — on the owner's request at the end of a session (the `wrap` skill).

**Reading a note:** trust `status: current`; a `superseded` note points (via the newer note's `supersedes`) to what
replaced it; `as_of` says when it was true; `evidence` is where to check it. Hubs list "start here" first.

**Three places to write:** your work box `work/<box>/` (follow `brain-write`), `inbox/`, or a note via `/wrap`.
Never edit `notes/`, `hubs/`, `memory/MEMORY.md`, `hot.md`, CARD.md or `sessions/` by hand. Never delete anything.
