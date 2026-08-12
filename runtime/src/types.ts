export type SessionBackend = "tmux" | "herdr";
export type DelegationLane = "human_copilot" | "full_ai";

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
  fullAIHerdrWorkspaceLabel?: string;
  agents: Record<string, AgentConfig>;
  delegateAgent?: string;
}

export interface RunRecord {
  id: string;
  name: string;
  agent: string;
  repo: string;
  backend?: SessionBackend;
  tmuxSession?: string;
  tmuxWindow?: string;
  tmuxPane?: string;
  herdrSession?: string;
  herdrWorkspace?: string;
  herdrTab?: string;
  herdrPane?: string;
  lane?: DelegationLane;
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
  lane?: DelegationLane;
  environment?: Record<string, string>;
  extension?: string;
}

export type ParentReportKind = "started" | "progress" | "needs_user" | "completed" | "failed";
export type SensitiveAction = "payment_or_purchase" | "password_or_mfa" | "terms_or_subscription";
export interface ParentReport {
  id: string;
  runId: string;
  routerId: string;
  chatId: string;
  kind?: ParentReportKind;
  message: string;
  sensitiveAction?: SensitiveAction;
  challengeToken?: string;
  challengedAction?: string;
  challengeExpiresAt?: string;
  authorizationId?: string;
  createdAt: string;
  leaseId?: string;
  leaseUntil?: string;
  deliveredAt?: string;
  challengeConsumedAt?: string;
  challengeReservationId?: string;
  challengeReservationUntil?: string;
}
