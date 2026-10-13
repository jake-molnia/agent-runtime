# Sandbox harness configuration

`opencode.json` is the unrestricted OpenCode V2 configuration shipped by the image.
It grants every action on every resource globally and on the runtime's `authored`
agent. This includes shell, filesystem, execute, subagents, external-directory
access, and repeated-tool approvals. The portable shell permission scanner is
explicitly disabled, and no additional local resource-denial policies are loaded.

The image currently installs OpenCode **2.0.26**. Its native allow-all rules are
its equivalent of YOLO mode; it does not need a separate approval-bypass flag.
Provider/model safety behavior and remote MCP authorization are outside this file.

## How the file reaches a run

The Dockerfile copies this file to
`/etc/agent-runtime/harnesses/opencode.json` in both images. The sandbox sets
`OPENCODE_CONFIG` to that path so directly launching `opencode` uses it.
`OPENCODE_DISABLE_PROJECT_CONFIG=1` prevents a checkout's config from replacing
these defaults. The runtime also embeds the same source file into its binary,
then adds the pinned agent instructions, output schema, model/provider settings,
skills, and selected MCP connections for each session.

Each sandbox session gets its generated `opencode.json` in its temporary home.
The supervisor points that OpenCode process's `OPENCODE_CONFIG` at the generated
file. Provider credentials are written separately to OpenCode's auth store.
Nothing is baked into the image from worker credentials or deployment secrets.

Snapshots pin the harness configuration digest. Changing this file requires
rebuilding the worker/sandbox images and reloading definitions; old snapshots
cannot silently pick up different execution settings.

The sandbox runs as root, and native tool execution requires no approval prompts.
The worker remains non-root. OpenCode is the only installed model-execution
harness today; adding another harness requires its own tested configuration and
launch wiring here.

## Pinned native contract

- [OpenCode V2 configuration schema](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/schema/src/config.ts)
- [Permission evaluation](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/core/src/permission.ts)
- [Global rules apply to all agents](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/core/src/config/plugin/agent.ts)
- [Portable shell scanner setting](https://github.com/anomalyco/opencode/blob/9b4ec5714d481559990db0a816d5dec19541a814/packages/schema/src/config/experimental.ts)
