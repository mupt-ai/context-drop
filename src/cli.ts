import fs from "node:fs";
import http from "node:http";
import path from "node:path";

import { parseArgv } from "./args.js";
import { quoteArgv, renderTemplate, shellExportBlock } from "./command.js";
import { assertNoFatalCommandFindings } from "./command-validation.js";
import { collectDoctorChecks } from "./doctor.js";
import { expectedSchemaSql, initRelaymuxDb, relaymuxDbPath, relaymuxDbStatus } from "./db.js";
import { defaultConfig, defaultConfigPath, getIntegration, isIntegrationEnabled, loadConfig, resolveLogDir, resolveStateDir, writeConfig } from "./config.js";
import { runDaemon } from "./daemon.js";
import { getLaunchAgentStatus, installLaunchAgent, printLaunchAgentStatus, restartLaunchAgent, uninstallLaunchAgent } from "./launch-agent.js";
import { handleNotify } from "./notify.js";
import { handleSchedule, scheduleHelpText } from "./schedule.js";
import { webhookConfig } from "./webhook.js";
import { expandPath, ensureDirectory, pathExists, readTextFile } from "./paths.js";
import { applyHomeMigration, buildHomeMigrationInventory, ensureRelaymuxHomeLayout, formatHomeMigrationInventory, formatHomeMigrationResults } from "./migration.js";
import { buildImsgConfig, initOptionsFromFlags, resolveImsgChatId } from "./setup-imsg.js";
import { buildTelegramConfig, initTelegramOptionsFromFlags, withTelegramIntegration } from "./setup-telegram.js";
import { resolveLaunchSession } from "./session.js";
import { createCommandWindow, killWindowByName, listAgentWindows, setWindowMetadata, validateSessionName } from "./tmux.js";
import { createWorktree, resolveRepoAndWorkdir } from "./worktree.js";
import { isReplyMode, replyModesText } from "./reply-modes.js";
import { resolveAgentConfig } from "./launch/agents.js";
import { makeRunId, sanitizeLaunchName } from "./launch/names.js";
import { resolveLaunchNotification } from "./launch/notifications.js";
import { launchTmuxAgent } from "./launch/tmux.js";
import { handleStatus } from "./status.js";

export async function main(argv, io = defaultIo()) {
  try {
    const platform = io.platform || process.platform;
    const parsed = parseArgv(argv);
    if (parsed.flags.version || parsed.command === "version") {
      io.stdout.write("relaymux 0.1.0\n");
      return 0;
    }
    if (parsed.flags.help && parsed.command === "schedule") {
      io.stdout.write(scheduleHelpText());
      return 0;
    }
    if (parsed.flags.help || parsed.command === "help") {
      io.stdout.write(helpText());
      return 0;
    }

    if (parsed.command === "setup") {
      return handleSetup(parsed.flags, io, platform);
    }

    const configInfo = loadConfig({ configPath: parsed.flags.config, env: io.env });
    const stateDir = resolveStateDir(configInfo.config, io.env);

    switch (parsed.command) {
      case "migrate-home":
        return handleMigrateHome(parsed.flags, configInfo, io);
      case "db":
        return handleDb(parsed.flags, parsed.positionals, io);
      case "launch":
        return handleLaunch(parsed.flags, configInfo, stateDir, io);
      case "status":
        return handleStatus(parsed.flags, configInfo, stateDir, io, platform);
      case "notify":
        await handleNotify({
          flags: parsed.flags,
          positionals: parsed.positionals,
          config: configInfo.config,
          stateDir,
          io,
        });
        return 0;
      case "ask":
      case "request":
        return handleAsk(parsed.flags, parsed.positionals, configInfo, io);
      case "schedule":
        return handleSchedule({
          flags: parsed.flags,
          positionals: parsed.positionals,
          configInfo,
          stateDir,
          binPath: process.argv[1],
          io,
          platform,
        });
      case "daemon":
        return await runDaemon({ flags: parsed.flags, configInfo, stateDir, io });
      case "start-tmux":
        return handleStartTmux(parsed.flags, configInfo, stateDir, io);
      case "supervise-tmux":
        return await handleSuperviseTmux(parsed.flags, configInfo, stateDir, io);
      case "install-launch-agent":
        installLaunchAgent({ flags: parsed.flags, configInfo, binPath: process.argv[1], io, platform });
        return 0;
      case "restart-launch-agent":
        restartLaunchAgent({ flags: parsed.flags, configInfo, binPath: process.argv[1], io, platform });
        return 0;
      case "status-launch-agent":
      case "launch-agent-status":
        printLaunchAgentStatus({ config: configInfo.config, io, json: Boolean(parsed.flags.json), platform });
        return 0;
      case "uninstall-launch-agent":
        uninstallLaunchAgent({ config: configInfo.config, io, platform });
        return 0;
      case "doctor":
        return handleDoctor(configInfo, io, platform);
      default:
        throw new Error(`Unknown command "${parsed.command}"`);
    }
  } catch (error) {
    io.stderr.write(`relaymux: ${error.message}\n`);
    return 1;
  }
}

async function handleSetup(flags, io, platform = process.platform) {
  const wantsImsg = Boolean(flags.imsg || flags.preset === "imsg");
  const wantsTelegram = Boolean(flags.telegram || flags.preset === "telegram");
  const wantsAdapter = wantsImsg || wantsTelegram;
  const configPath = flags.config || defaultConfigPath(io.env);
  let configInfo = loadConfig({ configPath: flags.config, env: io.env });
  const shouldInstallLaunchAgent = flags.launchAgent !== false;

  if (flags.dryRun) {
    io.stdout.write(`Would ${configInfo.exists ? "update" : "create"} config at ${configInfo.exists ? configInfo.path : configPath}\n`);
    if (wantsImsg) {
      const imessage = getIntegration(configInfo.config, "imessage");
      if (!flags.chatId && !flags.recipient && !imessage.chatId) {
        io.stdout.write("Would prompt for iMessage/SMS chat unless --chat-id <id> is provided.\n");
      } else {
        io.stdout.write("Would configure the iMessage/SMS adapter.\n");
      }
    }
    if (wantsTelegram) {
      io.stdout.write("Would configure the Telegram adapter.\n");
      if (!flags.telegramChatId) io.stdout.write("Would discover the Telegram chat id after you send /start to the bot.\n");
      if (flags.telegramBotToken) io.stdout.write("Would store the Telegram bot token under ~/.relaymux/secrets.\n");
    }
    io.stdout.write(shouldInstallLaunchAgent
      ? `Would install/restart the ${backgroundServiceDryRunName(platform)}.\n`
      : "Would skip background service installation because --no-launch-agent was passed.\n");
    return 0;
  }

  if (!configInfo.exists || flags.force || wantsAdapter) {
    await writeSetupConfig({
      ...flags,
      installLaunchAgent: false,
    }, io, platform);
    configInfo = loadConfig({ configPath: flags.config, env: io.env });
  } else {
    io.stdout.write(`Using existing config at ${configInfo.path}\n`);
    if (configInfo.usingLegacyDefault) {
      io.stdout.write("Tip: this is the legacy config path. Run `relaymux migrate-home --dry-run` to inventory a safe move into ~/.relaymux.\n");
    }
  }

  if (!shouldInstallLaunchAgent) {
    io.stdout.write("Background service installation skipped because --no-launch-agent was passed.\n");
    io.stdout.write(`Config: ${configInfo.path}\n`);
    io.stdout.write("Next: relaymux restart-launch-agent\n");
    return 0;
  }

  restartLaunchAgent({
    flags: { load: flags.load, watchdog: flags.watchdog },
    configInfo,
    binPath: process.argv[1],
    io,
    platform,
  });

  const status = handleDoctor(configInfo, io, platform);
  const launchAgent = getLaunchAgentStatus(configInfo.config, { platform, env: io.env });
  if (shouldInstallLaunchAgent && flags.load !== false && backgroundServiceNeedsSetupFailure(launchAgent)) {
    io.stderr.write(`Setup wrote the config, but the ${backgroundServiceStatusName(launchAgent, platform)} is not running. Review the service detail above and run \`relaymux restart-launch-agent\` after fixing it.\n`);
    return 1;
  }
  if (status === 0) {
    const placeholderOrchestrator = configInfo.config.orchestrator?.placeholder === true;
    io.stdout.write(placeholderOrchestrator
      ? "Setup complete. The background service is running, but no real orchestrator CLI is configured yet.\n"
      : "Setup complete. relaymux is ready.\n");
    io.stdout.write(`Config: ${configInfo.path}\n`);
    io.stdout.write(`Logs: ${resolveLogDir(configInfo.config, io.env)}\n`);
    io.stdout.write("Next steps:\n");
    if (isIntegrationEnabled(configInfo.config, "imessage")) {
      if (shouldInstallLaunchAgent) {
        io.stdout.write("  1. Text the configured iMessage/SMS chat; relaymux should reply from the background service.\n");
      } else {
        io.stdout.write("  1. Run `relaymux restart-launch-agent` when you are ready to receive iMessage/SMS requests in the background service.\n");
      }
    } else if (isIntegrationEnabled(configInfo.config, "telegram")) {
      if (shouldInstallLaunchAgent) {
        io.stdout.write("  1. Send a Telegram-mode request with `relaymux ask --reply-mode telegram <text>` after setting the bot token.\n");
      } else {
        io.stdout.write("  1. Run `relaymux restart-launch-agent` when you are ready to deliver Telegram replies from the background service.\n");
      }
    } else {
      io.stdout.write("  1. Use the local CLI/API path: `relaymux ask <text>` or `relaymux notify --reply-mode none ...`.\n");
    }
    if (placeholderOrchestrator) {
      io.stdout.write("  2. Install pi or edit ~/.relaymux/config.json so orchestrator.command points at your agent CLI.\n");
      io.stdout.write("  3. Run `relaymux restart-launch-agent` after changing the orchestrator.\n");
    } else {
      io.stdout.write("  2. Run `relaymux status` to inspect the daemon and any tmux agent tabs.\n");
      io.stdout.write("  3. Use `relaymux launch --repo <path> --agent pi --prompt <task>` for a manual first agent.\n");
    }
  } else {
    io.stderr.write("Setup completed, but doctor found missing requirements. Fix the missing checks above and re-run `relaymux doctor`.\n");
  }
  return status;
}

async function writeSetupConfig(flags, io, platform = process.platform) {
  const wantsImsg = Boolean(flags.imsg || flags.preset === "imsg");
  const wantsTelegram = Boolean(flags.telegram || flags.preset === "telegram");
  const wantsAdapter = wantsImsg || wantsTelegram;
  const existing = loadConfig({ configPath: flags.config, env: io.env });
  const configPath = existing.exists ? existing.path : flags.config || defaultConfigPath(io.env);
  const labels = [];
  let config;
  let updatedExisting = false;

  if (existing.exists && !flags.force && !wantsAdapter) {
    throw new Error(`Config already exists at ${existing.path}. Use --force to overwrite.`);
  }

  if (existing.exists && !flags.force && wantsAdapter) {
    config = cloneConfig(existing.config);
    updatedExisting = true;
  } else {
    if (wantsImsg) {
      const chatId = await resolveImsgChatId(flags, io, io.env);
      config = buildImsgConfig({
        ...initOptionsFromFlags(flags),
        chatId,
      }, io.env);
      labels.push("iMessage/SMS adapter");
    } else if (wantsTelegram) {
      config = buildTelegramConfig(await initTelegramOptionsFromFlags(flags, io, io.env), io.env);
      labels.push("Telegram adapter");
    } else {
      config = defaultConfig(io.env);
    }
  }

  if (updatedExisting && wantsImsg) {
    const imessage = getIntegration(config, "imessage");
    const chatId = flags.chatId || flags.recipient || imessage.chatId || await resolveImsgChatId(flags, io, io.env);
    const imsgConfig = buildImsgConfig({
      ...initOptionsFromFlags(flags),
      chatId,
    }, io.env);
    config.integrations = { ...(config.integrations || {}), imessage: imsgConfig.integrations.imessage };
    config.launchNotifications = { ...(config.launchNotifications || {}), replyMode: "imessage" };
    labels.push("iMessage/SMS adapter");
  }

  if (wantsTelegram && wantsImsg) {
    config = withTelegramIntegration(config, await initTelegramOptionsFromFlags(telegramFlagsWithExisting(flags, config), io, io.env));
    labels.push("Telegram adapter");
  } else if (updatedExisting && wantsTelegram) {
    config = withTelegramIntegration(config, await initTelegramOptionsFromFlags(telegramFlagsWithExisting(flags, config), io, io.env));
    labels.push("Telegram adapter");
  }

  const target = writeConfig(configPath, config, { force: Boolean(flags.force) || updatedExisting, env: io.env });
  const homeDir = ensureRelaymuxHomeLayout(path.dirname(defaultConfigPath(io.env)));
  io.stdout.write(`${updatedExisting ? "Updated" : "Created"} ${target}${labels.length ? ` with ${labels.join(" and ")} defaults` : " with core defaults"}\n`);
  io.stdout.write(`relaymux home: ${homeDir} (state ${resolveStateDir(config, io.env)}, logs ${resolveLogDir(config, io.env)})\n`);
  if (labels.length) {
    io.stdout.write("Next: relaymux restart-launch-agent (starts the background service)\n");
    io.stdout.write("Then: relaymux status-launch-agent && relaymux status\n");
  } else {
    io.stdout.write("Tip: add optional adapters later with `relaymux setup --imsg --no-launch-agent` or `relaymux setup --telegram --no-launch-agent`.\n");
  }
  if (flags.installLaunchAgent) {
    installLaunchAgent({
      flags: { load: flags.load },
      configInfo: { config, path: target, exists: true },
      binPath: process.argv[1],
      io,
      platform,
    });
  }
  return 0;
}

function backgroundServiceDryRunName(platform) {
  if (platform === "darwin") return "macOS LaunchAgent and watchdog";
  if (platform === "linux") return "Linux systemd user service";
  return `background service if supported on ${platform}`;
}

function backgroundServiceStatusName(status, platform) {
  if (status.serviceManager === "systemd") return "Linux systemd user service";
  if (status.serviceManager === "launchd" || platform === "darwin") return "macOS LaunchAgent";
  return "background service";
}

function backgroundServiceNeedsSetupFailure(status) {
  if (status.serviceManager === "unsupported") return false;
  return !status.loaded;
}

function telegramFlagsWithExisting(flags, config) {
  const telegram = getIntegration(config, "telegram");
  return {
    ...flags,
    telegramChatId: flags.telegramChatId || telegram.chatId,
    telegramBotTokenFile: flags.telegramBotTokenFile || telegram.botTokenFile,
    telegramBotTokenEnv: flags.telegramBotTokenEnv || telegram.botTokenEnv,
  };
}

function cloneConfig(config) {
  return JSON.parse(JSON.stringify(config));
}

function handleMigrateHome(flags, configInfo, io) {
  const inventory = buildHomeMigrationInventory({
    homeDir: flags.home || flags.relaymuxHome,
    targetConfigPath: flags.targetConfig,
    legacyConfigPath: flags.legacyConfig,
    legacyStateDir: flags.legacyStateDir,
    orchestratorImessageDir: flags.orchestratorImessageDir,
    agentmuxConfigPath: flags.agentmuxConfig,
    agentmuxStateDir: flags.agentmuxStateDir,
    configPath: configInfo.exists ? configInfo.path : undefined,
    config: configInfo.config,
  }, io.env);

  const applying = flags.apply === true && flags.dryRun !== true;
  if (flags.json && !applying) {
    io.stdout.write(`${JSON.stringify(inventory, null, 2)}\n`);
    return 0;
  }

  io.stdout.write(formatHomeMigrationInventory(inventory, {
    applying,
    force: Boolean(flags.force),
    symlink: Boolean(flags.symlink),
  }));

  if (!applying) return 0;

  const results = applyHomeMigration(inventory, {
    force: Boolean(flags.force),
    symlink: Boolean(flags.symlink),
    env: io.env,
  });
  if (flags.json) {
    io.stdout.write(`${JSON.stringify({ inventory, results }, null, 2)}\n`);
  } else {
    io.stdout.write(formatHomeMigrationResults(results));
  }
  return 0;
}

function handleDb(flags, positionals, io) {
  const subcommand = positionals[0] || "status";
  const dbPath = relaymuxDbPath(io.env);

  if (subcommand === "path") {
    if (flags.json) {
      io.stdout.write(`${JSON.stringify({ path: dbPath }, null, 2)}\n`);
    } else {
      io.stdout.write(`${dbPath}\n`);
    }
    return 0;
  }

  if (subcommand === "schema") {
    io.stdout.write(expectedSchemaSql());
    return 0;
  }

  if (subcommand === "init") {
    const result = initRelaymuxDb({ dbPath, env: io.env });
    if (flags.json) {
      io.stdout.write(`${JSON.stringify(dbCommandResult(result), null, 2)}\n`);
      return 0;
    }

    io.stdout.write(`DB: ${result.dbPath}\n`);
    io.stdout.write(`sqlite3: ${result.sqlitePath}\n`);
    if (result.applied.length === 0) {
      io.stdout.write(`Schema: up to date at version ${result.currentVersion}\n`);
    } else {
      io.stdout.write(`Applied ${result.applied.length} migration(s): ${formatMigrationList(result.applied)}\n`);
      io.stdout.write(`Schema: version ${result.currentVersion}\n`);
    }
    return 0;
  }

  if (subcommand === "status") {
    const status = relaymuxDbStatus({ dbPath, env: io.env });
    if (flags.json) {
      io.stdout.write(`${JSON.stringify(status, null, 2)}\n`);
    } else {
      io.stdout.write(formatDbStatus(status));
    }
    return status.sqlite.available && !status.error ? 0 : 1;
  }

  throw new Error(`Unknown db command "${subcommand}". Use path, init, status, or schema.`);
}

function dbCommandResult(result) {
  return {
    dbPath: result.dbPath,
    sqlitePath: result.sqlitePath,
    applied: result.applied,
    migrations: result.migrations,
    currentVersion: result.currentVersion,
    expectedVersion: result.expectedVersion,
  };
}

function formatDbStatus(status) {
  const lines = [
    `DB: ${status.dbPath}`,
    `sqlite3: ${status.sqlite.available ? status.sqlite.path : "missing"}`,
    `exists: ${status.exists ? "yes" : "no"}`,
    `initialized: ${status.initialized ? "yes" : "no"}`,
    `schema: current ${status.currentVersion}, expected ${status.expectedVersion}, pending ${status.pending.length}`,
  ];
  if (status.migrations.length > 0) {
    lines.push(`migrations: ${formatMigrationList(status.migrations)}`);
  }
  if (status.pending.length > 0) {
    lines.push(`pending: ${formatMigrationList(status.pending)}`);
  }
  if (status.error) {
    lines.push(`error: ${status.error}`);
  }
  return `${lines.join("\n")}\n`;
}

function formatMigrationList(migrations) {
  return migrations.map((migration) => `v${migration.version} ${migration.name}`).join(", ");
}

function handleLaunch(flags, configInfo, stateDir, io) {
  const config = configInfo.config;
  const agent = resolveAgentConfig(config, flags.agent);
  assertNoFatalCommandFindings(agent.agentName, agent.agentConfig, { location: `agents.${agent.agentName}` });

  if (flags.executor !== undefined || flags.mode !== undefined || flags.group !== undefined) {
    throw new Error("relaymux launch only supports tmux; use --session to choose a tmux session.");
  }

  const prompt = resolvePrompt(flags);
  const runId = flags.runId || makeRunId();
  const { repo, workdir, worktreeAddArgs } = resolveRepoAndWorkdir(flags);
  const name = sanitizeLaunchName(flags.name || `${agent.agentName}-${path.basename(workdir)}-${runId.slice(-6)}`);
  const sessionInfo = resolveLaunchSession({ flags, config, env: io.env, repo, workdir, name });
  const holdOnExit = flags.hold ?? config.holdOnExit ?? false;
  const launchNotification = resolveLaunchNotification(flags, config);

  if (!flags.dryRun && worktreeAddArgs) {
    createWorktree(worktreeAddArgs);
  }

  launchTmuxAgent({
    agentConfig: agent.agentConfig,
    agentName: agent.agentName,
    attach: Boolean(flags.attach),
    cliPath: process.argv[1],
    configPath: configInfo.path,
    dryRun: Boolean(flags.dryRun),
    holdOnExit,
    io,
    launchNotification,
    name,
    printCommand: Boolean(flags.printCommand),
    prompt,
    quoteArgv,
    repo,
    runId,
    session: sessionInfo.session,
    sessionInfo,
    stateDir,
    workdir,
    worktreeAddArgs,
  });

  return 0;
}

async function handleAsk(flags, positionals, configInfo, io) {
  const text = resolveRequestText(flags, positionals);
  const replyMode = flags.replyMode || "none";
  if (!isReplyMode(replyMode)) {
    throw new Error(`--reply-mode must be ${replyModesText()}`);
  }

  const metadata = flags.metadataJson ? parseMetadataJson(flags.metadataJson) : {};
  const wait = flags.wait !== false;
  const result = await postDaemonRequest(configInfo.config, {
    text,
    source: flags.from || "terminal",
    metadata,
    replyMode,
    wait,
  }, Number(flags.timeoutMs || 0));

  if (!wait) {
    io.stdout.write(`Queued terminal request ${result.requestId}\n`);
    return 0;
  }

  if (!result.ok) {
    throw new Error(result.error || "terminal request failed");
  }

  io.stdout.write(`${String(result.reply || "Done.").trim()}\n`);
  return 0;
}

function handleStartTmux(flags, configInfo, stateDir, io) {
  if (!flags.allowTmuxDaemon) {
    throw new Error("start-tmux daemon mode is retired: the relaymux background service must run directly outside tmux (LaunchAgent on macOS, systemd user service on Linux). Use `relaymux restart-launch-agent` for the background service and `relaymux launch` for agent tmux tabs.");
  }
  if (!flags.session) {
    throw new Error("Missing --session <name> for start-tmux");
  }

  const config = configInfo.config;
  const session = String(flags.session);
  validateSessionName(session);
  const windowName = sanitizeLaunchName(flags.windowName || "relaymux-daemon");
  const cwd = expandPath(config.orchestrator?.cwd || "~");
  const daemonArgv = [process.execPath, process.argv[1], "--config", configInfo.path, "daemon", "--session", session];
  const exitBehavior = flags.hold
    ? [
        'printf "\\nrelaymux daemon exited with status %s\\n" "$status"',
        'exec "${SHELL:-/bin/sh}"',
      ]
    : ['exit "$status"'];
  const daemonShellCommand = [
    "set +e",
    shellExportBlock({
      RELAYMUX_CONFIG: configInfo.path,
      RELAYMUX_SESSION: session,
    }),
    quoteArgv(daemonArgv),
    "status=$?",
    ...exitBehavior,
  ].join("\n");

  if (flags.dryRun) {
    io.stdout.write(`# session: ${session}\n`);
    io.stdout.write(`# window: ${windowName}\n`);
    io.stdout.write(`# cwd: ${cwd}\n`);
    io.stdout.write("# leaving configured background service alone\n");
    if (flags.restart !== false) {
      io.stdout.write(`# would replace existing tmux window ${session}:${windowName} if present\n`);
    }
    io.stdout.write("# daemon shell command:\n");
    io.stdout.write(`${daemonShellCommand}\n`);
    for (const extraWindow of resolveExtraTmuxWindows(config, { configPath: configInfo.path, session, stateDir })) {
      io.stdout.write(`# extra ${extraWindow.mode}: ${extraWindow.name}\n`);
      io.stdout.write(`# extra cwd: ${extraWindow.cwd}\n`);
      if (flags.restart !== false && extraWindow.restart !== false) {
        io.stdout.write(`# would remove existing tmux window ${session}:${extraWindow.name} if present\n`);
      }
      io.stdout.write(`${extraWindow.shellCommand}\n`);
    }
    return 0;
  }

  ensureDirectory(stateDir);
  if (flags.restart !== false) {
    killWindowByName({ session, name: windowName });
  }

  const target = createCommandWindow({
    session,
    name: windowName,
    cwd,
    shellCommand: daemonShellCommand,
  });
  const started = new Date().toISOString();
  setWindowMetadata(target.windowTarget, {
    relaymux: "1",
    relaymux_agent: "daemon",
    relaymux_config: configInfo.path,
    relaymux_daemon: "1",
    relaymux_name: windowName,
    relaymux_repo: cwd,
    relaymux_run_id: `daemon-${session}`,
    relaymux_session: session,
    relaymux_started: started,
  });

  const extraTargets = [];
  for (const extraWindow of resolveExtraTmuxWindows(config, { configPath: configInfo.path, session, stateDir })) {
    if (extraWindow.deprecatedMode) {
      io.stderr.write(`relaymux: tmux.extraWindows mode=${extraWindow.deprecatedMode} is deprecated; creating a normal tmux tab/window instead.\n`);
    }
    if (flags.restart !== false && extraWindow.restart !== false) {
      killWindowByName({ session, name: extraWindow.name });
    }

    const extraTarget = createCommandWindow({
      session,
      name: extraWindow.name,
      cwd: extraWindow.cwd,
      shellCommand: extraWindow.shellCommand,
    });
    setWindowMetadata(extraTarget.windowTarget, {
      relaymux: "1",
      relaymux_agent: "extra",
      relaymux_config: configInfo.path,
      relaymux_extra: "1",
      relaymux_name: extraWindow.name,
      relaymux_repo: extraWindow.cwd,
      relaymux_run_id: `extra-${session}-${extraWindow.name}`,
      relaymux_session: session,
      relaymux_started: started,
    });
    extraTargets.push({ ...extraWindow, target: extraTarget.target });
  }

  io.stdout.write(`Started relaymux daemon in tmux target ${target.target}\n`);
  for (const extraTarget of extraTargets) {
    io.stdout.write(`Started extra tmux ${extraTarget.mode} ${extraTarget.name} in target ${extraTarget.target}\n`);
  }
  io.stdout.write(`Session: ${session}\n`);
  io.stdout.write(`Attach with: tmux attach -t ${session}\n`);
  io.stdout.write(`Logs: tmux capture-pane -pt ${target.windowTarget} -S -200\n`);
  if (flags.attach) {
    io.stdout.write(`Attach command: tmux attach -t ${session}\n`);
  }
  return 0;
}

async function handleSuperviseTmux(flags, configInfo, stateDir, io) {
  if (!flags.allowTmuxDaemon) {
    throw new Error("supervise-tmux daemon mode is retired: the relaymux background service must run directly outside tmux.");
  }
  const config = configInfo.config;
  const session = String(flags.session || io.env.RELAYMUX_SESSION || config.session || "agents");
  validateSessionName(session);
  const windowName = sanitizeLaunchName(flags.windowName || "relaymux-daemon");
  const configuredIntervalMs = Number(flags.intervalMs || config.daemon?.supervisorPollMs || 15000);
  const intervalMs = Number.isFinite(configuredIntervalMs) ? Math.max(1000, configuredIntervalMs) : 15000;
  let checking = false;

  const log = (...args) => io.stdout.write(`[${new Date().toISOString()}] ${args.join(" ")}\n`);
  const warn = (...args) => io.stderr.write(`[${new Date().toISOString()}] ${args.join(" ")}\n`);
  const startFlags = {
    ...flags,
    session,
    windowName,
    attach: false,
    dryRun: false,
    keepLaunchAgent: true,
    stopLaunchAgent: false,
    restart: flags.restart !== false,
  };

  function ensureStarted() {
    if (checking) return;
    checking = true;
    try {
      const reason = tmuxStackRepairReason(config, session, windowName);
      if (!reason) return;

      log(`starting relaymux tmux stack: ${reason}`);
      handleStartTmux(startFlags, configInfo, stateDir, io);
    } catch (error) {
      warn("tmux supervisor check failed:", error?.message || String(error));
    } finally {
      checking = false;
    }
  }

  if (flags.dryRun) {
    return handleStartTmux({ ...startFlags, dryRun: true }, configInfo, stateDir, io);
  }

  log(`supervising relaymux tmux session ${session} window ${windowName} every ${intervalMs}ms`);
  ensureStarted();
  if (flags.once) return 0;

  const interval = setInterval(ensureStarted, intervalMs);
  await new Promise<void>((resolve) => {
    const shutdown = (signal) => {
      log(`stopping tmux supervisor (${signal})`);
      clearInterval(interval);
      resolve();
    };
    process.once("SIGTERM", () => shutdown("SIGTERM"));
    process.once("SIGINT", () => shutdown("SIGINT"));
  });
  return 0;
}

function tmuxStackRepairReason(config, session, windowName) {
  const windows = listAgentWindows({ session });
  const daemonWindow = windows.find((window) => window.agent === "daemon" && (window.name === windowName || window.windowName === windowName));
  if (!daemonWindow) {
    return `daemon window ${session}:${windowName} is missing`;
  }

  const extraWindows = Array.isArray(config.tmux?.extraWindows) ? config.tmux.extraWindows : [];
  for (const [index, extraWindow] of extraWindows.entries()) {
    const name = sanitizeLaunchName(extraWindow.name || `extra-${index + 1}`);
    const exists = windows.some((window) => window.agent === "extra" && (window.name === name || window.windowName === name));
    if (!exists) {
      return `extra window ${session}:${name} is missing`;
    }
  }

  return "";
}

function resolveExtraTmuxWindows(config, context) {
  const extraWindows = config.tmux?.extraWindows;
  if (!Array.isArray(extraWindows)) return [];

  return extraWindows.map((extraWindow, index) => {
    const name = sanitizeLaunchName(extraWindow.name || `extra-${index + 1}`);
    const templateContext = {
      ...context,
      name,
      index: String(index),
    };
    const cwd = expandPath(renderTemplate(extraWindow.cwd || config.orchestrator?.cwd || "~", templateContext));
    const deprecatedMode = extraWindow.mode === "pane" ? "pane" : "";
    const env: Record<string, string> = {
      RELAYMUX_CONFIG: context.configPath,
      RELAYMUX_SESSION: context.session,
    };
    for (const [key, value] of Object.entries(extraWindow.env ?? {})) {
      env[key] = renderTemplate(value, templateContext);
    }

    let commandText;
    if (Array.isArray(extraWindow.command) && extraWindow.command.length > 0) {
      commandText = quoteArgv(extraWindow.command.map((part) => renderTemplate(part, templateContext)));
    } else if (extraWindow.shellCommand) {
      commandText = renderTemplate(extraWindow.shellCommand, templateContext);
    } else {
      throw new Error(`tmux extra window "${name}" must define command or shellCommand`);
    }

    const shellCommand = [
      "set +e",
      shellExportBlock(env),
      `exec ${commandText}`,
    ].join("\n");

    return {
      name,
      cwd,
      deprecatedMode,
      mode: "window",
      restart: extraWindow.restart,
      shellCommand,
    };
  });
}

function handleDoctor(configInfo, io, platform = process.platform) {
  const checks = collectDoctorChecks(configInfo.config, configInfo, io.env, { platform });
  for (const check of checks) {
    io.stdout.write(`${doctorStatusLabel(check)}\t${check.name}\t${check.detail}\n`);
  }
  return checks.every((check) => check.ok || check.fatal === false || check.severity === "warning") ? 0 : 1;
}

function doctorStatusLabel(check) {
  if (check.ok) return "ok";
  if (check.severity === "warning") return "warning";
  if (check.severity === "error") return "error";
  if (check.fatal === false) return "warning";
  return "missing";
}

function resolvePrompt(flags) {
  if (flags.promptFile) {
    return readTextFile(expandPath(flags.promptFile));
  }
  if (flags.prompt === undefined) {
    throw new Error("Missing --prompt <text|@file> or --prompt-file <path>");
  }

  if (flags.prompt.startsWith("@")) {
    return readTextFile(expandPath(flags.prompt.slice(1)));
  }

  const maybePath = expandPath(flags.prompt);
  if (!flags.prompt.includes("\n") && pathExists(maybePath) && fs.statSync(maybePath).isFile()) {
    return readTextFile(maybePath);
  }

  return flags.prompt;
}

function resolveRequestText(flags, positionals) {
  if (flags.prompt !== undefined || flags.promptFile) {
    return resolvePrompt(flags);
  }
  if (flags.message !== undefined) {
    return String(flags.message);
  }
  if (positionals.length > 0) {
    return positionals.join(" ");
  }
  throw new Error("Missing request text. Use `relaymux ask <text>` or --message <text>.");
}

function parseMetadataJson(raw) {
  try {
    const parsed = JSON.parse(String(raw));
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      throw new Error("metadata must be a JSON object");
    }
    return parsed;
  } catch (error) {
    throw new Error(`Invalid --metadata-json: ${error.message}`);
  }
}

async function postDaemonRequest(config, body, timeoutMs): Promise<any> {
  const resolved = webhookConfig(config);
  const token = fs.readFileSync(resolved.tokenFile, "utf8").trim();
  if (!token) throw new Error(`Webhook token file is empty: ${resolved.tokenFile}`);

  const payload = Buffer.from(JSON.stringify(body));
  const hostname = String(resolved.host).replace(/^\[(.*)\]$/, "$1");
  return new Promise<any>((resolve, reject) => {
    const req = http.request({
      hostname,
      port: resolved.port,
      path: "/request",
      method: "POST",
      headers: {
        authorization: `Bearer ${token}`,
        "content-type": "application/json",
        "content-length": String(payload.length),
      },
    }, (res) => {
      const chunks = [];
      res.on("data", (chunk) => chunks.push(chunk));
      res.on("end", () => {
        const raw = Buffer.concat(chunks).toString("utf8");
        let parsed;
        try {
          parsed = raw ? JSON.parse(raw) : {};
        } catch {
          reject(new Error(`daemon returned non-JSON response (${res.statusCode}): ${raw.slice(0, 500)}`));
          return;
        }
        if ((res.statusCode || 500) >= 400) {
          reject(new Error(parsed.error || `daemon request failed with HTTP ${res.statusCode}`));
          return;
        }
        resolve(parsed);
      });
    });
    req.on("error", (error) => reject(new Error(`Could not reach relaymux daemon at ${hostname}:${resolved.port}: ${error.message}`)));
    if (timeoutMs > 0) {
      req.setTimeout(timeoutMs, () => req.destroy(new Error(`Timed out after ${timeoutMs}ms waiting for relaymux daemon`)));
    }
    req.end(payload);
  });
}

function defaultIo() {
  return {
    env: process.env,
    platform: process.platform,
    stdin: process.stdin,
    stdout: process.stdout,
    stderr: process.stderr,
  };
}

function helpText() {
  return `relaymux - coordinate local CLI agents in tmux with optional notification adapters

Mental model:
  - The background daemon/local API runs directly outside tmux (LaunchAgent on macOS, systemd user service on Linux) when installed.
  - By default, all agents open as tmux tabs/windows in one shared session.
  - Managed config/state/logs/prompts/scratch live under ~/.relaymux by default.
  - Use --session only when you explicitly want a separate/new/named tmux session; panes/splits are never used.

Start here:
  relaymux setup
  relaymux status

Usage:
  relaymux setup --telegram --telegram-bot-token <token>
  relaymux setup --imsg
  relaymux setup [--imsg|--telegram] [--chat-id <id>] [--telegram-chat-id <id>] [--no-launch-agent] [--dry-run]
  relaymux install-launch-agent [--dry-run] [--no-load] [--no-watchdog]
  relaymux restart-launch-agent [--dry-run] [--no-load] [--no-watchdog]
  relaymux status-launch-agent [--json]
  relaymux uninstall-launch-agent
  relaymux launch --repo <path> --agent <name> --prompt <text|@file> [--name <name>] [--notify-on-exit never|failure|always]
  relaymux ask <text> [--no-wait] [--reply-mode imessage|telegram|none]
  relaymux schedule add --name <name> --prompt <text> --cron "0 9 * * *" [--reply-mode none|imessage|telegram] [--scheduler auto|launchd|cron]
  relaymux schedule list|remove [--name <name>]
  relaymux status [--json] [--session <name>]
  relaymux notify [--run-id <id>] [--reply-mode imessage|telegram|none] [--message <text>] [--suicide]
  relaymux db path|init|status [--json]
  relaymux db schema
  relaymux migrate-home [--dry-run] [--apply] [--symlink]
  relaymux doctor

Setup options:
  --imsg                    Enable the optional iMessage/SMS adapter and prompt for a chat when possible
  --telegram                Enable the optional Telegram adapter; waits for /start when chat id is missing
  --chat-id <id>            Messages chat id/phone for imsg history/send; omit to pick from recent chats
  --telegram-chat-id <id>   Telegram chat id for Bot API sendMessage; omit to discover from /start
  --telegram-bot-token <token>      Telegram bot token to store in ~/.relaymux/secrets/telegram-bot-token
  --telegram-bot-token-env <name>   Env var that contains the Telegram bot token (default TELEGRAM_BOT_TOKEN)
  --telegram-bot-token-file <path>  File that contains the Telegram bot token (contents are never printed)
  --telegram-parse-mode <mode>      Optional Telegram parse_mode such as MarkdownV2 or HTML
  --telegram-timeout-ms <ms>        Telegram sendMessage timeout (default 30000)
  --cwd <path>              Working directory for Pi and message commands
  --state-dir <path>        State/token/log directory
  --install-launch-agent    Install the direct/background service after writing config (LaunchAgent on macOS, systemd on Linux)
  --no-launch-agent         For setup: skip background service installation
  --dry-run                 For setup: print planned config/service changes without writing files

Migration options:
  --apply                   Copy inventoried relaymux-owned files into ~/.relaymux
  --symlink                 After copying, replace old relaymux-owned source paths with symlinks
  --home <path>             Override target relaymux home (default ~/.relaymux)
  --legacy-config <path>    Override legacy config path to inventory
  --legacy-state-dir <path> Override legacy state dir to inventory

Launch options:
  --prompt-file <path>       Read prompt from a file
  --session <name>           Explicitly launch this agent into a separate/named tmux session
  --session-mode <mode>      shared (default) or per-worktree
  --worktree <path>          Launch from a generic git worktree path
  --create-worktree          Create --worktree with git worktree add when missing
  --worktree-branch <name>   Branch name to use with --create-worktree/session naming
  --worktree-from <ref>      Starting ref to use with --create-worktree
  --dry-run                  Print the tmux tab command without launching
  --print-command            Print the tmux tab command before launching
  --hold                     Keep a shell open after the agent exits
  --attach                   Print attach command after launch
  --notify-on-exit <mode>    Auto relaymux notify on agent exit: never (default), failure, or always
  --notify-reply-mode <mode> imessage/telegram sends adapter updates; none records quiet completion context
  --notify-tail-lines <n>    Recent tmux output lines to include for nonzero auto notifications
  --notify-tail-bytes <n>    Max bytes of recent tmux output in nonzero auto notifications

Background service options:
  --no-load                  Write the service file without loading/starting it
  --no-watchdog              On macOS, skip installing the periodic LaunchAgent health watchdog
  --keep-tmux-daemon         During migration, do not stop an old relaymux-daemon tmux tab

Request/notify/status options:
  --message <text>           Request/notify message text
  --no-wait                  For ask/request: enqueue and return immediately
  --timeout-ms <ms>          For ask/request: client-side wait timeout
  --reply-mode <mode>        imessage/telegram sends an adapter update; none is quiet/no text
  --from <name>              For notify: source/subagent name
  --idempotency-key <key>    For notify: suppress duplicate completion webhook retries
  --metadata-json <json>     For notify: optional metadata object
  --suicide                  For notify: after success, close only the current tmux window
  --history                  For status: include old run records whose tmux tabs are gone

DB options:
  relaymux db path            Print the canonical relaymux SQLite path
  relaymux db init            Create/update the first-party relaymux SQLite schema; requires sqlite3
  relaymux db status          Report sqlite3 availability, DB existence, and migration state
  relaymux db schema          Print the expected first-party relaymux schema SQL

Schedule options:
  --cron <expr>              Five-field cron schedule for relaymux schedule add
  --scheduler <backend>      auto, launchd, or cron (default auto)
  --prompt-file <path>       Read scheduled prompt text from a file
  --reply-mode <mode>        Scheduled ask reply mode: none, imessage, or telegram
  --no-load                  Write schedule files without installing/loading the OS job

Useful commands:
  tmux attach -t <session>       attach to the shared or named agent session
  tmux kill-session -t <session> kill only that tmux session; background daemon/adapters keep running

Default home layout: ~/.relaymux/{config.json,relaymux.sqlite3,state,logs}.
Config defaults to ${defaultConfigPath()} (legacy ~/.config/relaymux/config.json is still read if the new config does not exist).
`;
}
