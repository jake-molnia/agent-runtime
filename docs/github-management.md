# Configure GitHub review management

GitHub App credentials already have an external delivery mechanism in
`jake-molnia/config`: Vault -> External Secrets Operator -> the `github-app`
Kubernetes Secret -> worker environment/mounted files. This runtime reuses
`GITHUB_APP_ID`, `GITHUB_APP_KEY_FILE`, and `GITHUB_WEBHOOK_SECRET_FILE`.
There is no Vault client, secret creation, credential exchange, or secret renewal
in this feature. Add a webhook-secret mapping to the existing ExternalSecret in
the deployment repository if needed; its value remains externally managed.

Production connection, enrollment, and automation files belong in that external
configuration repository. The checked-in examples are samples, not production
policy. App registration and GitHub's installation/repository access consent
remain in GitHub. Runtime enrollment enables an already installed repository;
it cannot install the App or grant GitHub permissions.

## Connect and inspect the existing App

Configure the trusted worker's externally supplied identity and file bindings,
then run:

```sh
agent-runtime github check
agent-runtime github repositories discover
```

`check` verifies the App identity and Pull requests write permission through
GitHub's API and prints public metadata, permissions, and event subscriptions.
`discover` lists accessible repositories with their canonical repository and
installation IDs, skipping suspended installations. Discovery uses metadata-only
installation tokens. Neither command prints keys or tokens. Subscribe the App
to Pull request events in GitHub; action selection is configured separately below.

## Enroll or disable repositories

`GITHUB_REPOSITORIES_FILE` is the trusted non-secret deployment allowlist, mapping
repository IDs to installation IDs. It is the maximum access boundary for all
automations. Start a new local source file with `{}`; an empty map enables no
repositories. Use discovered names to resolve IDs or supply explicit IDs:

```sh
agent-runtime github repositories list --file repositories.json
agent-runtime github repositories enroll --file repositories.json --repository acme/api
agent-runtime github repositories enroll --file repositories.json --repository-id 67890 --installation-id 12345 --write
agent-runtime github repositories disable --file repositories.json --repository-id 67890 --write
```

Edits print the proposed JSON by default. `--write` atomically updates only the
specified local file; use it on a writable GitOps source, not the mounted
ConfigMap. Commit and deploy that file through your existing configuration flow.
Explicit IDs are configuration declarations, not verified installation claims;
canonical identity and access are checked again when a run resolves the PR.

New enrollments require worker reload/restart to update its ingress/client
allowlist. Removal is also checked by rereading the mounted enrollment file at
resolve and publish, including immediately before posting, so queued jobs cannot
continue after the deployed allowlist revokes them. Missing/invalid enrollment
configuration fails closed. Automations can only narrow this allowlist.

## Set per-automation selection

Copy `examples/github-pr-review.selection.yaml` into the separately opt-in
`github-automations/` directory as `github-pr-review.yaml`, or select its directory
using `GITHUB_AUTOMATIONS_DIR`. Generic `workflows/` and agent defaults remain
independent; no GitHub adapter is registered unless explicitly authored. Select
a structured agent from your external catalog in the manifest; the runtime does
not ship a production `github-reviewer`. All fields from the proposed selection
configuration are supported: repository include/exclude, base branches, draft
eligibility, required/excluded labels, excluded authors, and changed paths.

Rules have these semantics:

- All configured categories must match. Exclusions win.
- Repository names, authors, and labels use case-insensitive exact matching.
- Branch/path globs support `*`, `**`, and `?`. `*` does not cross `/`; `**` is a
  whole segment and crosses directories. `**/*.md` matches root-level Markdown
  too. Character classes and brace expansion are rejected, not ignored.
- At least one changed path must survive the include/exclude rules. A mixed
  code/docs PR runs if an eligible code path remains.
- Path selection controls whether to run, not what context is supplied to the
  reviewer. The full pinned diff remains available for context and validation.
- Drafts default to excluded. `drafts: true` explicitly allows open draft PRs.
- Closed PRs never run. Omitted optional filters add no restriction.
- Omitted `policy` defaults to per-PR concurrency 1 and reviewed-revision
  deduplication. Explicit unsupported policy values fail validation.

Triggers support `opened`, `reopened`, `synchronize`, `ready_for_review`,
`converted_to_draft`, `labeled`, `unlabeled`, and `edited`. Include the relevant
actions when labels, draft state, or base branches control eligibility. A metadata
change does not duplicate an already published review for the same automation,
revision, and policy identity.

## Inspect and explain

```sh
agent-runtime agents validate
agent-runtime automations validate
agent-runtime automations list
agent-runtime automations inspect github-pr-review
agent-runtime automations explain github-pr-review --input facts.json
```

`examples/github-pr-facts.json` demonstrates the offline facts shape. `explain`
checks those supplied PR facts and the current repository enrollment, returning
`eligible` and a reason such as `excluded_repository`, `missing_required_label`,
`excluded_author`, `base_branch_not_selected`, `no_matching_changed_paths`, or
`repository_not_enrolled`. This offline preview does not authenticate the facts
or execute a model. The worker fetches canonical facts itself.

Both webhook and manual automation submissions use the same trusted resolve
handler. The handler evaluates current GitHub metadata before sandbox provisioning
and records the skip reason in its resolved output. Publication re-fetches
metadata and files and checks both the pinned run policy and the active worker's
policy. If either forbids posting, nothing is posted. The final pre-post check
also verifies revision, eligibility, and enrollment. GitHub has no atomic
conditional review API tying a post to unchanged labels, so a tiny change-after-
check race remains; the review is still bound to the pinned commit.

Automation name and selection are included in the review identity to prevent
one workflow's completed review suppressing another workflow. Pinned selection
travels with the run; it is not provided by model output. Definition changes
still require deployment/worker reload; this feature is not a hot-reload service.

## Verify

```sh
go test ./githubreview ./definitions ./command
go test -race ./githubreview ./definitions ./command
go test ./...
go vet ./...
```

Tests cover the optional adapter's complete selection YAML, glob semantics, mixed-path eligibility,
label-trigger actions, canonical metadata, draft opt-in, policy-scoped dedup,
publication-time changes, enrollment revocation, public discovery, and CLI edits.
No production Vault or GitHub configuration is modified by those tests.
