// Context Drop bridge for Pi. Installed by `context-drop setup pi`.
//
// While a Pi session runs, this extension publishes a registry entry at
// ~/.context-drop/pi/<pid>.json and listens on ~/.context-drop/pi/<pid>.sock so
// `context-drop send` can deliver a message into the live session. Each
// connection sends one JSON line {"type":"user","text":"..."} and receives one
// JSON line {"ok":true} or {"ok":false,"error":"..."}.
import * as fs from "node:fs";
import * as net from "node:net";
import * as os from "node:os";
import * as path from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
	const home = process.env.CONTEXT_DROP_HOME || path.join(os.homedir(), ".context-drop");
	const dir = path.join(home, "pi");
	const socketPath = path.join(dir, `${process.pid}.sock`);
	const registryPath = path.join(dir, `${process.pid}.json`);
	let server: net.Server | undefined;
	let ctx: any;
	let status = "idle";

	const publish = () => {
		if (!ctx) return;
		const sm = ctx.sessionManager;
		const entry = {
			pid: process.pid,
			sessionId: sm.getSessionId(),
			sessionFile: sm.getSessionFile() ?? "",
			cwd: sm.getCwd?.() ?? ctx.cwd,
			name: sm.getSessionName?.() ?? "",
			socket: socketPath,
			status,
			updatedAt: Date.now(),
		};
		const tmp = `${registryPath}.${process.pid}.tmp`;
		try {
			fs.writeFileSync(tmp, JSON.stringify(entry), { mode: 0o600 });
			fs.renameSync(tmp, registryPath);
		} catch {
			// Registry is best effort; the session keeps working without it.
		}
	};

	const deliver = (text: string) => {
		if (!ctx || ctx.isIdle()) {
			pi.sendUserMessage(text);
		} else {
			pi.sendUserMessage(text, { deliverAs: "followUp" });
		}
	};

	const listen = () => {
		if (server) return;
		fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
		try {
			fs.unlinkSync(socketPath);
		} catch {}
		server = net.createServer((conn) => {
			let buffer = "";
			conn.setEncoding("utf8");
			conn.on("data", (chunk) => {
				buffer += chunk;
				const newline = buffer.indexOf("\n");
				if (newline < 0) return;
				const line = buffer.slice(0, newline);
				buffer = "";
				try {
					const frame = JSON.parse(line);
					if (frame?.type !== "user" || typeof frame.text !== "string" || !frame.text.trim()) {
						throw new Error("expected {\"type\":\"user\",\"text\":\"...\"}");
					}
					deliver(frame.text);
					conn.end(JSON.stringify({ ok: true }) + "\n");
				} catch (err: any) {
					conn.end(JSON.stringify({ ok: false, error: String(err?.message ?? err) }) + "\n");
				}
			});
			conn.on("error", () => {});
		});
		server.on("error", () => {});
		server.listen(socketPath, () => {
			try {
				fs.chmodSync(socketPath, 0o600);
			} catch {}
		});
		server.unref();
	};

	pi.on("session_start", async (_event, context) => {
		ctx = context;
		listen();
		publish();
	});
	pi.on("session_info_changed", async () => publish());
	pi.on("agent_start", async () => {
		status = "working";
		publish();
	});
	pi.on("agent_end", async () => {
		status = "idle";
		publish();
	});
	pi.on("session_shutdown", async () => {
		server?.close();
		server = undefined;
		for (const file of [socketPath, registryPath]) {
			try {
				fs.unlinkSync(file);
			} catch {}
		}
	});
}
