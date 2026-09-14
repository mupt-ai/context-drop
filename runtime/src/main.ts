import { readFileSync } from "node:fs";
import type { RuntimeConfig } from "./types.js";
import { createRuntimeServer } from "./server.js";

const configPath = process.argv[2];
if (!configPath) throw new Error("usage: runtime <config.json>");
const config = JSON.parse(readFileSync(configPath, "utf8")) as RuntimeConfig;
const token = readFileSync(config.tokenFile, "utf8").trim();
if (!token) throw new Error("runtime token is empty");
if (!config.workerAgent || !config.agents?.[config.workerAgent]) throw new Error("runtime config needs a configured workerAgent; run context-drop config worker-agent");
const server = createRuntimeServer(config, token);
server.listen(config.port, config.host, () => {
  console.error(`context-drop runtime listening on http://${config.host}:${config.port}`);
  void server.pool.start().catch(error => console.error("worker pool startup:", error.message));
});
const shutdown = () => server.close(() => process.exit(0));
process.on("SIGINT", shutdown); process.on("SIGTERM", shutdown);
