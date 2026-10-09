# Workflows

`Load(root, catalog)` reads optional `root/workflows/<name>.yaml` manifests.
The filename supplies the workflow name. An absent directory returns an empty
map. The runtime does not supply workflows.

```yaml
steps:
  review: {agent: code-review, input: input}
  adversarial: {agent: adversarial-review, input: input}
  verify: {agent: verify, input: [review, adversarial]}
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
the resolved input must each fit within `MaxInputBytes`, 4 MiB. There are no
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
