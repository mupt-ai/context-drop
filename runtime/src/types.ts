export type SessionBackend = "tmux" | "herdr";

export interface AgentConfig {
  command: string[];
  promptMode?: "arg" | "stdin";
}

export interface RuntimeConfig {
  host: "127.0.0.1" | "::1";
  port: number;
  stateDir: string;
  tokenFile: string;
  defaultBackend?: SessionBackend;
  tmuxSession: string;
  herdrPath?: string;
  herdrSession?: string;
  agents: Record<string, AgentConfig>;
}

export interface RunRecord {
  id: string;
  name: string;
  agent: string;
  repo: string;
  backend?: SessionBackend;
  tmuxSession?: string;
  tmuxWindow?: string;
  herdrSession?: string;
  herdrWorkspace?: string;
  herdrTab?: string;
  herdrPane?: string;
  status: "running" | "exited" | "unknown";
  createdAt: string;
}

export interface LaunchRequest {
  agent: string;
  repo: string;
  prompt: string;
  name?: string;
  backend?: SessionBackend;
  workspaceId?: string;
}
