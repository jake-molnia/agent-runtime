# Config worker replacement status

The runtime PR stack restores the reviewed behavior gaps and adds native root
execution in disposable sandboxes. The config migration is a separate suspended
draft. Publishing images and activating the deployment are separate steps.

The comparison used config `fbc7a7897a6a0154b9afacb517cc86ee65f7e3d3`
and runtime `114ead1430bc89273196f5ceb678e77666ea22ef`. The fixes below extend that
baseline. The config findings describe desired state, not a live-cluster inspection.

## Runtime fixes

- New agent snapshots allow all native tools, including shell, filesystem,
  execute, and subagents. The sandbox image runs as root. The worker remains
  non-root. Project configuration is suppressed to preserve the authored prompt
  and endpoint configuration, but agents can execute arbitrary sandbox code.
- The pinned output schema is supplied in trusted system instructions. PR context
  contains facts rather than a competing output-format instruction.
- Each review stage receives a prepared checkout at the resolved base and exact
  detached PR head. Repository-scoped contents-read credentials are transient.
  GitHub App signing keys and publication credentials stay on the worker.
- Publication accepts context-line comments and linked-body findings outside
  hunks, refreshes the base when resolving, and treats the PR head as completion
  identity. Config's legacy review and issue-comment markers prevent duplicates
  during cutover. Large bodies use durable plans and per-part reconciliation.
- Placement examines up to GitHub's 3,000 changed files; missing/truncated patches
  use body links. The runtime accepts 1 MiB final JSON, a separate 64 MiB transcript
  budget, 4 MiB aggregate graph input, and 8 MiB message/prompt envelopes.
- Policy/head guards run before every review stage and every 15 seconds during
  active work. Superseded or disabled runs cancel their contexts and child runs.
  Out-of-order events cannot blindly interrupt the current PR head.
- Stable ingress keeps admission identity while dispatching native, signed-webhook,
  and manual runs to immutable revision-specific adapters with worker labels.
  Child workflow action names also include the revision, so incompatible workers
  cannot execute saved old actions.
- Worker endpoints on port 9091 provide process liveness, latched startup, and
  scheduler-aware readiness using this process's ACTIVE record and fresh heartbeat.
- The images bundle pstack and Matt Pocock engineering skills. There is no Nix
  environment or T3-specific skill bundle. GHCR publishes versioned and main-push
  nightly images without a Depot dependency in this repository.

See [PR integration](github-review.md), [skills](sandbox-skills.md), and
[releases](releases.md) for configuration contracts.

## Config migration and activation

Production prompts, model settings, repository allowlists, Tailscale tags, secrets,
Kubernetes resources, storage, and Flux routing remain config-owned. The migration
moves agent manifests from Depot OCI bundles to the config Git source and updates
sandbox root/security context, per-sandbox service ports, worker network access,
probes, regular-file config materialization, RBAC, and persistent runtime state.
The optional publication database must be shared by workers that can publish
reviews for the same repositories.

Keep migration Flux Kustomizations suspended until the paired GHCR images are
published and pinned, required Vault values/roles exist, and the publication
schema/user/connectivity are ready. Suspending reconciliation preserves current
live resources but pauses updates; it is an explicit activation gate, not a
staging deployment or proof of application health.

## Cutover checks

Run a separate worker/pool/workflow name first. Compare dry-run reviews against
config, including exact checkout revisions, source reduction, context/fallback
placement, stale-head cancellation, duplicate delivery, restart/replay, artifact
retention, and claim cleanup. Exercise durable child cancellation and worker
placement against the actual Hatchet server. Do not infer compatibility solely
from SDK callback tests.

Drain old Python `pr-review` runs, including the legacy `judge` alias. Pre-v6
runtime snapshots must also drain with their original compiler/worker; they do
not silently gain unrestricted permissions. Retain old revision-specific workers
until their remaining runs finish, along with their snapshots, messages, and
matching skill images.

Stop the old unscoped Tailscale reaper before sharing production devices; it can
remove tagged devices after 45 minutes regardless of the new deployment scope.
Keep previous images/manifests and persistent data available for rollback. Verify
Flux reconciliation and application behavior separately.

An ambiguous GitHub write that never becomes visible remains conservatively
blocked. Investigate the durable publication record and GitHub state rather than
blindly resetting it and risking a duplicate. Manual model/effort choices use
config-defined plans instead of mutable overrides on an existing snapshot.
