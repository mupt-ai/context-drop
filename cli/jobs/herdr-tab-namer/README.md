# Herdr tab auto-namer

This workflow labels tabs using recent terminal output and a local Ollama-compatible model. It runs every minute from the `com.avyay.herdr-tab-namer` LaunchAgent in this directory; all workspaces on its configured Herdr socket are eligible.

The script honors `HERDR_BIN`, otherwise prefers `~/.local/bin/herdr`, then falls back to `herdr` on PATH. This avoids launchd selecting an older Homebrew client ahead of the user-installed client. Workspace/tab/pane discovery failures exit nonzero rather than appearing as an empty successful pass.

## Validate

```sh
node --test jobs/herdr-tab-namer/namer.test.mjs
```

For a live dry run, use the same environment as the LaunchAgent and add `--dry-run --once`. The caller must be inside Herdr. Dry runs still query the local model but do not rename tabs or update naming state.

## Install an update

Unload the LaunchAgent, back up the installed `namer.mjs`, and replace it with this version. Run a live dry run, then load the LaunchAgent again and verify a completed pass in `namer.log`.

The installed script currently lives under `~/.context-drop/managed/state/herdr-tab-namer/`; generated logs and naming state must not be committed here.
