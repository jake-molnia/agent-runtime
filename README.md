# Agent runtime

A Go runtime for config-owned agents and workflows, using Hatchet, isolated
sandboxes, native OpenCode V2 sessions, and immutable typed message handoffs.
It ships generic `code-review`, `verify`, and `adversarial-review` agent defaults.
It does not ship or register production workflows.

OpenCode V2 is the only supported agent harness. The sandbox installs the upstream
`@opencode/cli` package pinned to 2.0.26 and exposes its `opencode` executable on
`PATH`. `OPENCODE_BINARY` can select another path to an OpenCode V2 binary.

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
[defaults and overrides](definitions/README.md), and [workflow semantics](workflows/README.md).
GitHub review automation is an [optional trusted integration](docs/github-review.md);
no GitHub identity, workflow or secret configuration is required for generic workers.
The images include [pinned engineering skills](docs/sandbox-skills.md).

See [container releases](docs/releases.md) for versioned and nightly GHCR images,
and [config worker replacement](docs/config-runtime-replacement.md) for the current
integration and deployment gaps.

Sandbox harness defaults are checked in under [harnesses](harnesses/README.md)
and copied into the images. OpenCode runs with unrestricted tool permissions.
