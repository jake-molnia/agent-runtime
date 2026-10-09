# T3 on Agent Sandbox: reviewed architecture

The recommendation is one persistent T3 application, its existing SQLite database, the existing Go agent-runtime extended with Hatchet lifecycle workflows, and a small TypeScript execution worker inside each Kubernetes Agent Sandbox. Each top-level conversation owns a durable workspace and a replaceable sandbox. Delegated subagents inherit that sandbox while retaining their own conversation and provider identities.

This is a substantial execution-boundary change, not a database rewrite or a second implementation of T3 orchestration. Most feature logic can remain upstream code if it executes on the correct side of that boundary. A webhook or setup hook can initiate allocation, but cannot redirect filesystem, SDK, terminal, and browser operations by itself.

Reviewed on 2026-10-08 against T3 `580948708b66f2a971199670f76cb6b43119e9a4`, agent-runtime `476315dba982a73dba526a7c8f9da486c27165db`, Agent Sandbox `v1.0.3`, and Hatchet Go SDK `v0.109.10`. The implementation proposals below do not yet exist. Source findings and proposed acceptance tests are distinguished throughout. No live cluster, image build, provider login, or recovery test was performed.

## 1. Constraints and ownership

The following rules are fixed by the requested product:

1. One central T3 environment owns conversations. Preserve its environment ID, public API, ordinary client connection, and SQLite persistence. Run it as a single active Kubernetes workload with persistent storage, without user-managed machine installations.
2. Agent Sandbox provides execution isolation. No web UI, pairing service, central T3 database, or second conversation orchestrator runs in those allocations.
3. Go agent-runtime and Hatchet own durable provisioning and lifecycle operations. T3 must not grow another allocator database or resource controller.
4. New top-level conversations receive independent sandboxes. App-owned delegated children and grandchildren share their parent's allocation. Provider-native subagents remain in it too.
5. Sharing execution does not merge thread IDs, provider-native sessions, approvals, MCP invocation identity, or cancellation targets.
6. Replacing compute never replaces the conversation ID or durable workspace identity.
7. Pool, pod, machine, endpoint, and credential bootstrap details are hidden from normal users.
8. Settlement releases compute after safe completion. Workspace purge is a separate operation. Data retention does not mean keeping an idle pod running.
9. Existing T3 features remain available only when their execution route is implemented and tested. Never advertise a provider or tool as usable because its binary merely exists.
10. Suspension in this Agent Sandbox version kills the pod. An unanswered live approval is not a safe stop point merely because its question is stored in SQLite.

The ownership boundary is:

| Responsibility | Owner | Persisted where |
| --- | --- | --- |
| Messages, threads, runs, approvals, delegation, settings, schedules, PR links | Central T3 | Existing T3 SQLite and data directory |
| User intent, command receipt, execution request outbox | Central T3 | Existing transactional event/outbox system, with small binding additions |
| Root workspace binding and latest known execution status | Central T3 reference/projection | SQLite; observed runtime status is rebuildable |
| Allocation, resource mutation, retries, readiness, suspend, release | Go agent-runtime using Hatchet | Hatchet workflow inputs/results/receipts, plus owned Kubernetes resources |
| Actual pod/volume/service existence and observed generation | Kubernetes Agent Sandbox and storage controllers | Kubernetes objects/status |
| Provider SDK sessions, PTYs, local files, Git operations, browser objects | TypeScript execution worker | Live process state; durable native files on workspace PVC |
| Worker command receipts and unacknowledged event delivery | Execution worker | Small local journal on workspace PVC, not a T3 conversation database |
| Attachments and published HTML/screenshots needed after release | Central T3 | Existing durable attachment/artifact storage |

T3 decides whether conversation work permits stopping. Agent-runtime performs the stop and verifies that it happened. Hatchet does not decide whether a provider has finished its turn, and T3 does not directly mutate Kubernetes resources. These are separate authorities with an explicit request and acknowledgement, not two copies of one state machine.

## 2. Components and the chosen execution boundary

Deploy three application roles, reusing the existing services:

- The central T3 process serves the UI/API/MCP and owns SQLite. The web assets can stay bundled with it for the minimal deployment. Static hosting is optional, not required work.
- The existing Go agent-runtime worker registers new T3 lifecycle workflows with Hatchet alongside its OpenCode workflow. Add a narrow internal control API for T3 to submit/observe those workflows. Keep Hatchet and Kubernetes credentials here.
- The allocated sandbox starts a TypeScript execution worker built from the same T3 revision, alongside generic sandboxd/bootstrap functionality where useful. The worker hosts selected existing execution services, not T3's public server or database layer.

Two independent reviews compared central adapters with remote process/files against worker-local adapters and services. Both favor worker-local execution. Codex and ACP expose useful process-spawn seams, but Claude calls its Agent SDK `query`, Cursor uses its SDK session, and Muse calls `spawnMspConnection`. They do not all route process creation through a replaceable Effect spawner. Native search, node-pty, direct Node filesystem descriptors, and Playwright also escape a generic remote filesystem abstraction.

Keep provider normalization and native SDK process state together in the worker. Add a remote implementation of T3's normalized adapter contract centrally. Keep local VCS sequences, checkpoint file capture, terminal management, filesystem/search, project-command execution, and browser automation worker-local. Retain authorization, run decisions, PR API state, project settings, and durable publication centrally.

Do not relocate every class named “service.” For example, `GitManager` combines local VCS with central settings, text generation, project state, and PR services. Keep that coordination central and invoke worker-local VCS operations. Likewise, T3 chooses the project script centrally; the worker executes it and retains its operation result.

This preserves the existing provider drivers and local execution implementations better than maintaining remote-spawn exceptions in every provider. It still requires separating constructors that currently mix application state with local execution.

## 3. Identities and the smallest persistence changes

Use four distinct identities:

- `ThreadId`: durable conversation or delegated child.
- `WorkspaceId`: durable execution/filesystem ownership. Shared by one top-level conversation and its delegated descendants.
- `AllocationId` plus generation: one disposable compute assignment for that workspace.
- Worker incarnation: one actual worker process lifetime. A container can restart inside the same Pod, so Pod UID is insufficient.

A checkout within the workspace can have its own `CheckoutId` to preserve worktree features. Root and child usually share one checkout, but sandbox ownership must not depend on which checkout a thread uses.

Add a thread-to-workspace binding in the same transaction that creates the thread. Store an opaque agent-runtime ownership handle with the workspace reference. Add only the event cursors, execution operation references, and user-facing status projections needed for transport/recovery. Do not duplicate Hatchet's resource inventory in new T3 allocation tables. Existing run/provider/session tables remain authoritative for conversation state.

For a root, derive a stable workspace request key from the central environment and root thread identity. For a delegated child, inherit the parent's binding in the child-create transaction. Do not derive allocation identity from a fresh Hatchet workflow-run ID on each wake.

T3 lineage distinguishes `fork` from `subagent`. A conversation fork can have a parent and root lineage while still being a new top-level work item. Consequently, neither “has parentThreadId” nor the current lineage root is enough to select execution ownership. The explicit binding decides. A top-level fork receives a new sandbox and a defined source checkpoint/workspace snapshot; a delegated task inherits execution. Native provider subagents need per-provider handling of their actual session/MCP identity rather than fabricated independent sessions.

A path is not a worker address. Several existing file/list/search contracts accept only `cwd`. `/workspace/repo` can exist in every sandbox, so route by workspace reference and then resolve the path there. Thread-scoped methods can derive that reference centrally. Add it explicitly to methods that lack a thread. Never infer destination from a browser's active tab, the central environment ID, or a global “current sandbox.”

Illustrative caller-facing shape, not a proposed second framework:

```ts
// Actual implementation should derive branded IDs and payloads from Effect schemas.
interface ThreadExecution {
  forThread(threadId: ThreadId): Effect<WorkspaceRef>;
  withReady<A>(workspace: WorkspaceRef,
    use: (worker: WorkspaceExecution) => Effect<A>): Effect<A>;
}

interface WorkspaceExecution {
  providers: RemoteProviderAdapter;
  files: WorkspaceFiles;
  vcs: WorkspaceVcs;
  terminals: WorkspaceTerminals;
  browser: WorkspaceBrowser;
  runProjectAction(input: ResolvedProjectAction): Effect<OperationRef>;
}
```

The checkpoint effect resolves the thread once, calls the workspace VCS operation with its existing checkpoint identity, and commits the returned OID centrally. Feature callers do not coordinate claim creation, readiness, MCP registration, volume mounting, or Hatchet retries themselves. Those belong behind readiness and the runtime control contract.

## 4. Build lifecycle on agent-runtime and Hatchet

Reuse the current packages deliberately:

| Existing code | Reuse | Required extension |
| --- | --- | --- |
| `sandbox/control.go` | Stable names, ownership/spec checks, watches, UID-guarded mutations | Direct Sandbox + retained PVC path; generation-aware readiness; verify UIDs after watch relist; observe completed suspend/delete |
| `sandbox/runtime.go` | sandboxd process/file APIs | Bootstrap/diagnostics for the new worker, appropriately restricted transport |
| `hatchetbridge` | Workflow registration, successful step outputs, durable waits, telemetry | T3 workspace lifecycle tasks with stable root identity, keyed concurrency, idempotent transitions |
| `artifacts/store.go` | Bounded atomic artifact writes and sync | General artifact metadata/read/restore if needed; existing JSON Put is not a full workspace backup |
| `tailnet` | Optional identity/proxy support | Extend resource inventory if direct Sandboxes replace claims; use cluster networking by default for same-cluster T3 |
| `telemetry` | Lifecycle spans and progress | Correlate root, workspace, operation, allocation generation and incarnation |
| `orchestration.Engine` and `runtime.Supervisor` | Existing OpenCode product remains intact | Add a T3-specific integration beside it; do not teach T3 to imitate OpenCode session APIs |

The current `agent-run` workflow is per-run OpenCode execution. It keys native IDs to `WorkflowRunId`, has zero provision/execute retries, imposes a 24-hour execution timeout, and collects an OpenCode session export. It cannot be used unchanged as a conversation lifecycle lasting weeks.

Add a T3 lifecycle transition workflow with an internal request resembling:

```text
workspaceRef, operationId, requestRevision,
action = ensure_running | suspend | release_compute | purge_workspace,
resourceProfileRevision, expectedAllocation, quiescenceProof?
```

The input references secrets and large artifacts; it does not contain provider tokens, conversation transcripts, or workspace archives. Hatchet persists task progress and results. Kubernetes names/UIDs and workspace markers let tasks rediscover effects after a crash before a successful step output exists.

Use per-workspace concurrency of one for resource mutation. The pinned SDK exposes keyed concurrency, shared tenant-scoped limits, and workflow idempotency. Prefer one short transition workflow type with queued mutations; if split across definitions, share the same concurrency key/limit. Do not use cancellation of an in-progress Hatchet task as if it undid an already submitted Kubernetes delete.

A normal ensure-running operation does the following:

1. Resolve the stable workspace and latest applicable lifecycle request. Reject stale expected generation/revision.
2. Ensure the retained volume exists and matches ownership. Recover an existing compatible allocation by deterministic identity before creating another.
3. Create or resume the Agent Sandbox, then observe current-generation Kubernetes readiness.
4. Bootstrap the worker and validate authenticated identity, image/protocol, workspace mount, and provider capability handshake.
5. Return a ready execution handle. T3 persists the observed result before dispatching provider work.

Hatchet idempotency TTL/status scopes are bounded. Handle an idempotency collision by attaching to the existing run; stable external resource names and UID/spec checks remain necessary after the idempotency window expires. Workflow completion alone is not proof a pod is currently ready.

Lifecycle events wake workflows, but are not the only durable copy of desired work. T3 commits requests through its existing outbox; Hatchet receives a stable operation identity. Maintain a monotonic workspace admission revision in T3 alongside the binding, incremented transactionally for new work and membership changes across all children. Before destructive resource mutation, the workflow reads current T3 admission status or consumes an equivalent current CAS-protected fence. An old immutable workflow input is not evidence of the latest intent. Periodic runtime reconciliation checks known owned resources and incomplete transitions. If T3 is unavailable, unknown semantic liveness does not authorize deletion. Explicit administrative hard expiry is a forced interruption policy, not safe idle suspension.

Use resource-version compare-and-swap for mode/fence changes and UID plus resource-version preconditions for deletion. UID alone does not prevent an old delete from removing the same Sandbox after it has resumed. Serialize lifecycle mutations and keep worker admission closed across the irreversible stop boundary; a later wake may need to wait and create the next allocation.

Do not place model token streams, PTY traffic, browser frames, or every MCP call into Hatchet workflow history. Those use the direct T3-worker channel. Hatchet provides lifecycle durability, not a replacement chat/event database or a provider protocol tunnel.

## 5. Storage and Agent Sandbox resource policy

Keep T3's full data directory on a single-writer PVC, including SQLite WAL-related files, environment identity, settings, secrets, attachments and published artifacts. Use a storage/filesystem combination supporting SQLite semantics. Single-replica replacement must not overlap two active writers. Backups must use a consistent SQLite backup/checkpoint procedure rather than copying a live database file alone.

Each root workspace gets a separate retained PVC managed by agent-runtime. Retain the entire repository graph: checkout, `.git` common directory, linked worktree administration, submodules, untracked files, hidden checkpoint refs, provider native homes/session files, copied inputs, and required logs. Keep mount paths stable across replacement. A Git clone or checkpoint does not preserve all of that state.

Important source finding: Agent Sandbox creates VCT-derived PVCs with Sandbox owner references. Deleting the claim/Sandbox can garbage-collect those PVCs. Claim expiry policy `Retain` retains the claim while deleting its underlying Sandbox. Neither that setting nor PV reclaim policy Retain automatically gives a replacement allocation access to an intact bound workspace.

The minimal reliable first path is a direct Agent Sandbox resource referencing an already existing, separately owned PVC in its PodTemplate. Leave `volumeClaimTemplates` empty and set `service: true`. The pinned controller copies explicit pod volumes and only adopts/creates VCT volumes, so this avoids transient compute ownership of durable data. Extend the current Go Control to support this direct path; it remains Kubernetes Agent Sandbox, not a different sandbox backend.

Idle suspension retains the Sandbox/PVC while terminating its pod. Settlement can delete the Sandbox and retain the workspace PVC. Only a separate purge operation deletes that PVC, after all member threads and compute have released it. Wait for the old writer to terminate and storage to detach before activating a replacement. RWO alone does not exclude two writers on the same node; use RWOP where available plus lifecycle fencing.

Warm-pool tradeoff is real. Claims require a pool and cannot generally substitute an arbitrary existing PVC. Per-claim environment or VCT overrides force cold starts. A generic prewarmed pod cannot simply have its volumes changed into a returning workspace's retained PVC. Start with cold allocation and cached images; preserve hidden resource-profile policy so a proven warm-storage strategy can be added later. Do not introduce a per-root template and pool merely to work around that limitation. Do not strip owner references during deletion and hope to win a garbage-collection race.

Current config templates also use `emptyDir` home and omit explicit Service creation. Their marker-file readiness check does not prove the T3 worker is usable. These inspected manifests need a dedicated T3 execution variant.

## 6. Remote commands, events, and restart behavior

Use one versioned internal worker protocol for typed provider/feature operations and bounded streams. Reuse T3 contracts where possible but do not serialize Effect values, callbacks, scopes, or live SDK objects. Protocol and image compatibility are checked before opening a session.

Each mutation carries an operation ID, payload hash, workspace/allocation generation, worker incarnation, and its thread/run/provider ownership where relevant. The worker rejects wrong-generation work. Kubernetes object generation, allocation epoch, worker incarnation, and central admission revision are separate fields, not one overloaded counter. Same ID and same payload returns the known status/result; the same ID with a different payload is an error. Cancellation records a target operation tombstone so a delayed start cannot run after Stop.

Record accepted, started, and result separately. A successful socket send is not durable acceptance. For a provider start, the operation result is successful turn admission/start with a stable native reference; final run completion still arrives through events. A checkpoint/script operation has its own completion definition. Preserve these distinctions rather than waiting for every whole run inside T3's outbox executor.

Worker events carry incarnation and monotonic sequence. T3 commits event dedupe/cursor movement with the corresponding normalized events/projections, then acknowledges. Replay after lost acknowledgement must not duplicate messages, approvals or child completions. Gaps trigger replay or explicit snapshot repair. Backpressure and journal limits must stop admission or interrupt visibly before losing required events; token output must not grow memory without bounds.

The worker journal is an execution receipt/replay mechanism, not authoritative conversation history. A small SQLite journal on the retained workspace is a reasonable implementation. Hatchet remains authoritative for lifecycle operations. A script that sent an external request and crashed before recording success has an ambiguous result; neither journal nor Hatchet makes arbitrary shell commands exactly once. Reconcile known effects or mark interrupted. Never blindly repeat them.

Central restart and worker restart are different:

- With the same live worker incarnation, reconnect, reauthorize, inspect active operations/requests, and replay events before releasing queued commands. Pending provider callbacks may still exist. Existing T3 shutdown/recovery must not blanket-kill them or mark their runs cancelled merely because the central process restarted.
- With a new worker incarnation, SDK closures, approval callbacks, PTYs and browser objects are gone. Expire process-bound requests, mark active attempts interrupted, preserve files/history, and resume through the provider's supported native session/continuation path. A still-visible question is not an answerable callback.
- If the old worker cannot be reached, do not immediately launch a second writer. Establish termination/fencing through agent-runtime first. If that cannot be established, remain unavailable with a clear execution status rather than risking concurrent writes.

The robust target is reattachment across central restart. The independent reviewer recommends an initial conservative interrupting release; I accept that as a prototype milestone, but require the reconnect proof before claiming the requested feature-preserving production behavior. A first spike may explicitly interrupt/drain on central restart as a limited mode, but it must not be presented as preserving active sessions. In that mode, quarantine and terminate/fence the old writer before terminalizing the attempt or starting a replacement; recording cancellation in SQLite first would allow a supposedly cancelled run to continue editing. Before rollout, prove MCP token recovery and pending callback/event replay. Simply disabling T3's existing recovery is also wrong because it strands outbox effects and orphaned sessions.

The operational failure contract is explicit:

| Failure | Required behavior |
| --- | --- |
| Hatchet unavailable | Existing worker execution may continue through its direct channel. New lifecycle work remains in T3's durable outbox; do not report a sandbox ready or deleted prematurely. |
| Central T3 unavailable | Workers retain bounded unacknowledged events. Tool calls requiring central authority wait/fail explicitly. No new destructive lifecycle decision may infer idleness from central silence. |
| Worker unreachable | Distinguish transport loss from process death. Quarantine admission until identity/liveness is reconciled; do not activate another writer on the same volume. |
| Node/pod loss | Retained storage and native files survive if the storage system does. Process-backed work is interrupted. Recovery waits for old-writer exclusion before replacement. |
| Workspace volume unavailable | Chat/history stay usable. Execution remains blocked with the stored workspace identity; never silently create an empty replacement workspace. |
| Protocol/image mismatch | Reject new sessions with a useful status. Pin active allocation versions, and roll upgrades through tested drain/reconnect paths. Do not replace executables under running providers. |
| Worker journal fills during outage | Stop admitting work and apply a bounded interruption/backpressure policy before dropping authoritative events. Retain enough state to diagnose the interrupted attempt. |

## 7. MCP, delegation, and callbacks

Keep central T3 MCP ownership and toolkit implementations for conversation operations. Route tool handlers that need files/processes through the calling thread's workspace binding. `delegate_task`, task status, result delivery, scheduling, PR links, and explicit new-thread launch retain their T3 semantics.

The current MCP endpoint defaults to loopback for wildcard server binds. That address is wrong inside a remote sandbox. The registry and `McpProviderSession` config are also process-local maps, so a central restart loses issued token state even while providers remain alive.

Prefer a small per-session authenticated MCP forwarding endpoint in the execution worker. Native providers receive a stable worker-local address and a distinct local session credential. The worker maps that session to the central thread/provider scope and forwards to central MCP over its authenticated connection using replaceable upstream credentials. Central still authorizes each tool. Refresh validates current thread/session membership, revocation, capability grants and allocation incarnation; an old local proxy credential must not revive a revoked session. This handles provider configurations that cannot hot-swap headers without restarting the native session.

That proxy is intentional worker loopback, unlike accidentally advertising central loopback. It must support streamable HTTP/SSE, MCP session IDs and reconnection, cancellation, and the provider-specific callback paths. Reauthorizing does not automatically recover an in-flight central MCP tool call. Side-effecting calls need stable tool-operation correlation and reattachment/reconciliation; do not blindly replay a thread-creation tool after losing its response.

ACP's stdio bridge and terminal fallback also need to exist in the image. Preserve `T3_ACP_MCP_ENDPOINT` and the executable entrypoint/environment contract with worker-local paths. Include native Codex MCP elicitation and MCP Apps calls in protocol tests; they are not interchangeable with ordinary HTTP tool calls.

Every app-owned child gets its own logical provider session and MCP scope while sharing a workspace. Include workspace identity in provider-runtime residency/pooling keys so central provider-instance reuse cannot bridge independent roots. Within a workspace, OpenCode 2.x may share an instance-owned helper server across sessions; stop/unload must release only the targeted session until the shared server has no remaining owners. Never write a single mutable global MCP entry into the shared project for all agents. Preserve OpenCode's thread-specific registrations and deny-other-thread behavior. Native subagents may expose only their parent session scope; preserve what the provider actually supplies and do not claim independent security identities it does not have.

Subagents sharing a pod/filesystem are cooperating agents in one trust boundary. Per-session MCP scope prevents routing mistakes and controls tools; it does not create hostile-tenant isolation between siblings that can read the same files.

## 8. Safe suspend, settlement, and wake

Safe idle is a property of the entire workspace group, not the root thread's latest message. Aggregate T3 runs, queued runnable work, child tasks and outstanding effects, then ask the worker about actual callbacks, processes, terminals, scripts, browser activity and filesystem mutations.

Stop is blocked by a live provider turn, blocking approval/question/elicitation, active descendant, pending execution mutation, setup/settle script, checkpoint/restore, or another workload promised to remain alive. Future schedules and central PR polling do not need a running sandbox. A bare idle shell can close. An active foreground terminal command is not an idle shell.

The stop sequence uses an admission barrier:

1. T3 requests a transition tied to a conversation/workspace revision after checking its durable activity.
2. The worker stops admitting new side effects for that transition and reports activity plus a drain result. It flushes required receipts, native state, logs and generated assets.
3. T3 rechecks its revision and descendants. Agent-runtime consumes the bounded drain proof, verifies the current allocation/incarnation, then performs the Hatchet-controlled suspension/release.
4. The operation completes only after current-generation suspension or resource absence is observed, not when a PATCH or DELETE returns.

A new message before destructive stop invalidates the drain if still cancellable. Once stop is committed, the message remains durably queued and waits for completion plus ensure-running. It must never execute into a worker the old stop workflow can still delete. This is safe even when the new message cannot “win” the race.

Settlement currently closes idle shells, preserves running terminals, starts settle scripts, and detaches their completion. Replace that cleanup invocation with a durable action identity and completion barrier before release. Run shared destructive settle cleanup once at workspace ownership level, after children finish; child settlement must not run a root cleanup concurrently with siblings. Failed cleanup prevents successful release reporting. A configured force-stop deadline is explicit policy, not silent success.

The initial policy should preserve live user work and delay compute release while it remains active. If the product later wants settling to terminate an unattended dev server, define that separately and test it. Do not let automatic inactivity settlement kill a busy user terminal merely to match a lifecycle label.

Agent Sandbox cannot preserve all live approval waits by suspension in the pinned version. Codex message-backed asynchronous questions are a useful exception after active work finishes. Other live waits remain running until resolved. This limitation is intrinsic to process termination, not a missing database column.

## 9. Feature preservation and less obvious blockers

The detailed audits provide exact file/line/fix/test matrices. These are the main consequences for the design:

| Feature | Preserve centrally | Move or adapt | Release gate |
| --- | --- | --- | --- |
| Chat, history, approvals, subagents | Existing T3 state and orchestration | Remote provider execution/events | No duplicate run or result after reconnect |
| Files/search | Authorization and workspace target | Node FS and native index in worker | Same paths in two sandboxes return different correct contents |
| Attachments | Claims, uploads, published data | Transfer immutable inputs; worker-local prompt paths | Images/files/questions work; published assets survive release |
| Git/worktrees | Intent, settings, repository identity | Local VCS sequences and metadata | Worktree/submodule/common-dir survive replacement |
| Checkpoints | IDs and event metadata | Worker-local capture/restore with immutable retry result | Lost capture response cannot change historical content |
| Terminals | UI ownership and central routing | PTY manager, process inspection, local bounded history | Reattach to same PTY after central restart; worker death reports exit |
| Setup/settle actions | Selection and durable action identity | Worker execution/sentinel/status | One action per identity; completion before release |
| Preview | User/agent authority and client transport | Browser, port discovery, local downloads in worker | Port 3000 in two workers stays isolated; stale DOM refs rejected |
| HTML rendering | Publication and attachment identity | Inline worker-local assets and render | `/tmp/image.png` resolves on worker, page survives compute deletion |
| PRs/webhooks/schedules | Existing records and API polling | Repo-dependent Git/CLI uses worker | Dormant PR polling does not wake every workspace |
| Provider models/health/skills | Settings/account selection and bounded cache | Binary/native account/workspace probes | Empty central PATH does not mark worker provider unavailable |
| Desktop/device functions | Existing device consent/authority where applicable | Worker-executable bridge or explicit capability unavailable | Never return a central-only launcher path to an agent |

Three especially subtle cases require deliberate choices.

First, checkpoints in a shared checkout are observations of shared files, not per-agent changes. T3's existing capture mutex does not freeze agent or terminal writes. Keep current restore protection for overlapping/shared workspaces. Compare workspace identity before worker-local canonical paths so identical paths in independent sandboxes do not falsely collide. Do not promise one child's file rewind can safely undo only that child.

Second, checkpoint capture currently unconditionally updates its hidden Git ref. Lost response followed by retry can capture later edits into the old checkpoint. Make capture create-once by stable checkpoint/operation identity and return the existing OID on retry. Test the failure after Git writes the ref but before T3 commits its result.

Third, projects have operations before a thread exists: folder registration, browsing, branch selection, settings and `t3.json` discovery. A thread-only execution binding leaves them stranded. Preserve those features with a retained project seed workspace operated by short-lived Agent Sandbox allocations through the same runtime. It runs no agent by default. New root threads get independent clones initialized from a pinned source. Project-wide operations target the seed explicitly; conversation operations target the thread workspace. Never silently choose one active thread's checkout as the project root. Importing a local-only repository needs a workspace upload/import path; local-folder creation on the central host is not cloud execution.

Top-level conversation forks need a separate content policy. For a current-workspace fork, acquire a whole-workspace quiescence barrier and copy a consistent repository/native-state snapshot to the new retained volume. For a historical run/checkpoint fork, restore the requested recorded state and use a provider-supported native fork or portable context handoff. Do not silently substitute the project seed commit or copy current dirty files into a historical fork. If the required snapshot/native state cannot be produced, the fork operation must report the unsupported source point rather than pretending it preserved it. The snapshot/copy method and provider matrix are explicit implementation gates; checkpoint capture alone does not establish a consistent snapshot of live shared writes.

## 10. Image, authentication, and deployment

Build the T3 TypeScript worker from the same pinned commit as the central server. Tag images with their content digest, worker protocol version and provider manifest. Pass versioned immutable provider configuration snapshots to session creation. Central instance settings changes affect new/rebuilt sessions through an explicit update policy; a worker does not independently watch or write T3 settings. A rolling upgrade must preserve negotiated compatibility with existing allocations or drain them before changing the required protocol. Preserve one provider implementation in the T3 fork; avoid copying adapter files into a separately maintained Go repository. The agent-runtime image/build can consume the packaged worker artifact and add generic runtime tools.

The image includes Codex, Claude, Cursor's required runtime assets, Grok, OpenCode 1.x/2.x in distinct prefixes, Antigravity, Pi, Muse, and a declared ACP extension set supported on the selected Linux architecture. T3 supports arbitrary ACP commands, so “everything” cannot mean an infinite registry. Keep extension support with explicit version/platform capabilities. Headless Chromium and its dependencies are part of browser-enabled profiles.

Existing config `packages/t3code/Dockerfile` has reusable toolchain/harness stages, but its current T3 server stage is not the desired worker. It requires amd64 and does not prove every harness works. Maintain a manifest generated/checked against the built-in driver list; a new upstream driver should make image coverage fail visibly.

Provider installation, provider authentication, MCP authentication, and Kubernetes identity are separate. Keep Kubernetes/Hatchet admin credentials out of sandboxes. Resolve scoped provider credentials during bootstrap, preserve native resume data per workspace, and keep central account authority. OAuth refresh tokens are not safe to clone into unlimited parallel writers without checking provider behavior. Use API credentials where supported; for native subscription sign-in, serialize refresh through an account owner if the provider permits token import, or explicitly report that flow unsupported until a tested mechanism exists. No generic broker can be assumed to work with every CLI's opaque auth store.

Models/health/usage/skills currently inspect local installs and native homes, and text-generation helpers may start provider processes. These must use worker/image/account probes too. Account setup and utility generation can use the same execution machinery with a scoped administrative/project workspace, not a permanent provider process on the central server. Keep cached catalog metadata distinct from a current authenticated health result.

For same-cluster deployment, internal Kubernetes service networking is enough; Tailscale is optional existing infrastructure, not mandatory per-sandbox enrollment. Restrict sandboxd and worker control reachability, authenticate the worker channel, and verify allocation identity beyond its DNS name. The central volume and cluster credentials are never mounted into execution workers. Existing T3 environment authorization is not automatically a multi-tenant SaaS isolation model; this design preserves the current trusted deployment scope rather than inventing tenancy.

The actual RuntimeClass, NetworkPolicy, CSI driver and browser sandbox settings need an integration proof. Agent Sandbox is a controller; workload isolation depends on the configured runtime and policy. Follow the existing cluster runtime configuration rather than promising microVM semantics from the API name.

## 11. Concrete blocker register

| ID | Existing assumption / failure | Required change |
| --- | --- | --- |
| B01 | MCP advertises central loopback | Worker-local scoped proxy or verified reachable endpoint; ACP executable paths |
| B02 | MCP credentials/config live only in central memory | Reauthorization/rebinding after central restart without restarting live provider callbacks |
| B03 | Claude/Cursor/Muse SDKs launch outside generic spawner | Host adapters and SDKs worker-side |
| B04 | Files/search APIs select by cwd alone | Explicit workspace target in shared contracts/callers |
| B05 | Direct FS/native search/PTY/Playwright bypass injectable services | Worker-local semantic operations, not transparent OS virtualization |
| B06 | Central shutdown/recovery assumes all providers died | Worker-incarnation-aware recovery and reconnect-before-replay |
| B07 | Generated PVC ownership follows Sandbox | Runtime-owned retained PVC mounted explicitly; separate purge |
| B08 | Existing home is emptyDir; readiness is marker-only | Persistent native home/repo and current-generation worker handshake |
| B09 | Control readiness ignores generation; watch relist can miss UID replacement | Generation/UID/Pod/incarnation validation and completed-mode observation |
| B10 | Current Hatchet job is one expiring OpenCode run | T3 lifecycle transitions keyed by durable workspace, using existing runtime primitives |
| B11 | A child is a real T3 thread; forks also have parents | Explicit binding inheritance for delegation only |
| B12 | Settlement detaches script completion and preserves busy terminals | Durable group drain, script receipts, agreed terminal policy |
| B13 | Checkpoint retries overwrite hidden refs | Create-once capture/result and workspace-scoped restore safety |
| B14 | Attachments/media/HTML carry central local paths | Explicit file locality, staging and central durable publication |
| B15 | Preview localhost and port inspection refer to central host | Browser/port scanner on worker, streamed client view |
| B16 | Provider catalog/auth/skills/usage inspect central machine | Worker-scoped probes, account ownership and finite image manifest |
| B17 | Project/draft operations may have no thread | Explicit project seed workspace and import/init semantics |
| B18 | Native session fork assumes files/session exist locally | Copy source state at a stable point or use supported portable handoff into new root |
| B19 | Device tools return central launcher paths | Remote worker bridge or accurately reduced capability |
| B20 | Hard expiry and duplicated retries can bypass safe drain | Hatchet transition fencing; forced expiry reported as loss; no unsafe replay |

Each is tied to source in the companion audits, not a hypothetical distributed-systems checklist.

## 12. Implementation sequence and acceptance

Deliver vertical slices so an incomplete feature cannot quietly fall back to central execution.

1. Establish the ownership contract and worker boundary. Add binding inheritance, workspace targets, and a versioned test worker. Prove two roots with identical paths/ports stay separate; three-level delegated descendants share only their intended workspace. Unsupported remote operations must fail explicitly.
2. Extend agent-runtime with direct Sandbox + retained PVC and register Hatchet transitions. Prove resource create-timeout recovery, UID/generation checks, retained data through suspend/delete/recreate, duplicate ensure, and stale release after wake. Keep the OpenCode workflow unchanged.
3. Connect central T3 to worker provider adapters and MCP. Use both Codex and an SDK-owned provider, such as Claude or Cursor, to avoid validating only the easiest process seam. Prove real file edit, approval, child delegation, cancellation and continued conversation.
4. Add remote Git/checkpoints/files/attachments/terminal/scripts and project seed operations. Use a central filesystem containing trap files at the same paths and verify it remains untouched. Test lost acknowledgement after checkpoint/script completion and shared-checkout restore protection.
5. Complete central reconnect and worker-loss recovery before enabling automatic lifecycle. Prove same-worker approval survives central restart; new-worker approval expires honestly; no duplicate provider start; queued messages and child results deliver once.
6. Add safe idle and settlement drain with Hatchet. Race new input, child completion, webhook, checkpoint, running terminal and slow settle script against suspension/release. Keep files/history after compute deletion and reopen with the same identity.
7. Complete the provider image matrix, previews/HTML, account/catalog paths, PR workflows and capability decisions. Verify web, desktop and mobile shared client contract changes together.

Release tests must kill processes and drop responses at known boundaries, not rely only on unit mocks. Existing T3 unit suites remain useful but cannot prove remote locality or crash behavior. Measure readiness, first-token and resume latency separately; record warm/cold, volume-attach, hydration and provider-start components. Do not promise a latency number until measured.

The non-negotiable demo is: launch two independent conversations; spawn multiple child agents in one; approve a tool; edit files; capture a checkpoint; use a terminal and browser preview; restart central T3 while workers live; suspend an idle root; wake it through a bound webhook; settle it; delete compute; reopen the same conversation and recover its workspace. At each step, history remains central, children retain correct MCP identity, and no operation executes on the central filesystem.

## 13. Review record and evidence

Three parallel source audits covered providers/MCP, workspace features, and lifecycle/storage. An independent cross-review evaluated the combined design. The execution candidates independently converge on worker-local adapters/services. The major disagreement is rollout policy for central restart: conservative interruption is simpler, while true reattachment better preserves T3 behavior. The recommended target is reattachment with a separate explicit limited spike mode, not a silent claim of compatibility.

Accepted additions include stable workspace identity, retained PVC ownership, typed worker operations, incarnation-scoped recovery, durable script/checkpoint results, and Hatchet lifecycle fencing. Rejected approaches include per-sandbox T3 servers, a PostgreSQL migration, a second T3 allocator DB, one giant multiweek OpenCode workflow, remote-spawner-only architecture, and automatic deletion from the thread.settled event.

Detailed findings:

- [Provider and MCP audit](review-evidence/providers-mcp.md)
- [Workspace and feature audit](review-evidence/workspace-features.md)
- [Agent Sandbox, Hatchet and storage audit](review-evidence/lifecycle-storage.md)
- [Independent review verdict](review-evidence/review-verdict.md)
- [Machine-readable source checks](review-evidence/source-checks.json)

Re-run the read-only evidence check with:

```sh
python3 docs/research/collect_t3_execution_evidence.py \
  --t3 /path/to/t3code \
  --agent-sandbox /path/to/agent-sandbox-v1.0.3
```

It verifies concrete code anchors and records the T3 revision. It does not run the proposed architecture or establish complete feature coverage. Further work must prove the provider auth matrix, MCP reconnect behavior, deployed Hatchet semantics, CSI storage behavior, and the internal worker extraction before implementation scope can be estimated credibly.
