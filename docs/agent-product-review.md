# Agent presets, workflow packs, and product readiness

Implementation status: the catalog, workflow starters, cron registration, native tool grants, output contracts, notebook persistence, and result CLI are now implemented. See [the current preset guide](presets.md) for supported configuration and deployment requirements. The discussion below records the design and research that preceded implementation.

Assessment date: 2026-10-09. This is a product recommendation based on repository inspection and primary-source research, not an implementation or a production-readiness certification. The initial audience assumed here is developer and platform teams operating a self-hosted service. The companion [research notes](agent-product-research-sources.md) contain the external source comparison.

The user's subsequent selections broaden the product to recurring research, everyday monitoring, news briefs, and hands-off repository maintenance. They explicitly prefer agents exploring with general tools and simple cron triggers over custom collectors, domain APIs, or fixed news/monitoring pipelines. The [selected catalog plan](agent-catalog-plan.md) supersedes this document's initial preset shortlist, delivery ordering, and integration-heavy recommendations for everyday jobs. The code-level usability findings below still apply.

## Recommendation

Ship a small catalog of complete task packages and explicitly installed workflow packs. Start with supplied-input tasks so customers can obtain useful results before connecting repositories, CI, or ticket systems. Add one supported repository integration next. Keep the existing separation between the runtime and deployment-owned workflows.

The runtime already has valuable foundations: isolated sessions, exact tool grants, immutable workflow snapshots, validated message handoffs, artifact export, durable task coordination, and lifecycle telemetry. The product gap is the work a customer must do between choosing a task and receiving an understandable result. More prompt names alone will not close that gap.

Five initial additions: `implementation-planner`, `test-designer`, `ci-failure-analyst`, `findings-triager`, and `release-notes-writer`. Combine these with the existing reviewer roles into three first-release packs: change assessment, feature planning, and CI diagnosis. A release brief can follow using the same packaging.

The research shortlist also favored standalone specification review and issue triage. This consolidated ordering favors an implementation planner and findings triager because they fill missing stages in the first engineering workflows. Specification clarification belongs in the planner's initial contract; a dedicated issue-triage package can follow when an issue integration is supported.

These priorities are product judgment. The external documentation establishes useful patterns and comparable capabilities, not measured demand for these exact presets. Validate the ordering with pilot users.

## What a preset should include

A supported preset should have instructions, an explicit result schema, example input and output, required evidence, optional connectors, supported model configurations, limitations, and evaluation cases. Its documentation must say whether it only drafts a result or can perform an external action.

Separate three levels of capability:

1. **Supplied input.** Operates on JSON or text supplied by the caller. Still needs a configured model and runtime deployment.
2. **Connected reads.** Fetches fresh evidence through a supported remote MCP broker or trusted adapter.
3. **Actions.** Changes files, executes tests, posts comments, or modifies external systems. Requires a supported execution or publishing integration, policy enforcement, and repeat-safe operations.

An agent name does not grant the capability. The compiler starts with deny-all and adds exact selected MCP actions. Native filesystem and shell tools are not enabled by these definitions. See [compiler](../definitions/compile.go) and [MCP reference](../definitions/README.md#remote-mcp-grants).

## Candidate catalog

All names below are proposed additions, not current builtins. A supplied-input variant can be authored with the current agent format. Shipping it still requires writing and evaluating the package.

| Proposed agent | Useful result | Works on supplied input | Integration needed for a fuller product |
| --- | --- | --- | --- |
| `implementation-planner` | Ordered change plan, affected components, dependencies, open questions | Requirements and architecture excerpts | Repository search to ground affected files |
| `test-designer` | Test matrix linked to requirements, edge cases, proposed test cases | Requirements, code excerpts, existing test descriptions | Repository reads; execution service to prove tests pass |
| `ci-failure-analyst` | Failure category, cited log evidence, ranked hypotheses, next checks | CI logs and relevant diff | CI log retrieval and optional controlled rerun |
| `findings-triager` | Deduplicated findings with provenance, confidence, disposition, unresolved disagreements | Reviewer outputs plus original evidence | Optional repository reads for disputed findings |
| `release-notes-writer` | User-facing release draft with source references and breaking changes | Commits, PR summaries, release metadata | GitHub reads; publishing is a separate action |
| `requirements-analyst` | Acceptance criteria, ambiguity list, missing constraints | Feature request or specification | Optional issue tracker reads |
| `docs-reviewer` | Documentation/code inconsistencies and suggested corrections | Documentation plus code/API excerpts | Repository/documentation search |
| `dependency-upgrade-analyst` | Compatibility risks, migration steps, required checks | Manifest, lockfile changes, supplied upstream release notes | Registry and official-document retrieval; execution for validation |
| `migration-planner` | Staged migration and rollback plan with preconditions | Current/target design and constraints | Repository/schema introspection; no automatic migration execution |
| `security-reviewer` | Evidence-backed findings within a declared threat scope | Diff, code excerpts, deployment assumptions | Repository reads and specialist scanner integrations; not a security certification |
| `performance-analyst` | Bottleneck hypotheses and a measurement plan | Profiles, traces, timings, code excerpts | Profiling/benchmark services to establish a regression |
| `incident-analyst` | Timeline, evidence-linked hypotheses, next investigation steps | Logs, alerts, changes, incident notes | Observability reads; remediation requires a separate action path |
| `issue-triager` | Suggested category, priority rationale, missing information, duplicate candidates | Issue plus supplied candidate issues | Issue search; labels/comments require a write integration |
| `researcher` | Sourced brief, conflicting evidence, unanswered questions | Supplied source documents | Search and fetch integration for fresh research |
| `change-implementer` | Patch plus validation report | Can draft a patch from enough supplied context | Useful autonomous operation needs controlled checkout, file edits, test execution, diff collection, and PR integration |

Avoid launching `fixer`, `test-runner`, or `publisher` as if instructions provide their effects. Start with the diagnostic or drafting counterpart and make action support explicit.

## Workflow packs

The following are proposed packages. Their fixed agent graphs fit the current YAML model once the referenced agent packages exist. External triggers, data gathering, deterministic output formatting, and publication are separate integration work.

| Pack | Agent graph | Result and boundary |
| --- | --- | --- |
| Change assessment, first release | `code-review` and `adversarial-review` in parallel, then `findings-triager`, then `verify` | Ranked, deduplicated, verified findings. Return the report first; GitHub commenting is optional later. Offer a single-review fast mode and benchmark whether extra stages improve quality. |
| Feature planning, first release | `implementation-planner` → `test-designer` → `adversarial-review` → `verify` | Implementation plan and acceptance/test matrix. Every downstream step gets the original requirements and relevant earlier outputs. |
| CI diagnosis, first release | `ci-failure-analyst` → `verify` | Evidence-linked diagnosis and next checks. Does not claim a fix or successful rerun. |
| Release brief, next | `release-notes-writer` → `verify` | Release draft with traceability to supplied changes. Publication stays separate. |
| Dependency upgrade assessment, next | `dependency-upgrade-analyst` → `test-designer` → `verify` | Migration checklist and test plan. Applying the upgrade requires execution tools. |
| Incident investigation, connected phase | `incident-analyst` → `adversarial-review` → `verify` | Investigation report grounded in an explicit time window. Live evidence needs observability connectors. |

Use whole-value references deliberately. For example, a verifier can receive `input: [input, plan, tests, challenge]`. The current format produces a positional array; it does not name the array elements or automatically include ancestral context. Package instructions must explain each position. See [ResolveInput](../workflows/workflow.go) and the [existing example](../examples/definitions/workflows/review-change.yaml).

Do not make models perform deterministic joining, formatting, schema conversion, or deduplication by exact identity. Use ordinary code in a trusted adapter for those jobs. An agent earns its place when consolidation needs judgment, such as reconciling contradictory findings.

Ship workflow packs as opt-in configuration. A future installer can materialize versioned files into the customer's configuration directory without registering unsolicited production jobs. Retain snapshots for exact run identity, and add human-readable pack versions for discovery and upgrades. Current YAML `version: 1` describes the file format, not a package release.

## Product gaps, in priority order

### 1. The first successful run requires infrastructure expertise

The worker needs Hatchet configuration, Kubernetes sandbox pools, provider credential files, a runtime key, and shared state/artifact storage. The repository includes image build stages and source build commands, but the documented onboarding path assumes those services exist. The entire source tree rejects symlinks, so ordinary Kubernetes projected-volume layouts need materialization. See [worker setup](agent-automations.md#start-the-generic-worker), [worker](../command/worker.go), [Dockerfile](../Dockerfile), and [file loading](../definitions/catalog.go).

**Priority: before a self-service release.** Provide a supported deployment recipe, a sample pack, and a preflight command that checks all prerequisites together. Clearly separate an offline configuration preview, an infrastructure smoke test, and a paid model run. Do not call a fake-provider demo a successful model evaluation.

A local execution backend is a substantial scope change. It is a first priority only if individual developers are the target. For platform teams, a documented reference deployment and a working result command are the nearer path.

### 2. The CLI stops before the user obtains a result

The command router offers `serve`, `worker`, `version`, `agents`, `workflows`, `run`, and `submit`. Configured `run` returns a run ID. There are no runtime-owned commands for listing runs, waiting, reading the final report, retrieving artifacts, or cancelling a run. Hatchet has a UI and run-state APIs, and the runtime has cancellation primitives; the gap is their integration into this product. See [router](../command/command.go), [run command](../command/catalog.go), [engine](../orchestration/engine.go), and [Hatchet architecture](https://docs.hatchet.run/v1/architecture-and-guarantees).

**Priority: before a self-service release.** Add an end-to-end submit/wait/result path, step progress, artifact links, cancellation, and sanitized error details. These are proposed commands and flows, not existing features. Reuse Hatchet rather than building a second scheduler or run database.

### 3. Output schemas validate results but do not guide generation

`definitions/compile.go` builds system instructions from Markdown and skills. It does not add `Agent.Schema`. The prompt submission carries text; the task-data envelope carries message and reference data, not the expected output contract. Only after execution does `agentexec` call `ValidateOutput`, reducing failures to `agent output schema validation failed`. See [compiler](../definitions/compile.go), [prompt construction](../messages/service.go), [prompt submission](../orchestration/engine.go), and [post-run validation](../agentexec/executor.go).

This makes schema-only customization misleading. Until fixed, package authors must repeat the expected shape in instructions. Builtins use `{}`, so they provide no stable domain result contract.

**Priority: before publishing a larger preset catalog.** Supply the schema to generation through a verified supported structured-output mechanism, or a generated explicit output instruction if the pinned harness requires that. Preserve schema validation at the boundary. Report safe field paths and expected types on failure. Evaluate a bounded formatting repair only for suitable tasks; never retry an external side effect just to repair JSON.

### 4. Connecting tools is mostly the customer's integration project

An MCP server definition has only `url` and exact `tools`. The profile and agent both select connections. Authentication is delegated to deployment networking or mTLS; local subprocess servers, bearer headers, and interactive OAuth are not supported by this format. `githubreview` has useful webhook and publication building blocks but is not wired into the generic worker. See [MCP type](../definitions/mcp.go), [tool setup](agent-automations.md#approve-mcp-tools), [GitHub webhook](../githubreview/webhook.go), and [worker](../command/worker.go).

**Priority: before advertising connected workflows as turnkey.** Ship one supported GitHub/CI broker or adapter, documented tool contracts, connection health checks, example permissions, and a trusted secret binding story. Connector requirements should be visible before a user selects a pack. Preserve exact tool grants.

### 5. Human approvals exist as backend mechanics, not a complete experience

The engine detects pending permissions/forms. Hatchet waits for scoped interaction events, and `NotifyInteraction` can wake the owner after an authorized reply. `Engine.Proxy` exposes native endpoints when an embedding application provides an authenticated run resolver. The shipped command router and worker do not provide an approval inbox or generic reply flow. Also, the timeout wraps execution, so waiting for a person consumes the agent's execution deadline. See [engine status loop](../orchestration/engine.go), [durable wait](../hatchetbridge/wait.go), [interaction notification](../hatchetbridge/bridge.go), [proxy](../orchestration/proxy.go), and [executor timeout](../agentexec/executor.go).

**Priority: before action-capable packs.** Show the proposed effect, target resource, evidence, expiry, and approve/reject actions. Persist the decision and make resume repeat-safe. Model confidence or a verifier's JSON `approved` value must not substitute for an enforced human or deployment policy gate. Define separate active-execution and approval-wait limits.

### 6. The YAML graph is useful but leaves common authoring work to prompts

Each step has only `agent` and `input`; references are scalars or positional arrays. There are no named bindings, input schemas, transforms, conditional gates, schedule fields, or bounded repeat constructs in the configured format. The message layer supports richer typed parts and attachments, but `Delivery.Prompt` rejects file parts without an adapter. See [workflow types](../workflows/workflow.go), [configured input validation](../hatchetbridge/configured.go), [message types](../messages/message.go), and [prompt construction](../messages/service.go).

**Priority: improve after the initial packs expose real friction.** Start with clear input contracts, named whole-value bindings, deterministic utility steps, and attachment read adapters. Add gates where publication requires them. Keep fixed graphs for the first packs; do not introduce an unrestricted expression language or dynamic manager just to make examples more elaborate.

Expose schedules and webhook submission through the product's configuration or integration layer when needed. Hatchet already supplies scheduling, rate limiting, and durable coordination. Its current documentation is architectural evidence; verify support in this repository's pinned Go SDK before implementing new bindings. See [Hatchet architecture](https://docs.hatchet.run/v1/architecture-and-guarantees) and [rate limits](https://docs.hatchet.run/v1/rate-limits).

### 7. Operational telemetry does not establish agent quality or spend

The runtime exports phase/startup timings and run/event counters. Its exporter intentionally removes payloads and sanitizes errors. That is useful infrastructure observability, but it is not a result-quality evaluation or a per-run model usage ledger. Agent configuration has a timeout, not a token, cost, turn, or tool-call budget. See [telemetry](../telemetry/telemetry.go), [export sanitization](../telemetry/exporter.go), and [definition types](../definitions/catalog.go).

**Priority: quality checks with the first packs; usage limits before broader rollout.** Add versioned evaluation fixtures with expected findings, clean negative cases, unsupported claims, malformed inputs, and prompt-injection attempts. Measure evidence accuracy, false positives, abstention, schema adherence, completion rate, latency, and model usage. Compare multi-agent packs with a single-agent baseline. Record observed usage separately from estimated monetary cost. Do not claim a hard spending cap unless the runtime can enforce it against in-flight calls.

Existing unit/native smoke tests are valuable, but the documented fake-provider tests do not measure live-model task quality. See [verification notes](agent-automations.md#migration-and-evidence).

### 8. Replay needs a product policy for external effects

Immutable messages make completed outputs reusable, but execution is explicitly not exactly once: a session may run again if it finishes before its output is persisted. Configured model tasks disable retries. The GitHub adapter has domain-specific publication controls; arbitrary MCP brokers do not automatically inherit them. See [executor contract](../agentexec/executor.go), [configured registration](../hatchetbridge/configured.go), and [GitHub review adapter](../githubreview/review.go).

**Priority: before auto-publication or remediation.** Classify transient infrastructure failures separately from schema, policy, and domain failures. Add explicit retry policies for safe operations. Give external actions stable operation IDs, idempotency handling, stale-revision checks, and an inspectable receipt. Put these guarantees in deterministic broker/adapter code, not agent instructions.

### 9. Configuration identity is stronger than configuration lifecycle UX

Snapshots pin the complete resolved agent and workflow, which is a strength. Operators must still retain old snapshots and compatible worker handlers. Editing source does not revoke grants in old snapshots, and there is no pack installation/update command in the router. See [pinning and lifecycle](agent-automations.md#pinning-and-lifecycle), [snapshots](../definitions/snapshot.go), and [router](../command/command.go).

**Priority: before sustained production operation.** Add pack versions, compatibility metadata, inspectable upgrade diffs, rollback instructions, snapshot retention rules, and a distinct revocation mechanism at the authorization boundary. Snapshot hashes answer what ran; product versions should explain what changed.

## Delivery sequence and acceptance criteria

These are proposed release gates, not measured current performance or time estimates.

1. **Complete one run.** Fix output-contract delivery and safe schema diagnostics. Add reference deployment/preflight, submit/wait/result, and a single packaged workflow. On the supported environment, a new user should obtain and inspect a real result without reading Go source or finding files in shared storage.
2. **Ship five evaluated presets and three packs.** Include examples, explicit input/output contracts, and a documented model compatibility set. Each pack must beat or justify its cost against a single-agent baseline on a reviewed dataset. Insufficient evidence must be a valid result, not an invented success.
3. **Connect one external system.** Offer GitHub/CI retrieval first. Add write actions only with approval, deterministic authorization, repeat-safe publication, and visible receipts. Demonstrate duplicate-event delivery and a crash/replay without duplicate publication.
4. **Make operation predictable.** Surface usage, failure classifications, safe retry controls, update/rollback, retention, and cancellation behavior. Test old pinned runs during a configuration rollout.

For a managed multi-tenant service, tenant isolation, identity, quota enforcement, data retention, and billing ownership become additional release blockers. For an individual-developer product, local setup becomes the central requirement. Neither audience change should silently expand the first self-hosted release.

## Research and verification limits

This assessment inspected source and current primary documentation. It did not deploy Hatchet, Kubernetes, brokers, or a model-backed workflow. No latency, cost, accuracy, or adoption claim here is a benchmark result. Only Markdown research/report files were added. The recommendations should be tested with pilot users and model evaluations before being treated as a committed roadmap.
