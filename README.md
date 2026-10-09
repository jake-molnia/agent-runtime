# Agent runtime

A Go runtime for config-owned agents and workflows, using Hatchet, isolated
sandboxes, native OpenCode V2 sessions, and immutable typed message handoffs.
It ships 16 agent presets for engineering, research, monitoring, and daily briefs.
Workflows are opt-in configuration, including simple cron-triggered agents with
durable notebooks. Agents investigate using explicitly granted native or MCP tools.

OpenCode V2 is the only supported agent harness. The sandbox installs the upstream
`@opencode/cli` package pinned to 2.0.26 and exposes its `opencode` executable on
`PATH`. `OPENCODE_BINARY` can select another path to an OpenCode V2 binary.

## Start with a preset

Build the CLI and select a trusted configuration directory containing your
`deployment.yaml` with model and execution defaults:

```sh
make build
export AGENT_DEFINITIONS_DIR=/path/to/config
bin/agent-runtime workflows templates
bin/agent-runtime workflows init price-watcher monitor-deals
bin/agent-runtime agents validate
```

Edit the generated brief and grant the tools the agent needs. The same command
creates starters for `researcher`, `daily-brief`, `upstreamer`, and every other
builtin. Existing files are never overwritten. Schedules start commented out.

Configure Hatchet, sandbox pools, credentials, and shared state as described in
[setup](docs/agent-automations.md). Notebook workflows also require a stable
`AGENT_DEPLOYMENT_ID` and shared `AGENT_NOTEBOOK_DIR`, default `/state/notebooks`.
After starting the worker, run and read a result:

```sh
bin/agent-runtime run monitor-deals --wait --text
bin/agent-runtime runs inspect RUN_ID
bin/agent-runtime runs result RUN_ID --wait --text
bin/agent-runtime runs cancel RUN_ID
```

A recurring brief is one agent doing its own research:

```yaml
input:
  brief: Explore today's AI and developer-tool news. Explain the useful stories with sources.
schedule:
  cron: "0 8 * * *"
  timezone: Europe/London
notebook: true
steps:
  brief: {agent: daily-brief, input: input}
output: brief
```

The agent receives its task and previous notebook, then returns a Markdown report,
source links, and replacement notes. Hatchet schedules runs; the runtime saves
validated notes and prevents overlapping notebook runs. See the
[preset guide](docs/presets.md) for all 16 roles and tool configuration.

## Author a workflow

Place this in your configuration directory as `workflows/review-change.yaml`:

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
    input: [input, review, adversarial]
output: verify
```

Step names are yours. Scalar input references forward whole values unchanged;
lists forward arrays in the declared order. There are no content-specific field
mappings, expressions, templates, or custom Go worker requirements.

```sh
make build
export AGENT_DEFINITIONS_DIR="$PWD/examples/definitions"
bin/agent-runtime agents defaults
bin/agent-runtime agents validate
bin/agent-runtime workflows list
bin/agent-runtime workflows inspect review-change
```

After configuring and starting the worker:

```sh
bin/agent-runtime run review-change --input examples/input.json
```

The CLI enqueues the configured workflow and returns its Hatchet run ID. The result
task returns the selected output value and its immutable message reference.
`--wait` retrieves the result; `--text` prints a report or JSON string as text.

Read [setup and ownership](docs/agent-automations.md),
[defaults and overrides](definitions/README.md), and [workflow semantics](workflows/README.md).
GitHub remains an optional adapter library; no GitHub identity, workflow or secret
configuration is required to use the runtime.
