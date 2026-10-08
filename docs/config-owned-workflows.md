# Compose config-owned agents

Production agent instructions, models, schemas, deployment profiles, workflow
composition, and publication policy belong in `jake-molnia/config`. This repository
provides the execution and transport libraries. Its checked-in agent examples are
contract fixtures, not the production review configuration.

## Source contracts

Inspected config commit `a1cc652afa6e995a2e714277b361e371a83137c0`, specifically
`packages/agent-tasks/agent_tasks/workflows/pr_review/agent.py`, `workflow.py`,
`models.py`, and `prompts/`. Those files define four roles:

- `pr-review` produces a private candidate review from pinned repository context.
- `pr-review-verify` independently investigates the change and the candidate review.
- `pr-review-adversarial` independently investigates the same change without receiving
  the candidate findings.
- `pr-review-writeup` receives the original review, verification, adversarial report,
  and ordered eligible findings, then returns public wording by source index.

The existing graph is:

```text
request -> review -> verify -----+
                  -> adversarial +-> writeup -> trusted publication
```

The dependency on review is not permission to feed its conclusions to every child.
The verifier consumes candidates. The adversarial branch only consumes the pinned
context. The writer's eligible list is verification findings followed by adversarial
findings. Config's deterministic reduction preserves source locations, chooses
severity, validates exact source-index coverage, and sorts the public findings.
Those domain rules do not belong in a generic runtime workflow builder.

No files in config were changed or executed while establishing these contracts.
The older Python implementation gives checkout commands a GitHub token. Do not copy
that credential boundary into new adapters. Keep GitHub credentials on the worker
and broker the repository/context reads or prepare checkout without retained tokens.

## Define workflows in trusted Go

Use `hatchetbridge.RegisterMessageDAG` in a worker built by the config repo. This
extends the existing typed message foundation instead of introducing another YAML
workflow language, selector expressions, prompt templates, or arbitrary script hooks.
Portable agent packages can still use YAML and Markdown; Go owns trusted integration.

```go
workflow, err := hatchetbridge.RegisterMessageDAG(client, service,
    hatchetbridge.MessageDAG{
        Name: "pr-review",
        Revision: pipelineRevision,
        Timeout: investigationTimeout,
        Output: "writeup",
        Nodes: []hatchetbridge.MessageNode{
            {ID: "review", Stage: reviewer, BuildInput: reviewInput},
            {ID: "verify", Stage: verifier, Parents: []string{"review"},
                BuildInput: verificationInput},
            {ID: "adversarial", Stage: adversarial, Parents: []string{"review"},
                BuildInput: independentInput},
            {ID: "writeup", Stage: writer, Parents: []string{"review", "verify", "adversarial"},
                BuildInput: writeupInput},
        },
    },
)
```

The actor values, budgets, revision, and input-builder functions are config-owned
values. This is a registration pattern, not a checked-in production worker.
Declare only real parent dependencies. A sequential chain is the same API with each
node depending on the previous node. A join declares several parents. Every node
must contribute to the selected output. Unknown actors, cycles, duplicate or missing
dependencies, reserved IDs, and unreachable nodes fail before registration.

`BuildInput` receives `MessageNodeInput` containing the original hydrated request and
only that node's declared parent deliveries. Its trusted code returns named message
parts. `MessageData(delivery, name)` extracts a selected JSON part. An input builder
can project a report's `findings`, flatten eligible findings, or deliberately omit
a parent report. It cannot make model-authored text become system instructions.

The writer explicitly lists review, verification and adversarial parents because it
consumes all three reports. No input adapter relies on transitive task outputs.

The runtime creates a fresh message addressed to each destination, validates it
against the destination stage's input contracts, and runs the pinned actor. Parallel
consumers do not share a mutable sandbox or rewrite the producer's message. Hatchet
owns the graph and waits; there is no nested `agent-run` workflow or polling runner.
Each node is a durable task with zero automatic execution retries. Message outputs
are cached immutably under scoped invocation identity; explicit replay can reuse a
completed result. If native execution succeeds but message persistence fails, compute
can repeat. This is not an exactly-once model-execution claim.

## Bind authored packages to native stages

`agentexec.Executor` runs a snapshot through the existing engine. `Stage` supplies
the message actor, schema validator, output contract, and real execution function:

```go
executor, err := agentexec.New(engine)
stage, outputValidator, err := executor.Stage(snapshot, inputContracts, "report")
validators[snapshot.Agent.Digest] = outputValidator
stages = append(stages, stage)
```

Load `snapshot` from the trusted catalog or `definitions.ReadSnapshot`, not a PR
checkout. The actor revision and output schema ID are the resolved definition digest.
Contracts and snapshots are copied so later mutations cannot switch the behavior.
Register the returned stage and validator through `messages.New`. Config supplies
input schema validators and selects the contracts needed for each role.

The executor derives sandbox/session identity from `Delivery.ExecutionID`. Each
invocation has an independent key and sandbox. It provisions, executes, exports,
reads and validates output, then cleans up. Failed execution exports before cleanup;
failed export retains the sandbox until its lease expires. Cleanup uses a bounded
detached context and failures prevent a successful result. Backend diagnostics are
sanitized, and no runtime credentials are put into message envelopes.

The DAG installs an `InteractionWait` callback for the native executor, using the
existing Hatchet durable interaction wait. Missing permissions are not automatically
approved. Outside a DAG, a trusted caller can use `WithInteractionWait` or allow the
engine to return its normal interaction-required error.

## Configuration and rollout ownership

Config owns the custom worker, registered stage catalog, input adapters, approved
preparation/tool brokers, and placement. The runtime's built-in `worker` command
still registers its supported single-agent/GitHub automation catalog. It does not
discover or dynamically execute Go hooks from a directory. A custom config worker
registers the DAG with `client.NewWorker(..., hatchet.WithWorkflows(workflow))`.

`Revision` must change when graph shape or trusted input/reduction code changes.
Resolve persists the revision and every actor digest. A mismatched worker fails
closed instead of silently using its newer actors or input code. Keep compatible
workers available until old runs drain; use the SDK's desired worker labels on run
submission and worker labels for version/placement affinity. Persist snapshots and
the message directory where all eligible workers can access them. The directory
store's filesystem locking requirements still apply.

Use the generic submit command for a custom worker's registered workflow:

```sh
agent-runtime submit pr-review --input message-input.json
```

It sends one bounded JSON object through Hatchet and returns the run ID. It neither
loads local definitions nor invents a workflow registry. Destination-side validation
and handler authorization still apply. `run` remains the convenience command for
the built-in catalog automations.

## Investigation capabilities are not prompt text

The source reviewer/verifier/adversarial roles need a pinned checkout, unchanged/base
file reads and search, disposable test execution, and version-matched documentation
access. The default authored package policy currently denies tools and supplies
only trusted diff context. It can perform diff-only critique and evidence-only
writing, but it cannot reproduce the source's full independent investigations.
Do not describe a diff-only verifier as having run tests or reviewed unseen files.

`agentexec.Backend` is the trusted integration seam. A config-owned worker can wrap
the existing engine with reviewed preparation and tool-broker adapters; inputs may
select only approved repository/revision context, never credentials, arbitrary
host paths, model configuration, or deployment commands. Full investigation requires
those enforced adapters and deployment isolation. They are not enabled by this DAG
library, a capability string, or a prompt. Do not relax deny-all permissions merely
to make an original prompt appear executable.

Actual production package migration, role model selection, test/read/documentation
brokers, GitHub publication reduction, and deployment changes remain in config.
This runtime change builds the common execution and fan-out/join tools; it does not
claim to have deployed or migrated the private production agents.

## Verification

```sh
go test -race ./agentexec ./messages ./hatchetbridge ./command
go test ./...
go vet ./...
make build
```

Tests execute fake native backends through the real immutable message store and
the pinned SDK's public declaration/callback APIs. They check candidate delivery,
independent context-only branches, joins, scoped routing, immutable revisions,
malformed data, artifact retention, cancellation, cleanup and durable waits.
They do not claim live Hatchet, GitHub, production sandbox or paid-model validation.
