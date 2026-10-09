# Configure generic agents and workflows

The runtime owns loading, validation, scheduling, native execution, typed routing,
artifact handling and cleanup. Your config repo owns workflow files, model choices,
instructions, MCP grants/connections, deployment profiles and domain policy.

## Configure deployment defaults

Create `deployment.yaml` in the trusted configuration directory:

```yaml
version: 1
defaults:
  model: {provider: openai, id: gpt-5}
  execution: {profile: investigation, timeout_seconds: 900}
profiles:
  investigation:
    pool: agent-pool
    namespace: agents
    directory: /workspace
    secret_files:
      openai: /secrets/openai-key
```

These are example deployment choices, not runtime defaults. Choose your provider,
model, pool, namespace and credential binding. Built-in agents have generic
instructions and accept arbitrary JSON input without fixed result keys; their
default output schema accepts any valid JSON. They do not contain model choices,
GitHub behavior or publishing authority.

When `defaults` is configured, the catalog resolves `code-review`, `verify`, and
`adversarial-review`. You need no agent files to use them. Explicit standalone
agent packages remain supported without deployment defaults.

## Override an agent

A same-name package such as `agents/verify/agent.yaml` overrides the builtin:

```yaml
version: 1
mcp: [issue-comments]
```

Its optional `instructions.md` replaces the generic instructions. You can say
"Verify the supplied material, then publish confirmed issues as comments grouped
by section" there. No such publishing policy is baked into the builtin verifier.
Absent fields inherit defaults; provided model, execution, description, MCP,
capability and schema fields override them. Lists replace inherited lists.

A differently named package can use `extends: verify`. Only builtin names are
valid inheritance targets. Optional skills and output schemas use the existing
trusted-package file rules. Standalone packages still require instructions.

## Approve MCP tools

The deployment configuration selects remote MCP connections:

```yaml
version: 1
defaults:
  model: {provider: openai, id: gpt-5}
  execution: {profile: investigation, timeout_seconds: 900}
mcp_servers:
  issue-comments:
    url: https://approved-broker.internal/mcp
profiles:
  investigation:
    pool: agent-pool
    namespace: agents
    directory: /workspace
    mcp: [issue-comments]
    secret_files:
      openai: /secrets/openai-key
```

Agent configuration selects a reference, not a URL, command or secret. The profile
must grant it; unknown or ungranted references fail validation. Only selected
connections enter the native session configuration. URLs reject credentials,
query strings and fragments. HTTPS is required except literal-loopback HTTP for
local testing. Arbitrary local subprocess MCP servers are not supported.

New snapshots allow all native tools inside the disposable sandbox, including
shell, filesystem access, execute, and subagents. The sandbox image runs as root.
Config chooses MCP endpoints; every tool exposed by a selected connection is
available locally. Aperture remains responsible for remote authorization.
`tools` and `tool_policy` are accepted as legacy metadata, not enforcement.

With profile tags and `APERTURE_UPSTREAM` on the sandbox,
`http://127.0.0.1:8082/v1/mcp` reaches Aperture through that sandbox's ephemeral
identity. Builtin skills activate the packaged catalog. Project configuration
remains suppressed so repository files cannot silently replace pinned prompts
or deployment-selected endpoints. The agent can still execute arbitrary code
inside its sandbox. The trusted system instructions include the output schema.

The publishing broker must enforce repository/resource authorization, payload
validation, revision checks, and deduplication. Instructions cannot authorize an
operation. GitHub App credentials remain on the trusted broker/worker, not in model
prompts or environment variables. Secret ownership and rotation are external.

## Define named steps

Create `workflows/<name>.yaml`. The filename is the workflow's Hatchet name.
`version` may be omitted and defaults to 1. The supported shape is exactly:

```yaml
steps:
  review:
    agent: code-review
    input: input
  adversarial:
    agent: adversarial-review
    input: input
  verify:
    agent: verify
    input: [review, adversarial]
output: verify
```

`input` is the original JSON value, including a string, number, boolean, array,
object or null. Other references are named step outputs. A scalar reference
forwards the entire value. A list returns an array of entire values in order;
even a one-element list stays an array. Agents see one generic input value,
not fields named context/review/adversarial. Inputs and outputs are not promoted
to system instructions. Arbitrary agent identities and step names work.

References imply dependencies. Independent root steps can run in parallel.
Every step must contribute to the selected output. Cycles, unknown references,
duplicate/unknown YAML fields, invalid names, malformed refs and missing schemas
fail before registration. A workflow has at most 32 steps; values are bounded to
4 MiB. No selectors, interpolation, expression language or executable hooks are
loaded from YAML.

The result task returns `value`, the selected step's complete JSON result, along
with the pinned workflow digest and durable message reference. Text is represented
as JSON strings. Large/binary files require explicit attachment/read adapters;
the workflow does not invent arbitrary host-file access.

## Start the generic worker

Set these variables in your deployment:

| Variable | Purpose |
| --- | --- |
| `AGENT_DEFINITIONS_DIR` | Trusted source directory, default `/config` |
| `AGENT_SNAPSHOT_DIR` | Shared durable agent/workflow snapshots, default `/state/definitions` |
| `AGENT_MESSAGE_DIR` | Shared immutable messages, default `/state/messages` |
| `AGENT_ARTIFACT_DIR` | Native session exports, required for structured agents |
| `AGENT_SECRET_KEY_FILE` | Stable runtime key, default `/secrets/runtime-key`, at least 32 bytes |
| `AGENT_WORKER_NAME` | Hatchet worker name, default `agent-runtime` |
| `AGENT_WORKER_SLOTS` | Normal and durable slots, default 4 |

Also configure the pinned Hatchet SDK's normal client authentication/addresses,
Kubernetes access to the named pools/namespaces, provider credential files, and
deployment network policy. Tailnet deployments retain their existing
`AGENT_DEPLOYMENT_ID` ownership requirement; see `tailnet/README.md`.

```sh
agent-runtime agents validate
agent-runtime workflows validate
agent-runtime worker
agent-runtime run review-change --input input.json
```

The worker registers configured workflow files and any explicitly enabled
[GitHub review integration](github-review.md). It fails if no workflows exist.
Generic workflows have no GitHub App/private-key/webhook/database prerequisite
and need no custom Go worker. Adapters can normalize events and submit the
same configured workflow, or expose authorized MCP operations.

`agents defaults|list|inspect` and `workflows list|inspect` provide diagnostics.
Inspection prints public deployment binding paths, not credential values. Never
load this directory from a PR checkout. Symlinks and unsafe package paths are
rejected; materialize Kubernetes projected-volume contents as trusted regular
files where required.

## Pinning and lifecycle

Startup saves complete immutable workflow plans: manifest, scalar/list input shape,
ordered references, selected output, resolved agents, instructions, schemas,
profiles and public MCP bindings. CLI submission pins a digest. Resolve and each
task reload that artifact, not mutable source files. Old prompts, models and
grants do not silently change when a new catalog is deployed.

Scheduler action names include snapshot revision identity. Keep the shared snapshots
and message directory available to all eligible workers.
Do not remove referenced snapshots. Maintain compatible workers while changing
step names or topology; a worker without an old task handler cannot execute it.
Changing current authoring is not retroactive revocation of old pinned grants.
Credential rotation remains late-bound for new provisioning; active native
sessions keep their bootstrap credentials.

Each step is a durable Hatchet task and has its own sandbox/session identity.
It uses the existing native lifecycle, exports before validation/cleanup, retains
failed exports until lease expiry, and reports cleanup failures. Native tool execution requires no permission prompts. Completed message outputs are immutable and reusable on replay.
No automatic model-execution retries are enabled; explicit replay can recompute
after a crash between successful execution and message persistence. Exactly-once
model execution is not claimed. Private input/output belongs in secured Hatchet,
message and artifact storage.

## Migration and evidence

`AGENT_DEFINITIONS_FILE` and `automations/` are retired with explicit diagnostics.
Move portable agent behavior into overrides and deployment settings into
`deployment.yaml`. Move graphs into `workflows/`. GitHub stays in the adapter
library or an authorized broker; no hardcoded GitHub trigger, writer, candidate
schema or publication workflow is part of generic execution.

This runtime pins Go 1.27.1, Hatchet v0.109.10, sandboxd v1.0.3 and OpenCode 2.0.26.
Tests cover the exact versionless workflow, heterogeneous whole-value inputs,
independent sandbox identities, replay, old-definition recovery, schema failures,
scoped reply identity, inheritance and MCP approval. Native opt-in tests use local
fake providers/brokers, not a paid model or production integration.

```sh
go test ./...
go test -race ./definitions ./workflows ./hatchetbridge ./agentexec ./command
go vet ./...
make build
AGENT_RUNTIME_TEST_OPENCODE_BINARY=/absolute/path/to/opencode \
  go test ./command -run '^TestOpenCodeNative' -count=1 -v
```

These checks do not deploy your config repo, Hatchet server, Kubernetes pools, MCP
brokers, TLS/network identity or secret retrieval. Validate those separately in
staging with your actual credentials. See `definitions/README.md` for pinned native
MCP source evidence and `workflows/README.md` for storage/identity details.
