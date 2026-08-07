# First handoff

On the sending machine, initialize and pair a recipient. Then:

```sh
context-drop handoff create \
  --to server \
  --summary "Review the failing test output; do not modify files" \
  --action "Return a proposed fix" \
  --artifact ./test-output.log
```

On the recipient:

```sh
context-drop inbox
context-drop inspect hnd_...
context-drop accept hnd_...
```

`accept` creates a new `0700` staging directory under the private Context Drop state directory and writes attachments with `0600` permissions. Arbitrary `--into` paths are refused; filenames are sanitized, collisions and digest mismatches are rejected. It does not execute content. Acceptance claims the handoff before downloading. An interrupted claim can be retried after five minutes; a completed acceptance is terminal.
