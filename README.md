# Agent runtime

Filesystem-authored agents run through Hatchet, the existing sandbox controller,
and native OpenCode V2 sessions. A trusted Go handler reviews GitHub pull requests
without giving the model GitHub credentials.

## Validate the checked-in reviewer

Use Go 1.27.1, the version declared by `go.mod`.

```sh
go build -o bin/agent-runtime ./cmd/agent-runtime
export AGENT_DEFINITIONS_DIR="$PWD/examples/definitions"
bin/agent-runtime agents validate
bin/agent-runtime agents list
bin/agent-runtime agents inspect github-reviewer
go test ./...
```

These commands validate and compile the real packages. They do not call GitHub,
Hatchet, a model provider, or Kubernetes. `inspect` prints the resolved snapshot
including deployment binding paths, but never reads or prints credential values.

## Deploy and invoke

Read [the setup guide](docs/agent-automations.md) before starting a worker.
It covers Kubernetes pools, Hatchet, persistent storage, GitHub App permissions,
webhook ingress, and migration from `AGENT_DEFINITIONS_FILE`.

After deployment, replace the example identity with canonical values from your PR:

```sh
bin/agent-runtime run github-pr-review --input review.json
```

The CLI returns a Hatchet workflow run ID. The webhook invokes the same
`github-pr-review` workflow. Neither path executes an agent in the submitting process.

See [the authoring reference](definitions/README.md) for schemas and compilation.

See [GitHub management](docs/github-management.md) for App inspection, repository
discovery/enrollment, per-workflow PR selection, and eligibility explanations.

## Config-owned multi-agent workflows

Production roles and workflow policy belong in `jake-molnia/config`, not this repo.
Use `agentexec.Executor.Stage` to bind pinned authored packages to native execution,
and `hatchetbridge.RegisterMessageDAG` for sequential steps, parallel investigation,
and joins over typed immutable messages. See [config-owned composition](docs/config-owned-workflows.md)
for the source review/verifier/adversarial/writeup contracts, ownership boundaries,
and the investigation tools still required for production parity.
