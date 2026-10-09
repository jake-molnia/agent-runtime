# Set up filesystem agents and GitHub reviews

## Author an agent

Start with `examples/definitions`. A deployment contains:

```text
deployment.yaml
agents/github-reviewer/agent.yaml
agents/github-reviewer/instructions.md
agents/github-reviewer/skills/review/SKILL.md
agents/github-reviewer/output.schema.json
automations/github-pr-review.yaml
```

Directory and file names determine agent and automation names. YAML uses version 1.
There is no expression language, imported runtime module, or author-selected
OpenCode agent selector. The compiler chooses `authored` internally.

Agent packages declare behavior, model, timeout, capability references, and an
optional JSON Schema. Deployment profiles grant capabilities and select pool,
namespace, session directory, tailnet tags, provider configuration, and approved
credential bindings. Only `github.diff` is registered initially. The trusted handler
fetches canonical diff data; no GitHub tool or token is exposed to the model.
Plain agents can request no capabilities and accept a prompt through `agent-run`.

All model tools are denied by global and agent-level OpenCode V2 permissions.
The deployment schema deliberately has no unsupported network policy switch.
Apply actual network isolation through Kubernetes policy and sandbox pool configuration.
The worker does not create those policies. Authoring arbitrary shell tools or
MCP integrations requires a reviewed Go implementation and an enforced capability.

The reviewer is intentionally diff-only. It receives repository and revision
metadata plus bounded patches, not a repository checkout. This avoids running PR
scripts or making checkout credentials available to untrusted code. It cannot
inspect unchanged files, run tests, or review binary patches without text content.
The encoded review context is limited to 1 MiB and at most 300 files. Oversized
PRs fail before sandbox provisioning rather than silently reviewing a truncated diff.

Never use the PR checkout as `AGENT_DEFINITIONS_DIR`. Keep authoring and deployment
files in a trusted image or read-only deployment directory. The loader rejects
unknown fields, unsupported versions, unsafe paths, symlinks, malformed YAML,
invalid schemas, and ungranted references before registration. Kubernetes projected
ConfigMap and Secret mounts use symlinks. Materialize their contents as ordinary
files in trusted directories before loading definitions or provider credential bindings.

## Configure the worker

The existing Kubernetes sandbox runtime and Hatchet deployment must already work.
Provision a sandbox pool named `agent-review` in namespace `agents`, or change
`examples/definitions/deployment.yaml` to your existing pool. The worker needs
the existing Kubernetes permissions to create, inspect, and delete owned claims.
The sandbox image must provide `agent-runtime serve`, sandboxd 1.0.3, and OpenCode V2.
The Docker default pins `@opencode/cli` 2.0.26, also tracked in `upstream.json`.
Overriding `OPENCODE_CHANNEL` requires checking that release's native API first.

Set these worker variables:

| Variable | Purpose |
| --- | --- |
| `AGENT_DEFINITIONS_DIR` | Trusted source directory, default `/config` |
| `AGENT_SNAPSHOT_DIR` | Shared durable snapshot store, default `/state/definitions` |
| `AGENT_ARTIFACT_DIR` | Durable native session export directory, required for structured agents |
| `AGENT_SECRET_KEY_FILE` | Stable runtime key file, default `/secrets/runtime-key`, at least 32 bytes |
| `AGENT_REVIEW_DATABASE_URL` | PostgreSQL DSN for durable review state, required for automations |
| `GITHUB_APP_ID` | GitHub App ID |
| `GITHUB_APP_KEY_FILE` | Worker-only RSA PEM private key, default `/secrets/github-app.pem` |
| `GITHUB_REPOSITORIES_FILE` | Worker-owned repository ID to installation ID JSON allowlist |
| `GITHUB_WEBHOOK_SECRET_FILE` | Exact webhook HMAC secret bytes, default `/secrets/github-webhook` |
| `AGENT_WORKER_NAME` | Hatchet worker name, default `agent-runtime` |
| `AGENT_WORKER_SLOTS` | Normal and durable slots, default `4` |

Supply Hatchet's ordinary SDK client configuration, including `HATCHET_CLIENT_TOKEN`
and your self-hosted server/TLS addresses. This code uses the pinned Go SDK
`github.com/hatchet-dev/hatchet v0.109.10`, not APIs inferred from current unpinned docs.
It registers `agent-run` and one workflow per automation file.

The example grants the model provider access through `/secrets/openai-key`.
This provider key is distinct from the GitHub App key. Provider credentials are
late-bound per provision. The runtime writes upstream auth storage in a private
temporary home outside `/workspace`, strips internal auth transport data from
`opencode.json`, and starts OpenCode with a minimal environment. Do not put GitHub
secrets in profile `secret_files` or provider config. GitHub keys remain in the worker.

Use persistent volumes for snapshots and artifacts. Every worker that can service
retries must see old snapshots at the same configured root. Snapshots use atomic
exclusive hard links and directory fsync, so the volume must support those operations.
Mount the definitions read-only and the snapshot store writable only by trusted workers.
Do not garbage-collect snapshots while queued, running, or replayable runs reference them.
Changing the current catalog does not revoke a pinned profile. To revoke historical
behavior, cancel affected runs and restrict their snapshot/credential access explicitly.

The database role needs permission to create `githubreview_records` on first startup
and read/update it afterward. Use a separate runtime database or schema, not Hatchet's
internal tables. Review state has no credentials or diff content. Back up this table
alongside snapshot and artifact storage. Startup fails rather than falling back to
process-local deduplication or an ambient PostgreSQL connection.

If using Tailscale, preserve the existing deployment ownership configuration.
Set `TAILSCALE_CLIENT_ID`, `TAILSCALE_CLIENT_SECRET_FILE`, and `AGENT_DEPLOYMENT_ID`.
Tagged profiles also require a deployment ID. See [tailnet ownership](../tailnet/README.md).
The checked-in profile has no tags and does not require a tailnet.

Run the worker after its trusted configuration and mounts are ready:

```sh
agent-runtime worker
```

## Configure a GitHub App and ingress

Create or configure your GitHub App yourself. This repository does not register
an App, create public tunnels, deploy infrastructure, or install the App automatically.

1. Grant repository Pull requests read/write. GitHub includes Metadata read.
   The diff-only handler does not request Contents access. The worker requests
   repository-scoped installation tokens and
   narrows Pull requests to read for resolution and write for publication.
2. Install the App only on approved repositories. Store repository ID to installation
   ID bindings in a worker-owned JSON file, using `examples/github-repositories.json`
   as the shape. Replace the example IDs with actual IDs.
3. Mount the App RSA PEM key only in the worker. Set `GITHUB_APP_ID` and key path.
4. Generate a webhook secret and save the exact bytes without a trailing newline.
   Configure the same value in GitHub and the worker's secret file.
5. Subscribe to Pull request events. Set the webhook URL to your TLS ingress path
   `/webhooks/github-pr-review`. Forward it to worker port 9091. This port also exposes
   `/metrics`; route only the webhook path publicly. Apply rate limits and TLS at ingress.
6. Select JSON payloads in GitHub. The handler requires `X-GitHub-Event`,
   `X-GitHub-Delivery`, and `X-Hub-Signature-256`. The configured actions are opened,
   synchronize, and ready_for_review. Unsupported actions do not enqueue jobs.

The adapter limits bodies to 1 MiB, verifies HMAC-SHA256 over the received bytes,
checks the installation/repository allowlist, and normalizes typed identity before
submitting to Hatchet. It returns success only after enqueue succeeds. A lost webhook
acknowledgment can cause another job, but durable revision deduplication prevents
another completed review. GitHub redelivery is the recovery path for failed ingress.

Hatchet v0.109.10 also supports native HMAC webhooks, the `GITHUB` source, and
GitHub event keys such as `github:pull_request:opened`. This implementation does not
claim native ingestion is unavailable. The small ingress adapter adds a bound on
body size, deployment allowlisting, and strict normalized input before enqueue.
The automation's `trigger.adapter` chooses this worker-owned adapter, not a Hatchet
event subscription. Do not also connect a raw native GitHub event to this workflow;
its input is the normalized envelope, not GitHub's raw payload.

## Invoke manually

Use the same trusted definitions directory as the deployed worker and Hatchet client
configuration. Copy `examples/review.json`, then replace every ID, repository name,
PR number, base SHA, and head SHA with canonical values. SHA fields use 40 lowercase
hexadecimal characters. A delivery ID identifies receipt, not the deduplication key.

```sh
export AGENT_DEFINITIONS_DIR=/path/to/trusted/definitions
agent-runtime agents validate
agent-runtime run github-pr-review --input review.json
```

Manual and webhook submissions both call `SubmitInput` with the same automation
name, normalized review, definition digest, and PR concurrency key. The handler
re-fetches canonical GitHub data for either path. Payloads cannot choose commands,
provider configuration, deployment secret paths, or repositories outside the allowlist.

For a plain agent, create `prompt.json` with `{"agent":"name","prompt":"your task"}`
and run `agent-runtime run agent-run --input prompt.json`. Schema-bearing agents
must return standalone JSON. Agents without a schema keep free-form session exports.

## Run identity, retries, and publication

The CLI and ingress select the resolved SHA-256 digest at enqueue. Workers save
content-addressed snapshots before accepting work. A `resolve` task reopens that
snapshot and persists its digest, typed canonical domain input, and pinned diff.
Provision, execute, collect, and publish never reread mutable authoring files.
Snapshots include instructions, skills, schema, model, timeout, deployment profile,
credential binding paths, and compiler-policy version. They exclude credential values.
New provisions read the current provider key without changing authored behavior.
Initialized sessions retain their original bootstrap credential. A provision replay
across credential rotation can fail the runtime's existing assignment-digest check;
do not rotate away an active session's credential without planning for that failure.
The handler implementation is trusted worker code and is not archived in the snapshot.
Keep compatible handler/compiler versions deployed until their runs drain.

The graph is resolve, provision, durable execute, collect/persist/validate, publish,
cleanup. Generic runs omit publication. Provision and execute have no automatic task
retries; their lifecycle can be explicitly replayed with the original digest. Artifact
collection, publication, and cleanup each have two retries. Native stable session and
message IDs preserve execution recovery. Durable interaction waits retain the existing
deadline checks and release Hatchet execution slots while waiting.

Hatchet queues one active workflow per repository ID/PR number concurrency group.
The worker checks that the supplied group key matches normalized identity. PostgreSQL
session advisory locks additionally serialize review-state transitions and publication
per PR across workers. Records use repository ID, PR number, head SHA, and agent digest
as the semantic key. Duplicate deliveries or manual submissions skip a completed key.
Interrupted resolution records do not permanently block a new run.

Collection exports the native session before output handling. The parser selects a
completed final assistant message for the run's prompt, excludes reasoning/tool text,
and rejects ambiguous, incomplete, fenced, or oversized output. The authored JSON
Schema and trusted review validator require a bounded summary/findings object with
safe paths and added right-side lines from the pinned patches. The model never chooses
the review event; trusted code sends COMMENT with the reviewed commit ID.

Publication re-fetches eligibility and both SHAs, verifies that patches still match
the original diff, and rechecks immediately before posting. Closed, draft, or obsolete
runs skip publication. A stable HTML marker identifies a review, and reconciliation
accepts only the same App's bot identity on the reviewed commit. It persists publishing
intent before POST. After a lost response or a failed completion save, a retry finds the
existing review and records its ID rather than posting again. If an intent remains but
GitHub does not expose a matching review, the worker fails closed. An operator must
investigate before resetting that record; there is no automatic blind repost.

Cleanup preserves the existing failed-session export behavior. Artifact export failures
retain the sandbox until lease expiry. Completed exports permit cleanup even when
publication fails. Cleanup failures report pending cleanup and remain bounded by the
lease/reaper. Worker GitHub App keys and installation tokens never enter Hatchet task
inputs, outputs, review state, or session artifacts. Provider auth is separate from
the native session export. Private repository patches and model output do enter Hatchet task
outputs and artifact storage; secure those stores and configure retention accordingly.

GitHub offers no atomic compare-current-head-and-create-review operation. A head update
between the final check and POST can still race publication. The review is pinned to
the old commit and cannot claim to review the new revision. No exactly-once distributed
transaction spans GitHub and PostgreSQL. Publishing intent, App-marker reconciliation,
and fail-closed uncertainty prevent automatic duplicate posts, at the cost of operator
recovery for unresolved outcomes. PostgreSQL must retain its records and advisory-lock
session semantics. Transaction-pooling proxies are unsuitable for this store.

## Migrate the old JSON configuration

`AGENT_DEFINITIONS_FILE` is removed and produces an explicit startup error if set.
There are not two competing authoring systems.

1. Move each JSON map entry to `agents/<name>/agent.yaml` and `instructions.md`.
2. Move model selection into `model.provider` and `model.id`; remove the old selector.
3. Move pool, namespace, directory, tags, provider config, and provider credential-file
   bindings into a named profile in `deployment.yaml`.
4. Replace `timeout_seconds` with `execution.timeout_seconds` and select the profile.
5. Remove `allow_project_config`, raw agent config, and arbitrary `prepare` argv.
   This system does not run repository setup scripts. Implement future trusted preparation
   through reviewed Go code, not task-supplied commands.
6. Add an automation file only for the registered `github.pr-review` handler.
7. Set the directory, shared snapshot storage, artifact storage, and database variables.
   Unset `AGENT_DEFINITIONS_FILE`, validate, and deploy before enqueueing new digests.

The Go bridge's `Build` replaces static-map registration. `Submit` requires a digest;
`SubmitInput` handles named workflows. Application callers must migrate too.

## Verify before production

```sh
go test ./...
go test -race ./definitions ./githubreview ./orchestration ./hatchetbridge ./command ./runtime
go vet ./...
go build ./cmd/agent-runtime
AGENT_DEFINITIONS_DIR="$PWD/examples/definitions" go run ./cmd/agent-runtime agents validate
```

`githubreview` includes fake GitHub HTTP/domain tests and optional real PostgreSQL
tests. Set `GITHUBREVIEW_TEST_POSTGRES_DSN` to a disposable local database to run the
store integration test. Do not point tests at production. See the test's environment
variable declaration if your checkout changes the test harness.

Install the pinned `@opencode/cli@2.0.26` through your approved package installation
process, then run the opt-in native integration test with its executable path:

```sh
AGENT_RUNTIME_TEST_OPENCODE_BINARY=/absolute/path/to/opencode2 \
  go test -race ./command -run '^TestOpenCodeNativeIntegration$' -count=1 -v
```

This test needs an unused local port 4096. It starts the real supervisor and OpenCode
with a local fake OpenAI-compatible provider, not a paid model or external service.
It checks credential bootstrap, file-only configuration, selected agent/model,
authored instructions and skills, status, native final output, and JSON Schema.
It also plants hostile project configuration, checks that it is ignored, and checks
that the provider receives no advertised tools or worker credentials. It does not
claim to exercise forced adversarial tool-call execution. The PostgreSQL and native
tests skip in the default suite when their opt-in environment variables are absent.

Local tests do not prove your GitHub App installation, Hatchet service, Kubernetes
pool/network policy, TLS ingress, or model credentials work. Validate those in staging
by opening a draft PR, making it ready, pushing another revision, redelivering an event,
and observing one COMMENT review per digest/revision with cleanup and retained artifacts.

## Primary-source checks

The layout borrows filesystem-first discovery from [Vercel Eve's agent-file reference](https://github.com/vercel/eve/blob/main/docs/reference/agent-files.md).
Eve actually uses `agent.ts`, `instructions.md`, and path-derived slots; its workspace
layout is `agents/<name>/agent/`. This runtime deliberately uses YAML and trusted Go
handlers rather than claiming Eve's TypeScript runtime or exact layout is compatible.

Pinned Hatchet evidence is in [the v0.109.10 webhook example](https://github.com/hatchet-dev/hatchet/blob/v0.109.10/sdks/go/examples/webhooks/main.go),
the SDK workflow options, and generated REST webhook types. Operational background is
in [Hatchet's Go webhook client reference](https://docs.hatchet.run/reference/go/feature-clients/webhooks)
and [GitHub webhook cookbook](https://docs.onhatchet.run/cookbooks/webhooks-github).
OpenCode configuration follows [V2 agents](https://dev.opencode.ai/v2/docs/agents/),
not legacy V1 fields. Native output parsing follows V2 message pagination and completion
metadata; see `orchestration/output_test.go` for checked fixtures and rejection cases.
The tested release is [OpenCode commit 9b4ec5714d481559990db0a816d5dec19541a814](https://github.com/anomalyco/opencode/tree/9b4ec5714d481559990db0a816d5dec19541a814).
Its captured native OpenAPI requires `model: {providerID, id}`, not legacy `modelID`.
