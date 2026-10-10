# Filesystem catalog reference

`definitions` loads agent behavior and trusted deployment profiles separately.
Authored model tools are denied unless an agent selects native tool actions or
remote MCP connections explicitly granted by its profile. `github.diff` remains an application
capability, not a model tool grant. Definitions do not load workflows.

MCP-enabled native sessions disable auxiliary title generation and explicitly
await selected connections before prompting. OpenCode 2.0.26 publishes tools after
a 100 ms debounce; the client applies a bounded 150 ms publication barrier after
successful connection status. Readiness fails closed on missing/auth-failed brokers.

## Public API

```go
func Load(root string) (*Catalog, error)
func BuiltinNames() []string
func Builtin(name string) (Agent, bool)
func (catalog *Catalog) Resolve(name string) (orchestration.Definition, error)
func (catalog *Catalog) Snapshot(name string) (Snapshot, error)
func (catalog *Catalog) Save(root string) error
func (snapshot Snapshot) Definition() (orchestration.Definition, error)
func (snapshot Snapshot) ValidateOutput(output json.RawMessage) error
func SaveSnapshot(root string, snapshot Snapshot) error
func ReadSnapshot(root, digest string) (Snapshot, error)
```

`Catalog` exposes `Agents map[string]Agent`, `Profiles map[string]Profile`,
`Defaults AgentDefaults`, and `MCPServers map[string]MCPServer`. Legacy automation
types and registration are removed. Generic graphs live in the `workflows` package.
`Snapshot` contains the resolved agent, profile, and selected MCP bindings.
`Profile.UnmarshalYAML(*yaml.Node) error` strictly decodes its JSON-compatible
`config` field.

## Source layout

```text
root/
  deployment.yaml
  agents/<name>/agent.yaml
  agents/<name>/instructions.md         optional for inherited agents
  agents/<name>/skills/<skill>/SKILL.md   optional
  agents/<name>/output.schema.json      optional
```

Standalone agent YAML requires `version: 1`, `description`, `model: {provider, id}`,
`execution: {profile, timeout_seconds}`, and an optional `capabilities` list.
Timeouts range from 1 to 86400 seconds. `output_schema` selects a file in that
agent's directory. It cannot contain path separators. Instructions and skill
files must contain non-whitespace text. Names contain ASCII letters, digits,
underscores, or hyphens and start with a letter or digit.

Deployment YAML has `version: 1`, a `profiles` map, optional `defaults`, and
optional `mcp_servers`. Each profile has `pool`,
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
Provider configuration is trusted, non-secret deployment input. Credential
fields and authentication headers must use secret placeholders, not literal
values. Credentials belong in `secret_files`. Whole-value
`{"$secret":"openai"}` placeholders can reference approved provider bindings.
Unknown bindings fail catalog loading; missing supplied values fail compilation.

An `automations` directory fails loading with a migration error. Workflow
configuration belongs outside this package. Unknown fields, versions,
references, and capabilities are errors.
Symlinks and non-regular files anywhere under the source root are rejected.

## Built-in defaults and overrides

`code-review`, `verify`, and `adversarial-review` have embedded generic Markdown
instructions and the JSON schema `{}`, which accepts any valid JSON value.
They contain no model, execution profile, path, tool grants, or workflow pipeline.
The instructions operate on supplied input and require evidence for conclusions.

When deployment `defaults` is present, the catalog resolves and snapshots all
23 built-ins. Its model and execution fields also supply omitted settings for
authored agents. Without `defaults`, legacy explicit packages load as before;
built-ins are not automatically registered. A referenced inherited agent must
still resolve a model, profile, and valid timeout.

```yaml
defaults:
  model: {provider: deployment-provider, id: deployment-model}
  execution: {profile: approved, timeout_seconds: 120}
```

`agents/verify/agent.yaml` implicitly inherits `verify`. A custom agent can select
a built-in with `extends: verify`. Only built-in names are accepted by `extends`.
Present YAML fields override defaults, including individual model and execution
fields. An absent `instructions.md` preserves inherited instructions; a present
file replaces them entirely. `output_schema` replaces the generic schema with
the named file. Built-in lookups return independent values.

Thirteen general-purpose presets return a structured result with `status`, `report`,
`sources`, and `notebook`. Their descriptions and contracts are in the
[preset guide](../docs/presets.md). All presets remain free of model choices and
tool grants. `BuiltinNames()` is the authoritative catalog.

## Native tool grants

Both the agent and its deployment profile may declare `tools` containing exact
permission actions: `read`, `glob`, `grep`, `edit`, `shell`, and `webfetch`.
The profile must grant every action selected by the agent. Lists replace inherited
lists. Unknown actions, duplicates, and wildcards are rejected.

`edit` grants the pinned runtime's edit, write, and patch operations together.
`shell` grants broad command execution inside the sandbox, including file and
network access; it is not a per-command allowlist. Explicit native reads can
encounter subordinate project instructions such as `AGENTS.md`. No preset gets
these actions automatically. `external_directory` remains denied for native
operations that enforce that permission; shell authority is broader.

Native `webfetch` reads URLs. Search and interactive browser tools can use approved
remote MCP services. Native `websearch` is not selectable because the pinned
runtime requires additional search-provider integration setup.

## Remote MCP grants

Deployment owns the approved server URLs and exact native tool lists. Agents
select connection names with `mcp: [broker]`; their profiles must grant every
selected connection with the same `mcp` list syntax. Unknown or ungranted
connections fail loading, including unknown profile grants.

```yaml
mcp_servers:
  broker:
    url: https://broker.example/mcp
    tools: [read_diff, fetch_issue]
```

Only `url` and `tools` are accepted. Commands, environment variables, OAuth
credentials, tokens, passwords, and headers are not supported. URLs reject
userinfo, queries, and fragments. HTTPS is required except for HTTP with a
literal loopback IP for tests. Broker authentication belongs to deployment
network or mTLS configuration. Agent input cannot choose an endpoint.

Native tool names must start with an ASCII letter, digit, or underscore and
contain only letters, digits, underscores, hyphens, dots, or colons. Wildcard
names, duplicate tools, and permission-name collisions fail loading.

## Compilation and output validation

Compilation selects `authored` and maps models to the V2 `providerID` and `id` fields.
It combines instructions with skill content in sorted skill-name order.
OpenCode V2 configuration contains `agents.authored.system`, `mode: primary`,
and ordered `permissions` arrays at both global and agent scope, each containing
`{action: "*", resource: "*", effect: "deny"}`. Selected MCP tools add exact
`allow` rules after that deny rule at both scopes. Only selected, profile-granted native actions add native tool allowances.
No wildcard action allowance is added. Project configuration is disabled.
Legacy `agent`, `prompt`, `permission`, and `tools` fields are not emitted.

The compiler targets OpenCode **v2.0.26**, commit
`9b4ec5714d481559990db0a816d5dec19541a814`, not the V1 configuration format.
Remote bindings use `mcp.servers.<name>` with `type: remote`, `url`,
`oauth: false`, and `codemode: false`. OAuth integration enrollment is disabled.
Only selected servers are emitted. Permissions use `<server>_<native-tool>`,
replacing tool characters outside `[a-zA-Z0-9_-]` with underscores, and
`resource: "*"`, exactly as the pinned runtime asserts.

Pinned primary sources:

- [MCP configuration container](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/schema/src/config/mcp.ts)
- [Remote server schema](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/schema/src/mcp.ts)
- [Tool action naming and permission assertion](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/core/src/tool/mcp.ts)
- [Last-matching permission evaluation](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/core/src/permission.ts)
- [OAuth enrollment exclusion](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/core/src/mcp/index.ts)

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

New schema-bearing snapshots include an explicit output-contract compiler policy.
The compiler supplies the actual schema in the model's system instructions and
requires standalone JSON. Legacy policy markers replay their original behavior.
Native grants use separate policy markers and are pinned with the snapshot.

## Snapshot storage

`Catalog.Save(root)` saves every resolved agent under
`root/snapshots/<digest>.json`. `SaveSnapshot` requires an intact snapshot from
`Catalog.Snapshot` or `ReadSnapshot`. Writes use synchronized temporary files,
an atomic exclusive hard link, and a directory synchronization. Saving the
same snapshot again succeeds; corrupt existing snapshots are never overwritten.

The SHA-256 digest includes the agent settings, instructions, skill content,
schema, resolved profile, credential binding paths, and compiler policy version.
Selected MCP URLs and tool lists are included; unselected registry entries are
excluded. MCP snapshots carry an explicit compiler policy marker, and unsupported
markers cannot replay. Absent new fields use `omitempty` and preserve legacy
snapshot digests and the previous deny-all compiler policy.
The digest excludes credential file contents. JSON formatting and object-key ordering
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

The seven `pr-*` roles belong to the runtime's bundled `pr-review` workflow. Their
review/report schemas differ from the general-purpose notebook envelope. All
role instructions use `builtins/<name>/instructions.md`; shared schemas use
`schemas/*.json`. No prompt files need to be copied into a deployment to use them.
