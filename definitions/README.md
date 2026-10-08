# Filesystem catalog reference

`definitions` loads agent behavior and trusted deployment profiles separately.
All authored model tools are denied. `github.diff` grants the application access
to a diff that the handler supplies in the task prompt, not a model tool.

## Public API

```go
func Load(root string) (*Catalog, error)
func (catalog *Catalog) Resolve(name string) (orchestration.Definition, error)
func (catalog *Catalog) Snapshot(name string) (Snapshot, error)
func (catalog *Catalog) Save(root string) error
func (snapshot Snapshot) Definition() (orchestration.Definition, error)
func (snapshot Snapshot) ValidateOutput(output json.RawMessage) error
func SaveSnapshot(root string, snapshot Snapshot) error
func ReadSnapshot(root, digest string) (Snapshot, error)
```

`Catalog` exposes `Agents map[string]Agent`, `Automations map[string]Automation`,
and `Profiles map[string]Profile`. `Snapshot` contains `Agent Agent` and
`Profile Profile`. `Model`, `Execution`, `Trigger`, and `Policy` expose typed
behavior settings. `Profile.UnmarshalYAML(*yaml.Node) error` supports strict
decoding of its JSON-compatible `config` field.

## Source layout

```text
root/
  deployment.yaml
  agents/<name>/agent.yaml
  agents/<name>/instructions.md
  agents/<name>/skills/<skill>/SKILL.md   optional
  agents/<name>/output.schema.json      optional
  automations/<name>.yaml               optional
```

Agent YAML requires `version: 1`, `description`, `model: {provider, id}`,
`execution: {profile, timeout_seconds}`, and an optional `capabilities` list.
Timeouts range from 1 to 86400 seconds. `output_schema` selects a file in that
agent's directory. It cannot contain path separators. Instructions and skill
files must contain non-whitespace text. Names contain ASCII letters, digits,
underscores, or hyphens and start with a letter or digit.

Deployment YAML has `version: 1` and a `profiles` map. Each profile has `pool`,
`namespace`, an absolute `directory`, optional `tags` and `capabilities` lists,
optional `secret_files`, and optional `config`. The only supported capability
is `github.diff`; a profile must grant every capability its agent requests.
An empty capability list is valid for plain-prompt agents.

`secret_files` maps provider IDs to absolute credential file paths. Paths must
be clean and cannot contain traversal or symlinks. Catalog loading does not
open these files. `Definition.Secrets` reads only the approved bindings, trims
surrounding whitespace, and fails on missing or empty credentials.

Profile `config` accepts only a top-level `providers` object. It cannot supply
agents, permissions, tools, plugins, MCP servers, or ambient instructions.
Provider configuration is trusted, non-secret deployment input. Credentials
belong in `secret_files`, not literal configuration values. Whole-value
`{"$secret":"openai"}` placeholders can reference approved provider bindings.
Unknown bindings fail catalog loading; missing supplied values fail compilation.

Automation YAML requires `version: 1`, an `agent` reference,
`handler: github.pr-review`, `trigger: {adapter: github.pull_request, actions: [...]}`,
and `policy: {concurrency: pull-request, limit: 1, deduplication: reviewed-revision}`.
Actions are `opened`, `synchronize`, and `ready_for_review`; at least one is
required. Review agents must request `github.diff` and declare an output schema.
Unknown fields, versions, references, actions, and capabilities are errors.
Symlinks and non-regular files anywhere under the source root are rejected.

## Compilation and output validation

Compilation selects `authored` and maps models to the V2 `providerID` and `id` fields.
It combines instructions with skill content in sorted skill-name order.
OpenCode V2 configuration contains `agents.authored.system`, `mode: primary`,
and ordered `permissions` arrays at both global and agent scope, each containing
`{action: "*", resource: "*", effect: "deny"}`. Project configuration is disabled.
Legacy `agent`, `prompt`, `permission`, and `tools` fields are not emitted.

`Definition.Config(secrets)` also returns an `auth` map of approved provider
credentials as `{type: "api", key: ...}`, as required by the runtime contract.
This map is transport data, not a field in upstream V2 `Config.Info`.
The runtime supervisor separates it from OpenCode configuration and writes the
V2 auth store into its private temporary home. Output endpoint wiring is in
`orchestration/output.go`.

Output schemas compile with `github.com/santhosh-tekuri/jsonschema/v6`, already
present in the module dependencies. Invalid schemas fail loading. Local schema
references are supported; external resources cannot be fetched.
`Snapshot.ValidateOutput` checks the stored digest, parses exactly one JSON
value, and validates it against the compiled schema. It fails if no schema exists.

## Snapshot storage

`Catalog.Save(root)` saves every resolved agent under
`root/snapshots/<digest>.json`. `SaveSnapshot` requires an intact snapshot from
`Catalog.Snapshot` or `ReadSnapshot`. Writes use synchronized temporary files,
an atomic exclusive hard link, and a directory synchronization. Saving the
same snapshot again succeeds; corrupt existing snapshots are never overwritten.

The SHA-256 digest includes the agent settings, instructions, skill content,
schema, resolved profile, credential binding paths, and compiler policy version.
It excludes credential file contents. JSON formatting and object-key ordering
do not affect it. Credential rotation does not change the digest.

Snapshots copy their maps and slices. Definitions also capture independent
copies. `ReadSnapshot` rejects unknown fields, invalid content, traversal in
digest arguments, symlinks, and digest mismatches. Recovery needs only the store
and digest, not the current source catalog. Secret bindings remain late-bound.

## Verification

```sh
go test -race ./definitions
go vet ./definitions
```
