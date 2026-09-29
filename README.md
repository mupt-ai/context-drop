# Context Drop

Context Drop starts, messages, and reads coding agents in [Herdr](https://herdr.dev) tabs. It also uploads files to a self-hosted temporary file store with expiring links. It does not provide messaging or scheduling; an assistant or script can call its CLI.

## Build from source

Requires Go 1.26.2+ and Herdr. Run agent commands inside a Herdr-managed pane. To start an agent, install and configure the corresponding agent CLI (Claude, Codex, or Pi) for Herdr. From this repository:

```sh
cd cli
make install                 # installs context-drop to ~/.local/bin
~/.local/bin/context-drop --help
```

Add `~/.local/bin` to your `PATH` if necessary. No release binaries are published yet.

To start an agent, create a Herdr workspace and choose a project directory. Replace `YOUR_WORKSPACE` with the workspace label and `/absolute/path/to/project` with your directory:

```sh
context-drop new my-task 'Inspect the failing tests' --workspace YOUR_WORKSPACE --cwd /absolute/path/to/project --agent pi --wait
context-drop send my-task 'Summarize your findings' --wait
context-drop read my-task
context-drop agents
```

`new` opens a tab and uses Herdr to start the selected agent (`claude` by default; `codex` and `pi` are also supported). `agents` lists names and pane IDs. `send` refuses an agent that is working, blocked, or in an unknown state unless you use `--force`. `--wait` prints the result when the agent finishes or needs input. See the [CLI reference](docs/cli.md).

## File uploads

```sh
context-drop upload ./report.txt --ttl 1h
```

Uploads require a running server and its upload token. Set `CONTEXT_DROP_ENDPOINT` to the server URL and `CONTEXT_DROP_UPLOAD_TOKEN` to its token. The [server guide](docs/server.md) shows how to run the bundled server with local storage; the [CLI reference](docs/cli.md) covers other configuration options. Download links are accessible to anyone who has the URL until they expire.

Run tests with `cd cli && make test`.

## License

[MIT](LICENSE).
