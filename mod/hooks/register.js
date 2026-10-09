// saahil-brain: the brain in Claude Code. Every brain hook runs from here (settings.json keeps only the mesh inbox
// hooks), so each event has one owner. Each part shells out to the brain CLI, which decides; a failure adds nothing.
// BRAIN_OFF=1 in the session's environment turns every part off.

const BRAIN = '@BRAIN_BIN@'
const BRAIN_ROOTS = ['@LAPTOP_HOME@/brain/', '@DEVBOX_HOME@/brain/'] // the devbox bind-mounts the second
const PANE = 'saahil-brain'
const TOOL_PREFIX = 'mcp__saahil-brain__' // how Claude Code names this mod's tools in tool.call; another server's brain_search stays theirs
const TIMEOUT_MS = 5_000
const BAND_TTL_MS = 60_000
const MAX_SHOWN = 12
const WRITE_TOOLS = ['Write', 'Edit', 'MultiEdit', 'NotebookEdit']

// The classic hook events that may hand the model context; SessionEnd and PostCompact only report.
const CONTEXT_EVENTS = ['SessionStart', 'SubagentStart', 'UserPromptSubmit', 'PostToolUse']

const TOOLS = {
  brain_search: {
    description: "Search Saahil's brain (notes, hubs, work docs, sessions) and get ranked ids with one-line summaries. Use it before re-deriving a decision or a lesson.",
    properties: { query: { type: 'string' }, limit: { type: 'number', description: 'default 8' } },
    required: ['query'],
    argv: input => ['search', input.query, '--json', '--limit', String(input.limit ?? 8)],
  },
  brain_show: {
    description: 'Show one brain node (an id or path from brain_search): its summary, frontmatter and edges.',
    properties: { id: { type: 'string' } },
    required: ['id'],
    argv: input => ['show', input.id, '--json'],
  },
  brain_sections: {
    description: 'List the sections of a big brain doc with line ranges, so you can Read one section instead of the whole file.',
    properties: { id: { type: 'string', description: 'id or path' } },
    required: ['id'],
    argv: input => ['sections', input.id, '--json'],
  },
  brain_neighbors: {
    description: 'Brain nodes linked to one node, within depth hops (default 1): its hub, related notes, sources.',
    properties: { id: { type: 'string' }, depth: { type: 'number' } },
    required: ['id'],
    argv: input => ['neighbors', input.id, '--json', '--depth', String(input.depth ?? 1)],
  },
  brain_note: {
    description: "Drop a short note into the brain's inbox for the dream to file (a finding, decision or lesson, with its evidence path).",
    properties: { text: { type: 'string' }, hub: { type: 'string', description: 'optional hub id' } },
    required: ['text'],
    argv: input => ['note', '--json', ...(input.hub ? ['--hub', input.hub] : []), input.text],
  },
}

let off = false
let cwd = ''
let shown = [] // what the brain put in front of this session, newest first
let view = { hubs: [], related: [], note: 'Loading…' }
let band = { line: undefined, hub: undefined, pending: undefined }
let bandTimer

export function register(on) {
  on('session.start', async ($, e, next) => {
    off = (await $.env.get('BRAIN_OFF')) === '1'
    cwd = e.cwd ?? ''
    if (off) return next(e)
    for (const [name, tool] of Object.entries(TOOLS)) {
      await $.tool.register({ name, description: tool.description, inputSchema: { type: 'object', properties: tool.properties, required: tool.required } })
    }
    await $.command.register({ name: 'brain', description: 'The brain beside this session: hubs, pointers shown, related notes, a note box', immediate: true })
    await $.command.register({ name: 'note', description: "Drop a note into the brain's inbox", argumentHint: '<text>', immediate: true })
    band.pending = refreshBand($)
    bandTimer ??= $.clock.every(BAND_TTL_MS, () => refreshBand($))
    return next(e)
  })

  on('classic.SessionStart', ($, e, next) => bridge($, e, next, 'start'))
  on('classic.SubagentStart', ($, e, next) => bridge($, e, next, 'start'))
  on('classic.UserPromptSubmit', ($, e, next) => bridge($, e, next, 'prompt'))
  on('classic.PostToolUse', ($, e, next) => bridge($, e, next, 'post'))
  on('classic.SessionEnd', ($, e, next) => bridge($, e, next, 'end'))
  on('classic.PostCompact', ($, e, next) => bridge($, e, next, 'compact'))

  on('tool.call', async ($, e, next) => {
    if (off) return next(e)
    const name = String(e.tool).startsWith(TOOL_PREFIX) ? e.tool.slice(TOOL_PREFIX.length) : e.tool
    const tool = TOOLS[name]
    if (tool) {
      const ran = await brain($, tool.argv(e))
      if (name === 'brain_show' && ran.exitCode === 0) remember([e.id])
      return { result: ran.exitCode === 0 ? ran.stdout.trim() || 'no result' : `brain ${name.slice(6)} failed: ${firstLine(ran.stderr) || 'exit ' + ran.exitCode}` }
    }
    const path = e.file_path ?? e.notebook_path
    if (!inBrain(path)) return next(e)
    if (e.tool === 'Read') return withSectionsHint($, path, await next(e))
    if (!WRITE_TOOLS.includes(e.tool)) return next(e)
    const verdict = verdictOf(await brain($, ['guard'], JSON.stringify({ hook_event_name: 'PreToolUse', tool_name: e.tool, tool_input: inputOf(e), cwd })))
    if (verdict.decision === 'block') return { deny: `brain guard: ${verdict.reason}` }
    const result = await next(e)
    if (verdict.decision !== 'warn' || result.deny !== undefined || result.isError) return result
    return { ...result, context: [...(result.context ?? []), `brain guard: ${verdict.reason}`] }
  })

  // ---- the pane and /note ----

  on('command.run', { command: 'brain' }, async $ => {
    await $.ui.open({ id: PANE, title: 'Brain', focus: true, closeOnEscape: true })
    await refreshView($)
    return {}
  })

  on('command.run', { command: 'note' }, async ($, e) => {
    if (off) return { text: 'brain is off (BRAIN_OFF=1)' }
    if (!e.args.trim()) return { text: 'usage: /note <text>' }
    return { text: await addNote($, e.args.trim()) }
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Button, Input, Text } = $.ui.resolve(e)
    const width = e.props.bodyColumns
    const section = (title, rows, empty) => [
      Text({ bold: true, children: [title] }),
      ...(rows.length ? rows.map(row => Text({ wrap: 'truncate-end', children: [fit(row, width)] })) : [Text({ dimColor: true, children: [empty] })]),
    ]
    return Box({
      flexDirection: 'column',
      children: [
        ...(view.note ? [Text({ dimColor: true, children: [fit(view.note, width)] })] : []),
        ...section(`Hubs of ${basename(cwd) || 'this work'}`, view.hubs.map(label), 'none found'),
        ...section('Shown this session', shown, 'nothing yet'),
        ...section('Related', view.related.map(label), 'none'),
        Button({ key: 'refresh', label: 'refresh', hotkey: 'r', plain: true, onPress: () => refreshView($) }),
        ...(Input ? [Input({ key: 'note', label: 'Note', placeholder: "Enter drops it into the brain's inbox", value: '', submitLabel: 'note', onSubmit: async text => text.trim() && $.ui.toast(await addNote($, text.trim())) })] : []),
      ],
    })
  })

  // ---- the band: brain: <hub> · synced <age> ✓ · dream <age> ago ----

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (off || e.props.hasSurvey || e.props.maxRows < 1) return next(e)
    if (band.line === undefined && band.pending) await band.pending
    if (!band.line) return next(e)
    const { Box, Text } = $.ui.resolve(e)
    return Box({ flexDirection: 'column', children: [Text({ dimColor: true, children: [fit(band.line, e.props.bodyColumns)] }), await next(e)] })
  })
}

// ---- the brain CLI ----

// A classic hook event runs `brain hook <sub>` with the event as stdin, as a settings hook would.
async function bridge($, e, next, sub) {
  const result = await next(e)
  if (off) return result
  const context = contextOf((await brain($, ['hook', sub], JSON.stringify(e))).stdout)
  if (!context || !CONTEXT_EVENTS.includes(e.hook_event_name)) return result
  if (e.hook_event_name === 'UserPromptSubmit') remember(context.split('\n').filter(line => line.trim()))
  return { ...result, additionalContext: [...(result.additionalContext ?? []), context] }
}

async function brain($, args, stdin) {
  try {
    return await $.process.run([BRAIN, ...args], { timeoutMs: TIMEOUT_MS, ...(stdin === undefined ? {} : { stdin }) })
  } catch (error) {
    return { exitCode: -1, stdout: '', stderr: String(error?.message ?? error) }
  }
}

async function brainJSON($, args) {
  const ran = await brain($, args)
  try {
    return ran.exitCode === 0 ? JSON.parse(ran.stdout) : undefined
  } catch {
    return undefined
  }
}

// A hook's model-facing text: Claude-style JSON's additionalContext, else the plain text it printed.
function contextOf(stdout) {
  const out = stdout.trim()
  if (!out) return ''
  try {
    const parsed = JSON.parse(out)
    return String(parsed?.hookSpecificOutput?.additionalContext ?? parsed?.additionalContext ?? '')
  } catch {
    return out
  }
}

// `brain guard` answers {decision, reason}, or Claude's PreToolUse JSON, or exit 2 with the reason on stderr.
function verdictOf(ran) {
  if (ran.exitCode === 2) return { decision: 'block', reason: firstLine(ran.stderr) || 'blocked' }
  if (ran.exitCode !== 0) return { decision: 'allow' }
  try {
    const parsed = JSON.parse(ran.stdout)
    const claude = parsed?.hookSpecificOutput
    if (claude?.permissionDecision === 'deny') return { decision: 'block', reason: claude.permissionDecisionReason ?? 'blocked' }
    const decision = parsed?.decision ?? parsed?.verdict
    return { decision, reason: String(parsed?.reason ?? parsed?.message ?? '') }
  } catch {
    return { decision: 'allow' }
  }
}

async function withSectionsHint($, path, result) {
  if (result.deny !== undefined || result.isError) return result
  const sections = await brainJSON($, ['sections', path, '--json'])
  const count = (Array.isArray(sections) ? sections : (sections?.sections ?? [])).length
  if (count === 0) return result
  return { ...result, context: [...(result.context ?? []), `This doc has ${count} sections: brain_sections lists them with line ranges; Read one with offset/limit instead of the whole file.`] }
}

async function addNote($, text) {
  const ran = await brain($, TOOLS.brain_note.argv({ text }))
  return ran.exitCode === 0 ? 'noted in the brain inbox' : `note failed: ${firstLine(ran.stderr) || 'exit ' + ran.exitCode}`
}

async function refreshView($) {
  const work = basename(cwd)
  const hits = hitsOf(await brainJSON($, ['search', work || 'index', '--json', '--limit', '8']))
  const hubs = hits.filter(hit => hit.type === 'hub').slice(0, 3)
  const related = hubs.length ? hitsOf(await brainJSON($, ['neighbors', hubs[0].id, '--json', '--depth', '1'])).slice(0, 6) : hits.slice(0, 6)
  view = { hubs, related, note: hits.length || hubs.length ? undefined : 'The brain found nothing for this work yet (or its index is not built).' }
  band.hub = hubs[0]?.id
  $.ui.invalidate('ui.render')
}

function refreshBand($) {
  band.pending = (async () => {
    const work = basename(cwd) || 'index'
    const [sync, dream, hubs, now] = await Promise.all([
      brainJSON($, ['sync', 'status']),
      brain($, ['dream', 'status']),
      band.hub ? [] : brainJSON($, ['search', work, '--json', '--limit', '1', '--type', 'hub']),
      $.clock.now(),
    ])
    band.hub ??= hitsOf(hubs)[0]?.id
    const record = sync?.result ?? sync
    const synced = record?.ended ? `synced ${age(now - Date.parse(record.ended))} ${record.exitCode >= 2 ? '✗' : '✓'}` : 'not synced yet'
    const run = /(\d{4}-\d{2}-\d{2})T(\d{2})(\d{2})(\d{2})Z/.exec(dream.stdout)
    const dreamt = run ? `dream ${age(now - Date.parse(`${run[1]}T${run[2]}:${run[3]}:${run[4]}Z`))} ago` : 'no dream yet'
    band = { ...band, line: `brain: ${band.hub ?? work} · ${synced} · ${dreamt}`, pending: undefined }
    $.ui.invalidate('ui.render')
  })()
  return band.pending
}

// ---- small things ----

function hitsOf(json) {
  const list = Array.isArray(json) ? json : (json?.results ?? json?.hits ?? json?.nodes ?? json?.neighbors ?? [])
  return list.map(hit => ({ id: hit.id ?? hit.ID, type: hit.type ?? hit.Type, title: hit.title ?? hit.Title ?? '', summary: hit.summary ?? hit.Summary ?? '' })).filter(hit => hit.id)
}

function label(hit) {
  return `${hit.id}${hit.title || hit.summary ? '  ' + (hit.title || hit.summary) : ''}`
}

function remember(items) {
  shown = [...items, ...shown.filter(old => !items.includes(old))].slice(0, MAX_SHOWN)
}

function inBrain(path) {
  return typeof path === 'string' && BRAIN_ROOTS.some(root => path.startsWith(root))
}

function inputOf(e) {
  const { tool, tool_use_id, agentId, ...input } = e
  return input
}

function age(ms) {
  const minutes = Math.max(0, Math.round(ms / 60_000))
  if (minutes < 60) return `${minutes}m`
  return minutes < 48 * 60 ? `${Math.round(minutes / 60)}h` : `${Math.round(minutes / 1440)}d`
}

function basename(path) {
  return String(path).replace(/\/+$/, '').split('/').pop() ?? ''
}

function fit(text, width) {
  return text.length <= width ? text : `${text.slice(0, Math.max(0, width - 1))}…`
}

function firstLine(text) {
  return String(text ?? '').trim().split('\n')[0]
}
