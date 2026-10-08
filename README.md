# saahil-brain

The code for Saahil's brain: one Go binary (`brain`) for knowledge (index, hooks, pointers, guards, PageRank, MCP)
and agents (the two-machine mesh; it absorbs `devbox-agent`), plus a local Temporal worker for the dream, per-session
learning and the backfill, the skills that hold all judgment, the Claude Code mod and the host adapters.

- Design, plan and goal: `/Users/test-user/Documents/work-repo/saahil-docs/brain-redesign-2026-10-07/`
  (`plans/DESIGN.md`, `plans/PLAN.md`, `GOAL.md`); after the cut-over: `~/brain/work/brain-redesign-2026-10-07/`.
- **Code only.** The brain's data (`~/brain`: docs, notes, memories, sessions, transcripts) holds customer data and
  never enters this repo. `.gitignore` and the pre-commit guard enforce it.
