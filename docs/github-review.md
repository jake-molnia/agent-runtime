# Configure trusted PR reviews

The optional GitHub integration wraps a config-owned agent workflow with trusted
event handling, per-PR scheduling, source validation, and publication. Generic
workers do not require a GitHub App or database. The integration activates when
`integrations/github-review.yaml` exists under `AGENT_DEFINITIONS_DIR`, or when
`AGENT_GITHUB_REVIEW_CONFIG` points to a file.

The [complete example](../examples/github-review) includes a review/verify/
adversarial/writeup graph, output schemas, Aperture profiles, and a dry-run request.
Copy and customize these files in your config repo. The example repository and
installation IDs are placeholders, and publication is disabled.

## Select the graph and repository policy

```yaml
version: 1
name: example-pr-review
workflow: review-agents
candidate_steps: [verify, adversarial]
writeup_step: writeup
actions: [opened, synchronize, reopened, ready_for_review]
native_events: false
worker_labels: {cluster: staging}
repositories:
  12345:
    name: owner/repository
    installation_id: 67890
    publish: false
```

`workflow` names an existing YAML graph. `candidate_steps` contains one or two
step IDs in concatenation order. Each returns `summary`, `findings`, and
`limitations`. Findings carry `priority`, `path`, `line`, `title`, and `explanation`.
`writeup_step` returns `assessment` and finding groups with `sources`, `title`,
and `explanation`. The schemas in the example define the bounds.

The writer must cover every source index exactly once. Trusted code reads the
actual candidate step messages, verifies their workflow/agent/reply identities,
and retains the first source's location and highest priority in each group.
It posts findings on context or added lines inline. Other locations, missing
patches, and truncated patches become links in the review body. Large review
bodies are split into persisted, individually reconciled parts. Candidate arrays
copied into writer output do not become authoritative evidence.

A stable ingress applies 30-day admission deduplication and serializes each PR.
It dispatches to an immutable revision-specific adapter with required worker
labels. The adapter resolves current PR identity, runs the configured graph as
a durable child, reduces sources, and publishes. Original workflow names remain
the CLI interface; scheduler action names include revision identity.

## Mount integration credentials

Set these variables only on workers running the integration:

- `GITHUB_APP_ID`: the App ID.
- `GITHUB_APP_PRIVATE_KEY_FILE`: a mounted RSA PEM key, PKCS#1 or PKCS#8.
- `GITHUB_REVIEW_DATABASE_URL_FILE`: a file containing the PostgreSQL connection URL.
- `GITHUB_WEBHOOK_SECRET_FILE`: optional HMAC secret file enabling
  `POST /webhooks/github` on port 9091.

The existing publication store initializes its table and uses PostgreSQL advisory
locks. App signing keys and publication credentials stay on the worker. Before
each agent starts, the worker uses a short-lived repository-scoped contents-read
token to fetch the exact base and head into that profile's `directory`. The agent
starts at detached HEAD. Temporary askpass files are removed before execution;
credentials are absent from Git configuration and workflow input.

Set the profile directory to an empty path such as `/workspace/checkout`. Agents
receive factual PR identity as `{"review": ..., "prompt": ...}` and investigate
through native tools in the checkout. The base is refreshed when the review
resolves. Later base-branch movement does not invalidate an unchanged PR head.

## Choose event ingress

For explicit worker placement, send signed GitHub webhooks to `/webhooks/github`.
The integration verifies the signature and repository installation, filters
draft/closed PRs before scheduling, and submits with required worker labels.
Configure the ingress and secret in the config repo.

Alternatively, set `native_events: true` to subscribe through Hatchet's existing
authenticated `github:pull_request:*` event ingress. The lightweight ingress
dispatches to the adapter with `worker_labels`, so native events retain explicit
placement. Native and signed-webhook routes share the same PR/head deduplication
identity. When deploying to multiple clusters, enable native subscriptions on
only the intended ingress or use an explicit event routing policy.

For a manual dry-run:

```sh
export AGENT_DEFINITIONS_DIR="$PWD/examples/github-review"
agent-runtime agents validate
agent-runtime workflows validate
agent-runtime reviews run examples/github-review/request.json
```

Use [the request shape](../examples/github-review/request.json) with actual
repository, installation, PR, base SHA, and head SHA values. Set a unique manual
`delivery_id` for a new run. `publish` defaults to false. Publication requires
both request intent and the current repository policy to permit it. The CLI needs
Hatchet credentials and integration configuration; App/database credentials are
needed by the worker. Arbitrary per-request model or effort overrides are not
accepted; choose trusted config-defined plans.

Repository policy and canonical head are checked before every stage and every
15 seconds during active work. Obsolete stages and their child runs are cancelled.
Out-of-order webhooks cannot blindly cancel a current review. Publication rechecks
policy and current head. Removed repositories, dry runs, and stale PRs do not
publish. Completion recognizes the previous config worker's review and issue-comment
markers and is keyed by PR head, independent of graph revision.

Publication persists the exact request plan and reconciles each part after a lost
response. An uncertain request is never blindly reposted. If GitHub never exposes
its result, the operator must investigate the durable record before retrying.
Admission deduplication and publication reconciliation remain separate checks.

## Keep production policy in config

Keep prompts, models, graph topology, repository allowlists, publish flags,
Tailscale tags, Aperture backends, secrets, storage, Kubernetes resources, and Flux
configuration in your config repo. This repository owns the reusable integration
and GHCR images, including the pinned skill bundle. It has no Depot dependency.

Use a separate workflow name and pool for staging. Revision-specific action
names prevent old tasks from landing on new incompatible workers. Retain old
workers until their runs drain; referenced snapshots, messages, and compatible
skill images must remain available. The v6 checked-in harness configuration migration itself
requires draining older compiler-policy runs with their original worker.
Test durable child cancellation/replay and TTL idempotency against your deployed
Hatchet version before cutting over. Unit tests cover SDK callback execution and
policy checks; they do not prove production server compatibility.

Worker health endpoints on port 9091 are `/healthz` for process liveness,
`/startupz` after the first confirmed scheduler registration, and `/readyz` for a
fresh ACTIVE Hatchet heartbeat belonging to this exact worker process.
