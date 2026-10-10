# Workflows

`Load(root, catalog)` reads optional `root/workflows/<name>.yaml` manifests.
The filename supplies the workflow name. An absent directory returns an empty
map. `workflows init PRESET NAME` creates an editable starter; nothing is registered
until the worker loads your selected workflow files.

```yaml
steps:
  review: {agent: code-review, input: input}
  adversarial: {agent: adversarial-review, input: input}
  verify: {agent: verify, input: [input, review, adversarial]}
output: verify
```

`version` defaults to 1. Every step must contribute to `output`. Names contain
at most 63 letters, digits, dashes, or underscores and start with a letter or
digit. Step IDs `input`, `resolve`, and `result` are reserved. There are at most
32 steps. Agent and step names do not prescribe roles.

`InputRefs{Sources, Multiple}` retains scalar versus list input shape. The
literal `input` refers to the initial JSON payload; other references identify
whole step results. `ResolveInput(step, initial, parents)` clones a scalar
reference's whole JSON value, or returns an array of whole values in list order.
A one-element list remains an array. Objects, arrays, strings, booleans, numbers,
and null are supported. Missing or malformed values fail. Referenced values and
the resolved input must each fit within `MaxInputBytes`, 1 MiB. There are no
selectors, expressions, or prompt interpolation.

`Capture(workflow, catalog)` resolves agents by step ID into a `Snapshot` and
requires each agent to have an output schema. It owns copies of the manifest
and resolved snapshots. `Snapshot.Order()` returns deterministic dependency
order. `Snapshot.Verify()` checks the stored manifest, validates resolved
agents through `definitions.Snapshot.Definition()`, and recomputes the digest.
Neither compilation nor verification reads provider credential files.

The canonical SHA-256 digest includes `CompilerPolicy`, the complete manifest,
and complete resolved agent snapshots, including schemas, instructions, models,
profiles, capabilities, and selected public MCP bindings. Input shape and
reference order affect identity. JSON object order and formatting do not.

`Save(root, snapshot)` stores immutable `root/workflows/<digest>.json` snapshots
using a synchronized temporary file, an exclusive hard link, and directory
synchronization. Concurrent saves of the same snapshot converge. Existing
corrupt files are rejected, never overwritten. `Read(root, digest)` rejects
unsafe paths, symlinks, unknown JSON fields, trailing JSON, and invalid stored
snapshots. It does not need the current catalog. Old plans therefore retain
their original agent settings after a deployment changes the catalog.

`Load` recognizes and validates durable digest-named JSON snapshots in the same
directory but does not return them as authored workflows. Other stray files or
subdirectories are rejected. Keep credentials outside these manifests and
snapshots; execution resolves public secret-file bindings separately.

## Bundled workflow selection

`use: pr-review` selects the runtime-owned seven-agent review graph. Do not combine
`use` with `steps` or `output`. `workflows init pr-review NAME` creates only that
selector. Deployment config supplies models/profiles/grants, not copies of prompts
or graph files. Capture expands the selector before pinning, so saved snapshots
contain the full graph and never re-resolve a pack during replay.

## Default input, cron, and notebooks

`input` at the workflow root supplies default task data. `run NAME --input FILE`
overrides it; without an override, `run NAME` uses that default. Explicit `null`
is a valid default and differs from an omitted input.

An optional `schedule: {cron: "0 8 * * *", timezone: Europe/London}` adds a Hatchet
cron trigger. Expressions have five fields; timezone defaults to UTC. The cron
input pins the complete workflow digest and default input. The agent/model/brief
used by a submitted run cannot silently change when source files change. Local
parser tests cover timezone transitions; actual timing is provided by Hatchet.

`notebook: true` is supported for one-step workflows. Its agent must explicitly
require a string `notebook` and a supported `status` enum in a direct object output
schema. The runtime provides initial input as `{"task": <input>, "notebook": "..."}`.
The agent returns full replacement notes with its report. Builtin notes are capped
at 16,384 Unicode characters, and storage enforces 64 KiB of UTF-8.

Notebook workers require a stable `AGENT_DEPLOYMENT_ID`. State is isolated by that
ID and workflow name under `AGENT_NOTEBOOK_DIR`, default `/state/notebooks`.
All workers for one deployment must use the same shared directory and ID. Use
different deployment IDs for independent owners; these are trusted operator
settings, not end-user authorization.

Hatchet limits a notebook workflow to one active run and cancels overlapping new
runs. Resolve pins the previous notes per run. Successful validated outputs commit
a new immutable notebook revision; blocked outputs preserve the previous text.
Failed executions do not update notes. Replay returns the original revision and
cannot regress newer notes. Filesystem locking and revision checks protect commits.
This needs a shared POSIX filesystem supporting flock, hard links, and fsync.

`no_change` is a result disposition for consumers, not an automatic notification.
The runtime exposes reports through Hatchet and `runs result`; sending to another
channel requires a configured tool or external consumer. Output persistence does
not guarantee exactly-once external actions.

Remove cron schedules through Hatchet when retiring a workflow. Removing its YAML
alone does not establish that existing server-side schedules have been deleted.
Keep compatible workers and snapshots for runs that are already queued.
