const BOOLEAN_FLAGS = new Set([
  "all",
  "allow-tmux-daemon",
  "apply",
  "attach",
  "create-worktree",
  "dry-run",
  "force",
  "help",
  "history",
  "hold",
  "imsg",
  "install-launch-agent",
  "json",
  "keep-launch-agent",
  "keep-tmux-daemon",
  "launch-agent",
  "once",
  "print-command",
  "restart",
  "suicide",
  "symlink",
  "telegram",
  "version",
  "wait",
  "watchdog",
]);

export function parseArgv(argv) {
  const tokens = [...argv];
  let command = null;

  const flags: Record<string, any> = {};
  const positionals: string[] = [];

  for (let i = 0; i < tokens.length; i += 1) {
    const token = tokens[i];

    if (token === "--") {
      positionals.push(...tokens.slice(i + 1));
      break;
    }

    if (!token.startsWith("--")) {
      if (command === null) {
        command = token;
      } else {
        positionals.push(token);
      }
      continue;
    }

    if (token.startsWith("--no-")) {
      flags[toCamel(token.slice(5))] = false;
      continue;
    }

    const eq = token.indexOf("=");
    const rawKey = eq === -1 ? token.slice(2) : token.slice(2, eq);
    const key = toCamel(rawKey);

    if (eq !== -1) {
      flags[key] = token.slice(eq + 1);
      continue;
    }

    if (BOOLEAN_FLAGS.has(rawKey)) {
      flags[key] = true;
      continue;
    }

    const value = tokens[i + 1];
    if (value === undefined || value.startsWith("--")) {
      throw new Error(`Missing value for --${rawKey}`);
    }
    flags[key] = value;
    i += 1;
  }

  return { command: command || "help", flags, positionals };
}

export function toCamel(value) {
  return value.replace(/-([a-z])/g, (_, char) => char.toUpperCase());
}
