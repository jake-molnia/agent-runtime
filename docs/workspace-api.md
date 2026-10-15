# Workspace lifecycle API

The lifecycle service manages retained workspaces and their sandbox compute.
Requests use JSON and the controller's bearer token. Internal tasks, operation
IDs, and lifecycle revisions are internal implementation details.

This API is separate from the T3 execution worker API. Worker commands and event
delivery continue through the authenticated execution gateway.

## Register a workspace

`PUT /v1/workspaces/{id}` binds a workspace to a configured pool and profile and
ensures its retained storage exists. Registration does not start compute.

```json
{"pool":"gompers","profile":"t3-v7"}
```

The response is the workspace resource. Repeating the same registration preserves
the binding and stored data. A conflicting binding fails. Pool selection happens
before the request: send it to the selected pool's controller.

Registration reserves the workspace ID atomically in a retained Kubernetes Lease
before creating storage. All replicas of a pool must use the same registry
namespace. A single profile namespace is the default. With profiles in multiple
namespaces, set `T3_WORKSPACE_REGISTRY_NAMESPACE` to one of them and retain that
choice across updates. The controller needs its existing Lease permissions in
that namespace. The registry record remains with the retained workspace; it is
not an execution lock and does not expire when compute stops.

## Request a state

`PUT /v1/workspaces/{id}/state` accepts a desired state:

```json
{"desiredState":"running"}
```

The supported values are `running`, `suspended`, and `released`. Suspension stops
execution while retaining the sandbox allocation and workspace storage. Release
removes compute and its execution credentials but retains workspace storage.
Neither operation deletes user files.

Send `If-Match: "<version>"` using the version returned by the latest workspace
read. The controller returns `412` for a stale version and `428` when the header
is missing. After a conflict or lost response, read the resource again and
reconsider the intent. Do not automatically replace a newer running request with
an old shutdown request.

Accepted transitions return `202` with the workspace resource and a `Location`
header for the workspace. An already satisfied request returns `200`. A repeated
request converges on the same desired state; internal task failures can be retried
without inventing a new caller-side operation ID. The controller reconciles
accepted intents from persistent state using Kubernetes leases; it does not
require Hatchet.

Normal shutdown still requires the worker to prove it has no active operations or
blocking callbacks. Changing the HTTP interface does not bypass that check.

## Read status

`GET /v1/workspaces/{id}` returns the workspace resource and its `ETag`. No profile
query parameter is required. The controller resolves the stored binding.

The resource includes:

- `id`, `pool`, and `profile`: workspace identity and placement.
- `desiredState`: the accepted target state.
- `state`: the observed lifecycle state, including transitional, failed, and
  unavailable states.
- `version`: an opaque conditional-update token. Do not parse it.
- `identity`: the worker identity, when known. A suspended workspace can retain
  its last identity without granting access.
- `progress`: the current lifecycle step and its start time, or `null`.
- `error`: a safe error code, message, and retryability flag, or `null`.
- `execution`: authenticated endpoint, token, and worker identity when execution
  is ready, otherwise `null`.

An unavailable worker does not make its workspace disappear. The resource remains
readable, with no execution credentials. Progress describes lifecycle phases;
it does not claim detailed container image download progress.

Workspace responses use `Cache-Control: no-store`. Treat execution tokens as
credentials and never include them in logs or diagnostics.

## Errors

Errors from the workspace endpoints have a structured body:

```json
{"error":{"code":"version_conflict","message":"Workspace version changed.","retryable":true}}
```

Use the HTTP status and error code to decide whether to reread, retry, or surface
a failure. The message is for display; callers must not parse it. A retryable
error does not authorize discarding a newer intent or bypassing worker identity
checks.

## Profile migration

Profile migration remains a management operation. `GET
/v1/workspaces/{id}/migration` provides the profile hash, revision, epoch, and state
needed by `POST /v1/profile-migrations`. Migration requires a completed compute
release and preserves the workspace's storage identity.

Each migration includes a stable `operationId` and the metadata response's
`bindingGeneration` as `expectedBindingGeneration`. A successful migration
increments that generation. Repeating the exact request is safe only while it
remains the latest migration; an intervening rollback invalidates old retries.
The migration tool stores this request before submitting it. Prepared legacy
ledgers without these fields must finish with their compatible old controller.

## Client migration

The lifecycle service no longer exposes `POST /v1/operations` or
`GET /v1/operations/{taskId}`. Its workspace GET no longer returns the internal
lifecycle state envelope. Update controller and T3 client together before
resuming lifecycle traffic. The execution worker's similarly named operation
endpoints are unchanged.
