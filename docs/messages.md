# Agent-to-agent messages

Agents exchange selected task data through typed messages. They do not share a
mutable workspace or forward their whole conversation history. A message can
contain text, structured JSON, and attachments. `messages.Service` owns routing,
validation, persistence, and reply correlation. Executors own model invocation.

This is an internal transport inspired by message-first agent protocols. It is
not an A2A server or an implementation of the A2A wire specification.

## Run the handoff example

```sh
store=$(mktemp -d)
go run ./examples/message-handoff --store "$store"
go run ./examples/message-handoff --store "$store"
```

The example uses deterministic executors, not live models. On the first run it
prints `producer executed` and `verifier executed` to stderr and a verdict
message to stdout. The verifier receives the producer's structured candidate
directly. Its verdict includes the candidate reference, and its message has an
`in_reply_to` reference to that same candidate. On the second run the messages
are recovered without invoking either executor again.

## Define the contract

Register a `messages.Stage` for each resolved agent revision. A stage declares
named input and output parts and an executor. Each data contract names a
versioned schema whose validator must be registered at service construction.
Unknown schemas, unknown part names, incompatible kinds, missing required
parts, and invalid values fail closed.

```go
candidateContract := messages.Contract{
    Name: "candidate", Kind: messages.Data,
    Schema: "candidate/v1", Required: true,
}

producer := messages.Stage{
    Actor: messages.Actor{Agent: "reviewer", Revision: definitionDigest},
    Inputs: []messages.Contract{requestContract},
    Outputs: []messages.Contract{candidateContract},
    Execute: reviewerExecutor,
}

verifier := messages.Stage{
    Actor: messages.Actor{Agent: "verifier", Revision: verifierDigest},
    Inputs: []messages.Contract{candidateContract},
    Outputs: []messages.Contract{verdictContract},
    Execute: verifierExecutor,
}
```

Use `messages.Typed[T](check)` for strict typed JSON validation plus a semantic
check. It rejects unknown fields, duplicate keys, trailing JSON, excessive
nesting, and null results. The check must enforce domain requirements such as
required fields, valid findings, or allowed decision values. Schema names alone
are not validation; this package does not interpret JSON Schema documents.

Derive `Actor.Revision` from the resolved definition and executor release,
including model settings, instructions, schema/validator versions, and relevant
execution policy. Do not reuse a revision
after changing its behavior. The registry copies contract slices and schema
maps at construction; registered executor and validator closures must themselves
remain immutable and safe for concurrent calls.

## Invoke and hand off

Publish an initial message addressed to the producer. Then invoke stages using
the returned references:

```go
candidate, err := service.Invoke(ctx, trustedScope, messages.Invocation{
    Stage: producer.Actor, Input: initialReference,
    TaskID: "produce", To: verifier.Actor.Agent,
})
if err != nil {
    return err
}

verdict, err := service.Invoke(ctx, trustedScope, messages.Invocation{
    Stage: verifier.Actor, Input: candidate,
    TaskID: "verify", To: "publisher",
})
```

The executor receives a `messages.Delivery` with:

- `Message`: the selected input message, with spilled text and JSON restored.
- `Reference`: the digest-pinned reference to the stored input envelope.
- `ExecutionID`: a stable, scope-aware key suitable for the sandbox run key.
- `Prompt()`: a bounded JSON representation for text-based model interfaces.
- `Attachment(ctx, name)`: verified binary bytes for a declared file part.

Executors return only `[]messages.Part`. The service authors the output's
identity, sender revision, recipient, context, task, and `in_reply_to` fields.
Models cannot choose those headers through the returned parts.

`Delivery.Prompt()` includes both the expanded message and its stored reference.
It belongs in the ordinary task prompt, never the system instructions. Trusted
agent instructions remain separate. Peer content is untrusted, and the prompt's
reminder is not a security mechanism against prompt injection.

An OpenCode executor can pass this prompt into the existing
`orchestration.Request.Prompt`, using `Delivery.ExecutionID` as `Request.Key`.
The executor must run the existing sandbox lifecycle, extract the intended final
output, and return typed parts. This PR does not replace session-output extraction
or automatically configure model executors in the default worker.

Use `service.Receive(ctx, scope, ref, recipient)` when trusted application code
consumes a result without invoking another agent. It verifies the recipient and
restores text and JSON. `Read` returns the stored envelope for inspection instead.

## Register with Hatchet

```go
workflow, err := hatchetbridge.RegisterMessageChain(client, service,
    hatchetbridge.MessageChain{
        Name: "review-verify",
        Producer: producer.Actor,
        Verifier: verifier.Actor,
        Timeout: 15 * time.Minute,
    },
    hatchet.WithWorkflowEvents("review:requested"),
)
```

Register the returned workflow on the worker like other Hatchet workflows. The
graph is `resolve -> produce -> verify`. Input is `MessageChainInput{Parts: ...}`.
The resolve task commits the initial message. Later task outputs carry only
references and pinned actor revisions, not agent payloads. The trusted storage
scope and conversation context are the Hatchet workflow run ID.

The graph validates producer/verifier contract compatibility at registration.
Persisted state pins the actors for the run. A redeployed worker that lacks a
pinned revision fails instead of silently substituting a new agent. Either keep
old revisions available until runs finish or deliberately terminate those runs.

Executors run in ordinary Hatchet tasks. This helper does not implement durable
human-interaction waits, native sandbox resource cleanup, event ingestion, or
publishing. Those remain responsibilities of the application's execution
adapter and domain workflow. Do not interpret successful verification execution
as approval: the publisher must validate its decision and exact candidate
reference before allowing any external write.

## Storage, limits, and retry semantics

`messages.Directory` stores immutable messages and content-addressed attachments
within a trusted scope. An ID cannot be overwritten with different content.
References include SHA-256 digests, which are checked on reads. Attachments are
scoped even when their content digests match across runs.

By default, text or JSON larger than 32 KiB spills into attachment storage. The
recipient sees restored text or JSON through the same interface. Files stay
explicit attachments. `Prompt()` refuses file parts rather than silently hiding
them or treating binary content as text; use a provider-native file/tool adapter.
Limits are 32 parts, a 2 MiB stored envelope and rendered prompt, and 16 MiB per
attachment and total expanded delivery. A reference does not remove the model's
context limit.

The directory backend uses synced temporary files, exclusive hard-link
publication, directory syncs, and advisory file locks. Workers sharing a store
must use the same durable POSIX filesystem with working cross-process locks,
hard links, and directory fsync. Provision the store root outside sandboxes and
restrict it to trusted workers. An ephemeral pod-local directory is not shared
durable storage. Object storage and a distributed execution-claim backend are
not implemented; alternatives implement the `messages.Store` contract.

`Store.Once` serializes invocations with the same scope, pinned actor, input
reference, task ID, and recipient. Successful committed outputs survive process
restarts and are reused. A crash or failure before commit can rerun the executor.
This is not exactly-once model execution or exactly-once external side effects.
Use the stable execution key to recover native sessions, and keep external
publishing outside agent executors with its own reconciliation policy.

Scopes and routing are isolation checks, not an authentication system. Only
trusted worker code should choose a scope, publish sender identities, register
executors, or expose storage access. Do not accept arbitrary scopes or storage
paths from webhooks or model tool arguments. Digests are not authorization.
The store must not contain secrets; no secrets or signed URLs should be put in
message parts. Payload redaction and access policy belong at the application
boundary. No message bodies are logged by this package.

Retention and garbage collection are intentionally separate. Attachments
written before a failed message commit can be orphaned. Keep scopes until all
consumers finish, then expire the entire scope according to application policy.

## Verify locally

```sh
go test ./messages ./hatchetbridge
go test -race ./messages ./hatchetbridge
go test ./...
go vet ./...
```

Tests cover typed producer/verifier delivery, reply references, restart recovery,
scope isolation, immutable conflicts, digest corruption, cancellation, concurrent
execution claims, spill/hydration, invalid output, contract compatibility, and
Hatchet task-handler handoffs. They do not provision a live Hatchet server,
OpenCode sandbox, or GitHub integration.
