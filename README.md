# Agent runtime

A Go runtime for config-owned agents and workflows, using Hatchet, isolated
sandboxes, native OpenCode V2 sessions, and immutable typed message handoffs.
It ships generic `code-review`, `verify`, and `adversarial-review` agent defaults.
It does not ship or register production workflows.

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
    input: [review, adversarial]
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

Read [setup and ownership](docs/agent-automations.md),
[defaults and overrides](definitions/README.md), [workflow semantics](workflows/README.md),
and [sandbox desktop and tools](docs/desktop.md).
GitHub remains an optional adapter library; no GitHub identity, workflow or secret
configuration is required to use the runtime.
