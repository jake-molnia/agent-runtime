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
It then validates locations against added right-side diff lines. Candidate arrays
copied into writer output do not become authoritative evidence.

The adapter uses a persisted resolve task, a durable child workflow, and a
trusted publish task. Hatchet serializes the entire parent run per PR and applies
30-day admission deduplication. This reuses Hatchet's scheduling and recovery
instead of adding a separate controller and submission outbox.

## Mount integration credentials

Set these variables only on workers running the integration:

- `GITHUB_APP_ID`: the App ID.
- `GITHUB_APP_PRIVATE_KEY_FILE`: a mounted RSA PEM key, PKCS#1 or PKCS#8.
- `GITHUB_REVIEW_DATABASE_URL_FILE`: a file containing the PostgreSQL connection URL.
- `GITHUB_WEBHOOK_SECRET_FILE`: optional HMAC secret file enabling
  `POST /webhooks/github` on port 9091.

The existing publication store initializes its table and uses PostgreSQL advisory
locks. GitHub credentials stay on the trusted worker. Agents receive canonical PR
identity and diff context as `{"review": ..., "prompt": ...}`.

## Choose event ingress

For explicit worker placement, send signed GitHub webhooks to `/webhooks/github`.
The integration verifies the signature and repository installation, filters
draft/closed PRs before scheduling, and submits with required worker labels.
Configure the ingress and secret in the config repo.

Alternatively, set `native_events: true` to subscribe through Hatchet's existing
authenticated `github:pull_request:*` event ingress. Omit `worker_labels` in that
mode: the pinned Go SDK cannot apply required placement labels to native event
registration. The loader rejects that combination. Native and signed-webhook
routes use the same PR/head deduplication identity.

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

Publication reloads repository policy and checks the current PR revision. Removed
repositories, dry runs, and stale PRs do not publish. The existing publisher
reconciles duplicate and uncertain writes before attempting another GitHub review.
Admission deduplication and publication reconciliation are separate checks.

## Keep production policy in config

Keep prompts, models, graph topology, repository allowlists, publish flags,
Tailscale tags, Aperture backends, secrets, storage, Kubernetes resources, and Flux
configuration in your config repo. This repository owns the reusable integration
and GHCR images, including the pinned skill bundle. It has no Depot dependency.

Use a separate workflow name and pool for staging. Drain runs before changing
integration configuration or graph topology, or retain compatible workers.
Persisted runs fail closed if the worker's integration/plan revision differs.
Test durable child cancellation/replay and TTL idempotency against your deployed
Hatchet version before cutting over. Unit tests cover SDK callback execution and
policy checks; they do not prove production server compatibility.
