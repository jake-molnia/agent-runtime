# Connect an existing GitHub App through Vault

Vault owns the GitHub App private key and webhook secret. App registration,
installation, secret provisioning, rotation, Vault authentication configuration,
and Vault policy changes happen outside this runtime. The runtime retrieves
existing secret values. It never creates an App, writes or deletes Vault secrets,
performs manifest credential exchange, or stores App credentials on disk.

The integration supports HashiCorp Vault KV v2. It uses only
`GET /v1/<mount>/data/<path>` and selects a string field. KV v1, Transit signing,
inline credentials, and managed authentication/renewal are not supported.

## Configure public identity and secret references

Copy `examples/github.yaml` into your trusted worker configuration and set
`GITHUB_CONNECTION_FILE` to its path. The default is `/config/github.yaml`.
The file contains the public App ID, Vault address, authentication mode, and
references. It must contain no private key, webhook secret, or inline Vault token.
Unknown YAML fields, duplicate keys, multiple documents, and unsafe references
are rejected.

```yaml
github:
  app_id: 12345
  credentials:
    provider: vault
    private_key_ref:
      mount: secret
      path: github/review-app/private-key
      field: value
    webhook_secret_ref:
      mount: secret
      path: github/review-app/webhook-secret
      field: value
vault:
  address: http://127.0.0.1:8100
  auth: proxy
```

Use logical KV paths without `/v1/` or `/data/`. `mount` and `path` support slash-
separated alphanumeric, underscore, hyphen, and dot segments. Empty segments,
traversal segments, percent escapes, query strings, and backslashes are forbidden.
`field` is a single segment. Values must be nonempty strings. Deleted, destroyed,
missing, malformed, and oversized responses fail closed.

Keep repository-to-installation bindings in `GITHUB_REPOSITORIES_FILE`. These
bindings, the App ID, and workflow eligibility are non-secret configuration.
Credential references belong to the worker connection, not agent instructions,
task inputs, or definition snapshots.

## Let external infrastructure authenticate

Choose one mode. The runtime never calls a Vault login or renewal endpoint.

### Local auto-auth proxy

Set `auth: proxy` and point `address` at a loopback Vault Proxy or Vault Agent
API proxy. Configure that external process to authenticate with the workload's
identity and inject its auto-auth token. The runtime sends no `X-Vault-Token`.
The proxy must be inaccessible to untrusted workloads and agent sandboxes.

Configure token injection in the external proxy, for example with
`api_proxy.use_auto_auth_token = "force"`. Verify the exact configuration against
the version you deploy. Manage proxy identity, policies, caching, and renewals in
your infrastructure, not in this repository's worker.

### Externally managed token sink

Alternatively, let Vault Agent authenticate and write a token sink mounted only
into the trusted worker. Change the `vault` section:

```yaml
vault:
  address: https://vault.internal:8200
  auth: token-file
  token_file: /run/vault/token
  namespace: production
  ca_file: /etc/vault/ca.pem
```

`namespace` and `ca_file` are optional. The worker rereads the token file for each
read and does not write or renew it. Namespace selection uses `X-Vault-Namespace`.
The runtime does not use `VAULT_TOKEN` or ambient Vault configuration as a fallback.

Both modes verify TLS using system roots and an optional externally supplied CA.
Plain HTTP is permitted only for loopback addresses. Proxy mode requires a
loopback target. Redirects are refused and ambient HTTP proxy settings are not
used. Reads have a ten-second timeout, honor request cancellation, and bound the
response body to 1 MiB.

## Grant read-only access outside the app

Ask the Vault administrator to bind the external workload identity to an
appropriately scoped read-only policy. `examples/github-vault-policy.hcl` shows
the two exact KV v2 data paths required by the example. It grants `read`, not
`create`, `update`, `delete`, `list`, or administrative access. This runtime does
not apply that policy.

Vault grants access at the secret path, not individual fields. Use separate paths
for the key and webhook secret when that separation matters. The proxy or token
must not have broader secret-management permissions just because the reader
itself only issues GET requests.

Keep the App private key in unencrypted PKCS#1 or PKCS#8 RSA PEM form in the
selected Vault string field. RSA keys must be valid and at least 2048 bits.
Keep the webhook secret's exact bytes in its string field. No whitespace is
trimmed; a newline is part of the signing secret. Its size must be 1 to 1024 bytes.

## Runtime behavior and external rotation

Worker startup checks both referenced values before accepting automation jobs.
It checks key encoding and validity, not whether GitHub currently grants every
desired permission. Existing GitHub API checks still verify installation and
repository identity during execution.

Each App JWT signing operation rereads the private-key source. Each webhook
verification rereads the webhook-secret source. Values are used only in trusted
worker memory, with no in-process secret cache or persistence. Only public
references remain in connection configuration. Short-lived GitHub installation
tokens are minted as needed and remain worker-only.

External rotation becomes visible on subsequent reads without restarting the
worker. If your external proxy caches static secrets, its refresh policy also
determines when new values become visible. Rotation coordination between Vault
and GitHub is the operator's responsibility. The runtime does not accept a
previous webhook-secret version as a grace-period fallback.

If Vault rejects or cannot serve a read, no fallback credential is used. A
webhook that cannot obtain its verification secret returns HTTP 503 without
enqueueing work. Signing fails before the dependent GitHub request. Error
responses do not include secret values, Vault bodies, raw parsing diagnostics,
or authentication tokens. Use externally managed Vault audit logs to diagnose
permission or path failures.

Fetched values are absent from Hatchet payloads, definition snapshots, model
configuration, agent messages, and runtime-written credential files. Do not
mount the worker's Vault token, proxy endpoint, or configuration into an agent
sandbox. Enforce this with deployment/network policy; prompt instructions are
not an isolation mechanism. Go memory is garbage-collected, not guaranteed to
be zeroized.

## Migrate existing worker configuration

1. Have the administrator provision the existing App credentials in Vault.
2. Configure the external authentication proxy or token sink and its policy.
3. Create the public connection YAML and set `GITHUB_CONNECTION_FILE`.
4. Keep the existing repository allowlist and webhook ingress configuration.
5. Remove `GITHUB_APP_ID`, `GITHUB_APP_KEY_FILE`, and
   `GITHUB_WEBHOOK_SECRET_FILE` from the worker deployment. These variables no
   longer configure App credentials. There is no file-secret fallback.
6. Restart the worker and verify a webhook and a scoped GitHub operation.

This changes only the GitHub App credential path. Existing model-provider or
other runtime secret bindings are not migrated by this integration.

## Verify the code

```sh
go test ./vault ./githubreview ./command
go test -race ./vault ./githubreview ./command
go test ./...
go vet ./...
```

Tests use isolated HTTP/TLS fixtures for Vault reads and exercise external token
and secret changes, path restrictions, TLS verification, redirect refusal,
permission failures, cancellation, key encodings, JWT signing, and webhook
verification. They assert no Vault writes and no local App-secret persistence.
They do not establish your production Vault role, GitHub installation, or ingress
configuration.

An optional integration test uses externally provisioned fixtures in a disposable
Vault KV v2 server. Provision a valid RSA PEM key and the webhook value
`integration-webhook-secret`, configure a read-only token or local proxy, and use
repository mapping `{"11":7}`. Then run:

```sh
GITHUB_VAULT_TEST_CONFIG=/path/to/fixture/github.yaml \
GITHUB_VAULT_TEST_REPOSITORIES_FILE=/path/to/fixture/repositories.json \
go test ./command -run '^TestGitHubVaultIntegration$' -count=1 -v
```

The test only reads the preexisting fixture values and verifies a signed webhook.
It neither provisions secrets nor contacts GitHub. Do not point this fixture test
at production App credentials. It has been exercised against a disposable Vault
1.21.0 server with a read-only policy; an independent write attempt was rejected
with HTTP 403.

Primary references:

- https://developer.hashicorp.com/vault/api-docs/secret/kv/kv-v2
- https://developer.hashicorp.com/vault/docs/agent-and-proxy/agent/apiproxy
- https://developer.hashicorp.com/vault/docs/agent-and-proxy/autoauth/sinks/file
