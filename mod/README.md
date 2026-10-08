# saahil-brain (Claude Code mod)

The brain inside Claude Code. `brain install` links this folder into `~/mods/saahil-mods/saahil-brain`, lists it in
that marketplace and enables it; `brain install --off claude` takes all three back out.

| Part | What |
|---|---|
| tools | `brain_search`, `brain_show`, `brain_sections`, `brain_neighbors`, `brain_note`: each runs `brain … --json` (5 s) |
| hooks | `SessionStart`/`SubagentStart` → `brain hook start`, `UserPromptSubmit` → `hook prompt`, `PostToolUse` → `hook post`, `SessionEnd` → `hook end`, `PostCompact` → `hook compact`, each with the event on stdin. settings.json carries only the mesh inbox hooks, so no brain command runs twice for one event. |
| guard | Write/Edit/MultiEdit/NotebookEdit under `~/brain` → `brain guard`: block denies with the reason, warn writes and tells the model. A Read of a mapped (> 50 KB) brain doc gets a hint to read one section. |
| `/brain` | a pane: hubs of this work, what the brain showed this session, related notes, a note box |
| `/note <text>` | drops the text into the brain's inbox |
| band | `brain: <hub> · synced <age> ✓ · dream <age> ago`, refreshed every 60 s |

`BRAIN_OFF=1` in the session's environment turns every part off. Develop: `claude plugin validate mod` and
`cd mod && claude plugin test .`
