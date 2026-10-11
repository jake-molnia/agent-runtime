# Build the T3 images

Build from this agent-runtime checkout and the T3 fork at the commit recorded in
[source.lock.json](source.lock.json). The source ref must be a full, committed SHA.
CI checks out that exact revision when building the web and worker images.

## Build on GitHub Actions

1. Set `ref` in `source.lock.json` to the T3 fork commit, or supply `t3_ref` when
   you run the **T3 images** workflow manually.
2. Run the workflow on the integration branch after both repositories are pushed.
3. Download the small verification artifacts, including each target's `published.txt`.
4. Pull the immutable GHCR reference from `published.txt` and use that digest in the deployment.


The workflow builds `t3-worker`, `t3-web`, and `t3-runtime` on Ubuntu for
`linux/amd64`. Each artifact includes image inspection output, its published digest, and both
source commits. After verification, it publishes immutable commit tags to GHCR. The web and control
images use the `agent-runtime-worker` package; the execution image uses
`agent-runtime-sandbox`. It does not deploy them. A push to `t3/sandbox-session-lifecycle` starts a build only when its
listed build inputs change.

## Build locally with Docker Buildx

From the agent-runtime checkout, supply the T3 checkout as a named build context:

```sh
docker buildx build --platform linux/amd64 \
  --build-context t3src=/path/to/t3code \
  --file images/t3/Dockerfile --target t3-worker \
  --tag t3-worker:local --load .
```

Repeat with `--target t3-web` and `--target t3-runtime`. The build installs the
T3 package from its frozen pnpm lockfile and creates a portable production
package with `pnpm deploy`.

The worker build runs every declared executable's version probe and boots the
packaged worker with disposable state. It verifies authenticated identity and
checks that the worker creates no conversation SQLite database. The web build
boots the bundled central application and requests its HTML over loopback. These
checks do not launch a browser or sign into providers. The workflow boots the exported worker and web images through their real
entrypoints with their deployment permissions: central web runs as UID 1000 with a
read-only root filesystem; execution workers run as root inside their isolated
sandbox with a writable root filesystem. Both drop capabilities,
and external networking disabled. Disposable writable mounts match the deployment
paths. It also checks that the worker rejects unauthenticated identity requests.

## Update a tool pin

1. Change the exact version in [providers.lock.json](providers.lock.json).
2. For npm harnesses, update the matching package manifest and regenerate that
   directory's `package-lock.json`.
3. For native downloads, verify the release source and record the downloaded
   artifact's SHA-256. Set the exact archive member for tar archives.
4. Build the worker target and inspect the version report and boot result.

The worker includes Codex, Claude Code, Grok, Pi, OpenCode, OpenCode 2, Muse, and
the Cursor SDK. Generic ACP extensions remain an explicit allowlist in the
manifest. Source-control tools are `gh`, `glab`, `tea`, `fj`, and Azure CLI with
the Azure DevOps extension. Go and pnpm support work on these repositories.
Azure CLI uses Microsoft's checksum-pinned Debian package with its bundled
Python. The Azure DevOps wheel is pinned separately, and dynamic extension
installation is disabled.

The T3 worker also includes code-server `4.141.0`, AIO `0.9.2`, and the shared
desktop tool installation from `desktop/install-tools.sh`. Code-server release
archives are checked against fixed SHA-256 hashes by `install-code-server.sh`.
The Go `t3-session` supervisor starts these services on loopback, then launches
the TypeScript execution worker with their addresses and desktop environment.
The real-entrypoint image smoke checks verify the IDE HTML, computer API, and
browser CDP endpoint before accepting the worker as ready. Jupyter is excluded.

## Check the Git credential helper

```sh
python3 images/t3/test_git_credential.py
```

This runs the helper through its stdin protocol and through native
`git credential fill` with a disposable home. It checks exact host and protocol
matching, non-mutating store/erase requests, malformed input, and token output.
Credentials come from the deployment profile, never from the image build.

## Validate the deployment inputs

```sh
go run ./images/t3/validate-profile.go deploy/t3/t3-profiles.json \
  deploy/t3/kustomization.yaml deploy/t3/namespaces.yaml \
  deploy/t3/runtime.yaml deploy/t3/web.yaml .github/workflows/t3-images.yml
```

This command parses the YAML and decodes the profile into the runtime's actual
Go types. It substitutes a valid storage-class name in memory for validation.
It does not apply Kubernetes resources or establish cluster compatibility.

Full OCI image builds require a working container builder. The current
restricted development container cannot create rootless BuildKit UID/GID
mappings. Local installer, executable, and package tests do not replace a
successful CI image build.

Deployment instructions are in [deploy/t3](../../deploy/t3/README.md).
