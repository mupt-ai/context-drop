# Context Drop

Context Drop lets a chat assistant start, message, and read coding agents in [Herdr](https://herdr.dev) tabs. Its CLI also uploads files to a temporary file store. The assistant handles messaging, scheduling, and memory; Context Drop has no daemon.

## Build and try it

Requires Go 1.26.2+, [Herdr](https://herdr.dev), and the `dari` agent launcher. Set up Herdr and `dari` before using the agent commands. From this repository:

```sh
cd cli
make install                 # installs context-drop to ~/.local/bin
~/.local/bin/context-drop --help
```

Add `~/.local/bin` to your `PATH` if it is not already there. No release binaries are published yet.

To start an agent, first create a Herdr workspace and choose an existing project directory (or Git worktree). Replace `YOUR_WORKSPACE` with that workspace's label and `/absolute/path/to/project` with the directory's absolute path. The example uses `pi`; `claude` (the default) and `codex` are also supported if installed through `dari`:

```sh
context-drop new my-task 'Inspect the failing tests' --workspace YOUR_WORKSPACE --cwd /absolute/path/to/project --agent pi --wait
context-drop send my-task 'Summarize your findings' --wait
context-drop read my-task
context-drop agents
```

`new` opens a tab in the named workspace and launches the agent through `dari`. `send` addresses an existing agent by name or pane ID; it refuses while the agent is busy unless you use `--force`. `--wait` prints the result when the agent finishes or needs input. See the [CLI reference](docs/cli.md) for other commands and options.

## File uploads

```sh
context-drop upload ./report.txt --ttl 1h
```

Uploads need a running server and its upload token. Set `CONTEXT_DROP_ENDPOINT` and `CONTEXT_DROP_UPLOAD_TOKEN` to the server URL and the same token configured on that server. The [upload server guide](docs/server.md) shows how to run it locally; the [CLI reference](docs/cli.md) covers other configuration options. The server lives in `cli/` and provides authenticated uploads and expiring download links.

`cli/jobs/` contains optional standalone launchd jobs for a Herdr activity journal, tab naming, and health-dashboard alert cooldown. Each has its own README. Run the CLI and server tests with `cd cli && make test`.

## License

[MIT](LICENSE).
