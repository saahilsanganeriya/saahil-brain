// Drives the prime and omp extensions with recorded host events against a fake brain binary.
// Run: node --test adapters/test/adapters.test.mjs   (prime's typebox comes from the installed prime-agent; without it, prime is skipped)
import assert from "node:assert/strict";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const TYPEBOX = "/home/test-user/.local/lib/node_modules/prime-agent/node_modules/typebox";
const dir = mkdtempSync(join(tmpdir(), "brain-adapters-"));
const log = join(dir, "calls.jsonl");
const fakeBrain = join(dir, "brain");
writeFileSync(fakeBrain, `#!/bin/sh
stdin=$(cat)
printf '{"argv":"%s","host":"%s","stdin":%s}\\n' "$*" "$BRAIN_HOST" "\${stdin:-null}" >> ${log}
printf '{"hookSpecificOutput":{"additionalContext":"%s"}}' "from $2"
`, { mode: 0o755 });
process.env.BRAIN_BIN = fakeBrain;

const calls = () => (existsSync(log) ? readFileSync(log, "utf8").trim().split("\n").map((l) => JSON.parse(l)) : []);

async function load(host) {
	const hostDir = join(dir, host);
	mkdirSync(join(hostDir, "node_modules"), { recursive: true });
	copyFileSync(join(here, "..", host, "brain.ts"), join(hostDir, "brain.ts"));
	if (existsSync(TYPEBOX) && !existsSync(join(hostDir, "node_modules", "typebox"))) symlinkSync(TYPEBOX, join(hostDir, "node_modules", "typebox"));
	return (await import(join(hostDir, "brain.ts"))).default;
}

// A pi stand-in that records handlers and tools; zod-like enough for omp's parameter builders.
function fakePi() {
	const handlers = {}, tools = {};
	const schema = () => ({ optional: schema });
	return { handlers, tools, on: (event, fn) => (handlers[event] = fn), registerTool: (t) => (tools[t.name] = t),
		zod: { object: schema, string: schema, number: schema } };
}

const ctx = { cwd: "/w/image-lane", sessionManager: { getSessionId: () => "s1", getSessionFile: () => "/sessions/s1.jsonl" } };

for (const [host, systemPrompt, appended] of [["prime", "BASE", "BASE\n\nfrom start"], ["omp", ["BASE"], ["BASE", "from start"]]]) {
	// Catches a digest not built at session start, pointers not delivered with the turn, a session end not reported,
	// a reload mistaken for an end, the wrong host name, or tools that call the wrong CLI.
	test(`${host}: start digest, prompt pointers, end, tools`, { skip: host === "prime" && !existsSync(TYPEBOX) }, async () => {
		writeFileSync(log, "");
		const pi = fakePi();
		(await load(host))(pi);
		pi.handlers.session_start({ reason: "startup" }, ctx);
		assert.deepEqual(pi.handlers.input({ text: "why did recall drop", source: "interactive" }, ctx), { action: "continue" });
		const result = await pi.handlers.before_agent_start({ prompt: "why did recall drop", systemPrompt }, ctx);
		assert.deepEqual(result.systemPrompt, appended);
		assert.deepEqual(result.message, { customType: "saahil-brain", content: "from prompt", display: false });
		await pi.handlers.session_shutdown({ reason: "reload" }, ctx);
		await pi.handlers.session_shutdown({ reason: "quit" }, ctx);
		const found = await pi.tools.brain_search.execute("t1", { query: "ocr" });
		assert.equal(found.content[0].text, '{"hookSpecificOutput":{"additionalContext":"from ocr"}}');
		const seen = calls();
		assert.deepEqual(seen.map((c) => c.argv), ["hook start", "hook prompt", "hook end", "search ocr --json --limit 8"]);
		assert.ok(seen.every((c) => c.host === host));
		assert.deepEqual(seen[1].stdin, { hook_event_name: "UserPromptSubmit", session_id: "s1", cwd: "/w/image-lane", host, prompt: "why did recall drop" });
	});
}

// Catches the off switch missing the extensions: with BRAIN_OFF=1 they register nothing.
test("BRAIN_OFF=1 registers nothing", async () => {
	process.env.BRAIN_OFF = "1";
	try {
		const pi = fakePi();
		(await load("omp"))(pi);
		assert.deepEqual([Object.keys(pi.handlers), Object.keys(pi.tools)], [[], []]);
	} finally {
		delete process.env.BRAIN_OFF;
	}
});
