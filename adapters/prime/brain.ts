// @saahil-brain-managed prime
/**
 * The brain in prime-agent: ~/.prime/agent/extensions/saahil-brain.ts, written by `brain install` (prime loads every
 * file there). A shim over the `brain` CLI, which makes every decision and prints nothing while the brain is in shadow.
 *
 *   session_start       `brain hook start` builds the digest once per root session file
 *   before_agent_start  the digest goes after the system prompt, byte-identical each turn (prime rebuilds it per turn);
 *                       the prompt's pointers (`brain hook prompt`) go as a hidden message, so the prefix stays cached
 *   session_shutdown    `brain hook end` on quit (not on reload)
 *   tools               brain_search, brain_show
 *
 * RLM children run this instance too; their files live under session-artifacts/, so only root sessions get the digest.
 * Fails open: a missing binary, an error or the 5 s timeout adds nothing; BRAIN_OFF=1 turns every part off.
 * API read from prime-agent 0.9.4 dist/core/extensions/types.d.ts and docs/extensions.md.
 */
import { spawn } from "node:child_process";
import { resolve } from "node:path";
import { Type } from "typebox";

const HOST = "prime";
const BRAIN = process.env.BRAIN_BIN || "/Users/test-user/.local/bin/brain";
const TIMEOUT_MS = 5_000;
const TEXT_MAX = 8_000;

/** Runs brain with the JSON payload on stdin; resolves its stdout, or "" on any failure. Never rejects. */
function brain(args: string[], payload?: unknown): Promise<string> {
	return new Promise((settle) => {
		let out = "";
		let done = false;
		const finish = (text: string) => {
			if (!done) (done = true), clearTimeout(timer), settle(text.trim());
		};
		const timer = setTimeout(() => (child?.kill("SIGKILL"), finish("")), TIMEOUT_MS);
		let child: any;
		try {
			child = spawn(BRAIN, args, { stdio: ["pipe", "pipe", "ignore"], env: { ...process.env, BRAIN_HOST: HOST } });
		} catch {
			return finish("");
		}
		child.stdout.setEncoding("utf8");
		child.stdout.on("data", (chunk: string) => (out += chunk));
		child.on("error", () => finish(""));
		child.on("close", (code: number | null) => finish(code === 0 ? out : ""));
		child.stdin.on("error", () => {});
		child.stdin.end(payload === undefined ? "" : JSON.stringify(payload));
	});
}

/** The model-facing text of a hook's output: Claude-style JSON's additionalContext, else the plain text. */
function contextOf(out: string): string {
	try {
		const parsed = JSON.parse(out);
		return String(parsed?.hookSpecificOutput?.additionalContext ?? parsed?.additionalContext ?? "").slice(0, TEXT_MAX);
	} catch {
		return out.slice(0, TEXT_MAX);
	}
}

function envelope(event: string, ctx: any, extra: object = {}) {
	return { hook_event_name: event, session_id: String(ctx?.sessionManager?.getSessionId?.() ?? ""), cwd: ctx?.cwd ?? "", host: HOST, ...extra };
}

function rootFile(ctx: any): string | undefined {
	const file = ctx?.sessionManager?.getSessionFile?.();
	return typeof file === "string" && file && !file.includes("/session-artifacts/") ? resolve(file) : undefined;
}

const text = (t: string) => ({ content: [{ type: "text" as const, text: t || "no result" }], details: {} });

export default function saahilBrain(pi: any): void {
	if (process.env.BRAIN_OFF === "1") return;
	const digests = new Map<string, Promise<string>>();
	let pointers: Promise<string> = Promise.resolve("");

	pi.on("session_start", (_event: any, ctx: any) => {
		const file = rootFile(ctx);
		if (file && !digests.has(file)) digests.set(file, brain(["hook", "start"], envelope("SessionStart", ctx)).then(contextOf));
	});

	pi.on("input", (event: any, ctx: any) => {
		if (event?.source !== "extension" && rootFile(ctx))
			pointers = brain(["hook", "prompt"], envelope("UserPromptSubmit", ctx, { prompt: String(event?.text ?? "") })).then(contextOf);
		return { action: "continue" };
	});

	pi.on("before_agent_start", async (event: any, ctx: any) => {
		const file = rootFile(ctx);
		if (!file) return;
		const [digest, nudge] = await Promise.all([digests.get(file) ?? "", pointers]);
		pointers = Promise.resolve("");
		const result: any = {};
		if (digest && typeof event?.systemPrompt === "string") result.systemPrompt = `${event.systemPrompt}\n\n${digest}`;
		if (nudge) result.message = { customType: "saahil-brain", content: nudge, display: false };
		return Object.keys(result).length ? result : undefined;
	});

	pi.on("session_shutdown", async (event: any, ctx: any) => {
		if (event?.reason !== "reload" && rootFile(ctx)) await brain(["hook", "end"], envelope("SessionEnd", ctx, { reason: event?.reason }));
	});

	pi.registerTool({
		name: "brain_search",
		label: "Brain search",
		description: "Search Saahil's brain (notes, hubs, work docs) and get ranked ids with one-line summaries.",
		parameters: Type.Object({ query: Type.String(), limit: Type.Optional(Type.Number()) }),
		execute: async (_id: string, p: any) => text(await brain(["search", p.query, "--json", "--limit", String(p.limit ?? 8)])),
	});
	pi.registerTool({
		name: "brain_show",
		label: "Brain show",
		description: "Show one brain node (id or path from brain_search): its summary, frontmatter and edges.",
		parameters: Type.Object({ id: Type.String() }),
		execute: async (_id: string, p: any) => text(await brain(["show", p.id, "--json"])),
	});
}
