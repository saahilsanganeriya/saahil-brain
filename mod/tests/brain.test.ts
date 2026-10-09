import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'

const BRAIN = '/Users/test-user/.local/bin/brain'
const NOW = Date.UTC(2026, 9, 8, 12, 0, 0)
const START = { surface: 'terminal', isInteractive: true, cwd: '/w/image-lane' } as const

type Ran = { exitCode: number; stdout?: string; stderr?: string }
type Call = { argv: string[]; stdin?: string; timeoutMs?: number }

// Stands for the engine and the brain CLI: `answer` decides each brain run; every run is recorded.
function stubBrain(on: On, answer: (args: string[], stdin?: string) => Ran, env: Record<string, string> = {}) {
  mock.clock(on, { now: NOW })
  mock.env(on, env)
  const calls: Call[] = []
  on('process.run', ($, e) => {
    calls.push({ argv: [...e.argv], stdin: e.init?.stdin, timeoutMs: e.init?.timeoutMs })
    const ran = e.argv[0] === BRAIN ? answer(e.argv.slice(1), e.init?.stdin) : { exitCode: 127 }
    return { value: { stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false, ...ran } }
  })
  on('tool.register', () => ({ value: undefined }))
  on('command.register', ($, e) => ({ value: { command: e.name } }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.render', () => ({ type: 'Text', props: {}, children: ['drawn by Claude Code'] }))
  on('session.start', () => ({ cwd: START.cwd }))
  on('tool.call', () => ({ result: 'done by the tool' }))
  on('classic.UserPromptSubmit', () => ({}))
  on('classic.SessionEnd', () => ({}))
  return calls
}

const brainRuns = (calls: Call[]) => calls.filter(call => call.argv[0] === BRAIN).map(call => call.argv.slice(1).join(' '))

// Catches a tool that shells out with the wrong arguments, no timeout, or hands the model an empty answer on failure;
// a tool left unanswered because Claude Code names it mcp__saahil-brain__<tool> (2026-10-09: every call failed); and
// another server's same-named tool (other-mcp has a brain_search) answered by this one.
test('brain_search runs `brain search … --json` within 5 s and reports a failure in words', async ($, on) => {
  let isDown = false
  const calls = stubBrain(on, args => (isDown ? { exitCode: 1, stderr: 'index missing: run brain index\n' } : { exitCode: 0, stdout: '[{"ID":"hub-image-lane"}]\n' }))
  await $.session.start(START)
  const found = await $.tool.call({ tool: 'mcp__saahil-brain__brain_search', query: 'ocr recall' } as never)
  expect(found.result).toBe('[{"ID":"hub-image-lane"}]')
  expect(calls.find(call => call.argv[2] === 'ocr recall')).toMatchObject({ argv: [BRAIN, 'search', 'ocr recall', '--json', '--limit', '8'], timeoutMs: 5000 })
  expect((await $.tool.call({ tool: 'mcp__other-mcp__brain_search', query: 'x' } as never)).result).toBe('done by the tool')
  isDown = true
  expect((await $.tool.call({ tool: 'mcp__saahil-brain__brain_show', id: 'x' } as never)).result).toBe('brain show failed: index missing: run brain index')
})

// Catches a brain hook run without the event on stdin, or context from SessionEnd handed on (it takes none).
test('classic events run `brain hook <sub>` with the event on stdin, and hand on context only where the event takes it', async ($, on) => {
  const calls = stubBrain(on, args => ({ exitCode: 0, stdout: JSON.stringify({ hookSpecificOutput: { additionalContext: `from ${args[1]}` } }) }))
  await $.session.start(START)
  const prompt = await $.classic.UserPromptSubmit({ prompt: 'why did ocr recall drop', session_id: 's1' } as never)
  expect(prompt.additionalContext).toEqual(['from prompt'])
  const sent = JSON.parse(calls.find(call => call.argv[2] === 'prompt')!.stdin!)
  expect(sent).toMatchObject({ hook_event_name: 'UserPromptSubmit', session_id: 's1', prompt: 'why did ocr recall drop' })
  expect(await $.classic.SessionEnd({ reason: 'other' } as never)).toEqual({})
  expect(brainRuns(calls).filter(run => run.startsWith('hook'))).toEqual(['hook prompt', 'hook end'])
})

// Catches the off switch missing a part: once BRAIN_OFF=1 no hook, guard or tool may start a brain process.
test('with BRAIN_OFF=1 the mod runs no brain process', async ($, on) => {
  const calls = stubBrain(on, () => ({ exitCode: 0, stdout: 'x' }), { BRAIN_OFF: '1' })
  await $.session.start(START)
  await $.classic.UserPromptSubmit({ prompt: 'p' } as never)
  await $.tool.call({ tool: 'Write', file_path: '/Users/test-user/brain/notes/x.md', content: 'x' })
  expect(brainRuns(calls)).toEqual([])
})

// Catches a write under ~/brain that skips the guard, a block that still writes, a warning lost, or the guard run on
// files outside the brain.
test('Write under ~/brain goes through brain guard: block denies, warn writes and notes', async ($, on) => {
  let verdict = { decision: 'block', reason: 'notes/x.md is over the 4 KB note cap' }
  const calls = stubBrain(on, args => (args[0] === 'guard' ? { exitCode: 0, stdout: JSON.stringify(verdict) } : { exitCode: 0 }))
  await $.session.start(START)
  const write = (path: string) => $.tool.call({ tool: 'Write', file_path: path, content: '---\nid: x\n---\n' })
  const blocked = await write('/home/test-user/brain/notes/x.md')
  expect(blocked).toEqual({ deny: 'brain guard: notes/x.md is over the 4 KB note cap' })
  verdict = { decision: 'warn', reason: 'summary is missing' }
  const warned = await write('/Users/test-user/brain/notes/x.md')
  expect(warned).toMatchObject({ result: 'done by the tool', context: ['brain guard: summary is missing'] })
  const guardInput = JSON.parse(calls.filter(call => call.argv[1] === 'guard').pop()!.stdin!)
  expect(guardInput).toMatchObject({ tool_name: 'Write', tool_input: { file_path: '/Users/test-user/brain/notes/x.md' } })
  await write('/w/image-lane/README.md')
  expect(brainRuns(calls).filter(run => run === 'guard').length).toBe(2)
})

// Catches a Read of a big brain doc without the hint to read one section.
test('Read of a mapped brain doc appends the sections hint', async ($, on) => {
  stubBrain(on, args => (args[0] === 'sections' ? { exitCode: 0, stdout: JSON.stringify([{ heading: 'a' }, { heading: 'b' }, { heading: 'c' }]) } : { exitCode: 0 }))
  await $.session.start(START)
  const read = await $.tool.call({ tool: 'Read', file_path: '/Users/test-user/brain/work/box/plans/DESIGN.md' })
  expect(read.context?.[0]).toMatch(/^This doc has 3 sections: brain_sections/)
})

// Catches a band that misreads the sync record or the dream's run id, or names no hub.
test('the band reads `brain: <hub> · synced <age> ✓ · dream <age> ago`', async ($, on) => {
  stubBrain(on, args => {
    if (args[0] === 'sync') return { exitCode: 0, stdout: JSON.stringify({ ok: true, result: { ended: '2026-10-08T11:48:00Z', exitCode: 0 } }) }
    if (args[0] === 'dream') return { exitCode: 1, stdout: 'last dreams:\n  2026-10-08T090000Z-ab12cd34  done  3 edits\nschedule: temporal unreachable\n' }
    if (args[0] === 'search') return { exitCode: 0, stdout: JSON.stringify([{ ID: 'image-lane', Type: 'hub' }]) }
    return { exitCode: 0 }
  })
  await $.session.start(START)
  const ui = await $.ui.mount({
    plugin: 'saahil-brain', component: 'AbovePrompt', requestId: 'band', surface: 'terminal', viewport: { columns: 120, rows: 40 },
    props: { hasSurvey: false, isWorking: false, maxRows: 4, bodyColumns: 110, scroll: { offset: 0, bodyRows: 4 }, view: {} },
  })
  expect(await ui.find({ type: 'Text', text: /^brain: / })).toMatchObject({ text: 'brain: image-lane · synced 12m ✓ · dream 3h ago' })
})
