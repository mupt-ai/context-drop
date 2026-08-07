# Pairing machines

Pairing lets several machines or agents share a machine chain. A machine chain is the account boundary for uploads, listing, pulling, machine membership, and messages.

## Chain session tokens and join tokens

A **chain session token** is stored on each machine after `context-drop init` or `context-drop join <token>`. It authorizes chain-scoped commands from that machine.

A **join token** is a random, single-use invite token. It expires quickly, defaults to 10 minutes, and cannot exceed 15 minutes. A join token lets one new machine join the chain and receive its own chain session token.

## Create the first chain

On the first machine:

```sh
context-drop init --machine-name laptop
```

If you omit `--machine-name`, Context Drop uses the hostname when available and falls back to `machine`.

## Join another machine

On an already joined machine:

```sh
context-drop token create --ttl 5m
```

On the new machine:

```sh
context-drop join <token> --machine-name desktop
```

Any joined machine can create the next join token.

## Name machines carefully

Machine names are human-friendly labels. They do not have to be globally unique, but commands that address a machine by name require that the name be unique inside the chain. If two machines have the same name, `context-drop send --to <name>` fails with an ambiguity error. Use the machine ID instead.

List machines to find IDs and names:

```sh
context-drop machines list
```

The text output is:

```text
<machine_id>    <name>    <last_seen_at>
```

Use JSON when a script needs structured output:

```sh
context-drop machines list --json
```

## Send and read messages

A chain message is a small text message sent from one joined machine to another joined machine.

Send a message by machine ID or unique machine name:

```sh
context-drop send --to <machine-id-or-unique-name> "hello from laptop"
```

Read messages sent to the current machine:

```sh
context-drop messages list
```

Use JSON output for scripts:

```sh
context-drop messages list --json
```

Messages are trimmed, cannot be empty, and are limited to 4096 characters.

## Send files to one machine

If `send` receives exactly one argument and that argument is an existing regular file, Context Drop uploads it as a normal drop and sends a message with the drop id, URL, and filename:

```sh
context-drop send --to desktop ./spec.md
```

The target machine can read the message and pull the drop:

```sh
context-drop messages list
context-drop pull <drop-id>
```

The drop URL is still a public bearer URL until expiry. Targeting controls routing/discovery inside the chain; it does not make the public URL private from someone who already has it.

## Pairing with a self-hosted endpoint

Pairing commands use the configured endpoint. If you are testing a local or self-hosted server, either set the endpoint once:

```sh
context-drop config set endpoint http://localhost:8080
context-drop init
context-drop token create
context-drop join <token>
```

or pass it on each command:

```sh
context-drop init --endpoint http://localhost:8080
context-drop token create --endpoint http://localhost:8080
context-drop join <token> --endpoint http://localhost:8080
```

## Common pairing errors

`invalid or expired join token` means the token was already used, expired, typed incorrectly, or belongs to a different server endpoint. Create a fresh token on an already joined machine and retry.

`not initialized or joined; run context-drop init or context-drop join <token>` means the current machine does not have a chain session token. Initialize a new chain or join an existing one first.

`machine name is ambiguous` means more than one machine in the chain has the same name. Run `context-drop machines list` and address the target by machine ID.

`message is empty` means the message only contained whitespace after trimming. Send non-empty text.
