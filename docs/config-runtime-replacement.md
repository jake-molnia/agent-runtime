# Config worker replacement status

The runtime now includes an optional trusted PR integration, an Aperture-managed
MCP catalog mode, and pinned engineering skills in its GHCR images. Production
replacement still requires config-owned deployment changes and a staging run.
It is not yet an image-only swap.

The baseline comparison used config `main` at
[`a1cc652`](https://github.com/jake-molnia/config/tree/a1cc652afa6e995a2e714277b361e371a83137c0)
and agent-runtime `48026c40886c0cd991ac6e8979dfe68ed60cead3` on 2026-10-09.
The additions below are in the current working branch. Config findings describe
checked-in desired state, not live-cluster observations.

## Runtime support added

The [optional PR integration](github-review.md) provides event normalization,
repository/installation authorization, 30-day admission deduplication, per-PR
concurrency, required placement for manual/signed-webhook submissions, durable
execution of a config-owned graph, trusted source-index reduction, and publication.
It reuses the existing GitHub App client and PostgreSQL publication store, including
stale-head checks and uncertain-write reconciliation. Generic workers remain
independent of GitHub and PostgreSQL.

The adapter reads actual verifier/adversarial step messages and preserves the old
config reducer's source coverage, source location, and priority rules. It does not
copy production prompts or model choices into the runtime. A complete portable
example lives in [examples/github-review](../examples/github-review).

Ephemeral, single-use, preauthorized Tailscale identities already existed.
Deployment profiles supply their tags. The sandbox's local Aperture proxy sends
requests through that sandbox identity. `tool_policy: broker_catalog` lets
Aperture control the tools exposed by a selected MCP server; exact local tool
lists remain available. Native shell/file tools stay separate from broker grants.

The images now contain [pstack and Matt Pocock engineering skills](sandbox-skills.md).
They do not contain Nix or a T3-specific skill collection. Both image targets have
[versioned and main-push GHCR publication](releases.md) without Depot.

## Config-owned work still required

1. **Agent and broker configuration.** Port production prompts, schemas, model
   settings, repository mappings, and publish policy into the new format. Register
   repository investigation/verification backends with Aperture and grant access
   through the sandbox tags. Registering a backend does not implement checkout or
   test execution by itself. The runtime no longer assumes the old local Nix
   environment or unrestricted shell. Repository-specific profiles must preserve
   the old tag isolation.
2. **Worker storage and credentials.** Materialize trusted config as regular files;
   projected ConfigMap symlinks are rejected by the agent loader. Mount the stable
   runtime key and persistent definitions, messages, and artifacts. Workers eligible
   for the same runs need the same durable data. Mount App/database credentials
   only when enabling the PR integration. The old worker's temporary directory and
   read-only root filesystem alone are insufficient.
3. **Services, probes, and networking.** The old sandbox templates use Kubernetes
   exec, have no service configuration, deny all ingress, and probe
   `.agent-home-ready`. The new runtime requires `status.serviceFQDN`, supervisor
   port 8081, and OpenCode port 4096, plus sandboxd on 8080/9090. Update both sandbox
   ingress and worker egress for trusted worker access. The old worker probes
   `/health` on 8001; the Go worker's 9091 metrics/webhook server does not yet provide
   equivalent scheduler-aware health probes.
4. **Ownership and compatibility.** Configure distinct `AGENT_DEPLOYMENT_ID` values
   for Portal and Gompers, `APERTURE_UPSTREAM`, provider bindings, and `/workspace`.
   Add claim patch permission for expiry and sandbox patch permission if using
   suspend/resume. Verify Hatchet server v0.107.0 against Go SDK v0.109.10, especially
   durable children, cancellation, replay, and idempotency. Native event registration
   cannot require worker labels in this SDK; use signed-webhook submission for
   explicit placement. Manual model/effort overrides use configured plans.
5. **Flux release sources.** Config currently consumes Depot OCI manifest bundles
   `agent-release-latest` and `agent-tasks-release-latest`. Runnable GHCR images
   do not replace those bundles. Use config Git sources with paired GHCR image
   digests, or separately move the manifest publisher to GHCR. Change private image
   pull credentials. Remove old Depot agent pipelines/sources only as part of that
   reviewed config migration; unrelated Depot workloads are outside this change.

Deployment evidence comes from config's
[`worker.yaml`](https://github.com/jake-molnia/config/blob/a1cc652afa6e995a2e714277b361e371a83137c0/clusters/shared/agent-workers/worker.yaml),
[`template-tailnet.yaml`](https://github.com/jake-molnia/config/blob/a1cc652afa6e995a2e714277b361e371a83137c0/clusters/shared/agent-environments/template-tailnet.yaml),
[`network-policy.yaml`](https://github.com/jake-molnia/config/blob/a1cc652afa6e995a2e714277b361e371a83137c0/clusters/shared/agent-infrastructure/network-policy.yaml),
and Portal/Gompers worker infrastructure overlays. The old business behavior is in
[`workflow.py`](https://github.com/jake-molnia/config/blob/a1cc652afa6e995a2e714277b361e371a83137c0/packages/agent-tasks/agent_tasks/workflows/pr_review/workflow.py).

## Cutover acceptance

Publish the paired images, create a separate worker/pool/workflow name in config,
and complete a generic run with real Hatchet, provider, and sandbox credentials.
Then compare the same PR through both workers in dry-run mode. Check exact revision
access, source reduction, stale-head rejection, duplicate delivery, cancellation,
restart/replay, artifact retention, and claim cleanup.

Drain old `pr-review` runs, including the legacy `judge` alias, before switching
production routing. The new graph cannot resume Python task records. Stop the old
Tailscale reaper before sharing production devices: it deletes tagged devices
older than 45 minutes without the new deployment scope. Use isolated staging
identities/policy or change that ownership logic for side-by-side trials.

Apply production changes through the config repo's Flux process. Keep previous
images/manifests and durable state for rollback. Verify reconciliation and
application behavior separately. Local unit/native tests do not establish live
Hatchet, PostgreSQL, GitHub, Tailscale, or Aperture compatibility.
