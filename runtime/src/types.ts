export type WorkerAgent = "pi" | "codex" | "claude";
export interface RuntimeConfig {
  host: "127.0.0.1" | "::1";
  port: number;
  stateDir: string;
  tokenFile: string;
  herdrPath?: string;
  herdrSession?: string;
  fullAIHerdrWorkspaceLabel?: string;
  agents: Record<string, { command: string[] }>;
  workerAgent: WorkerAgent;
  reportCredentialsFile: string;
  contextDropPath?: string;
}

export interface Conversation {
  path: string;
  leafId: string | null;
  instructions?: string;
}
export interface Task {
  id: string;
  worker?: number;
  requestedWorker?: number;
  prompt: string;
  name: string;
  repo: string;
  session: string;
  routerId: string;
  chatId: string;
  status: "queued" | "running" | "waiting" | "completed" | "failed";
  capability: string;
  createdAt: string;
  question?: string;
  requestIds: string[];
  turnId: string;
  instructions?: string;
  followups?: string[];
}
export interface ParentReport {
  id: string;
  runId: string;
  worker: number;
  routerId: string;
  chatId: string;
  kind: "progress" | "needs_user" | "turn_completed" | "completed" | "failed";
  message: string;
  createdAt: string;
  leaseId?: string;
  leaseUntil?: number;
  deliveredAt?: string;
  nextAttemptAt?: number;
  attempts?: number;
}
export interface Slot {
  id: number;
  capability: string;
  pane?: string;
  agent?: string;
  launching?: boolean;
  error?: string;
}
export interface PoolState {
  slots: Slot[];
  events: string[];
  conversation?: Conversation;
  tasks: Task[];
  reports: ParentReport[];
}
