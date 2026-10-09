# Publish container images

The [image workflow](../.github/workflows/images.yml) publishes two GHCR packages:

- `ghcr.io/jake-molnia/agent-runtime-worker`, the `worker` Dockerfile target.
- `ghcr.io/jake-molnia/agent-runtime-sandbox`, the `sandbox` Dockerfile target.

Both contain Linux AMD64 and ARM64 images. The sandbox includes OpenCode,
sandboxd, and Tailscale. The worker contains the Go runtime. Both images include
the [pinned skill bundle](sandbox-skills.md), with no Nix environment. Package names derive
from the GitHub repository name, so forks publish under their own owner.

## Publish from main

Merge the workflow into `main`. Each push runs the Go tests, vet, and binary
build, then builds and publishes both container targets with `sha-<full-commit>`
tags. A separate job promotes both packages to `nightly` after both builds pass.
It checks that the commit is still the head of `main` before promotion, so a
slow older build does not replace a newer nightly. This channel runs on pushes,
not a clock schedule. A superseded build still publishes its SHA tags.

Pull requests run the same checks and build both architectures without publishing.
The workflow uses `GITHUB_TOKEN` with job-scoped `packages: write`; no registry PAT
is needed for publishing. Existing packages must grant this repository Actions
access. New GHCR packages default to private. Configure a cluster pull credential
with package read access, or explicitly change package visibility before deployment.

## Publish a version

From a tested commit on `main`, create and push the next increasing version tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The workflow accepts stable `vMAJOR.MINOR.PATCH` tags. It publishes `v0.1.0`,
`0.1.0`, and the full SHA tag on both packages, then promotes both to `latest`.
Prerelease tags fail validation. Publish versions in increasing order: `latest`
follows the last successful release promotion, not a semantic-version comparison.
Never move a published version tag. This workflow publishes containers only;
it does not create GitHub Release entries or release assets.

## Deploy an image pair

Each image job records its manifest digest in the Actions summary. Pin both
worker and sandbox by digest from the same successful workflow run. Mutable
`nightly` and `latest` tags are convenient for discovery; promotion of two
separate packages is not atomic. SHA tags identify source commits, but rerunning
a build can replace them because OS package installation is not snapshot-pinned.
Digests identify the exact artifacts.

The workflow attaches OCI source/revision labels, build provenance, and SBOMs.
It does not publish the config repo's Flux OCI manifest bundles or change cluster
deployments. See [the replacement assessment](config-runtime-replacement.md)
before changing the production worker.

For a local build, select the target explicitly. The Dockerfile's default final
stage is the sandbox:

```sh
docker build --target worker -t agent-runtime-worker:local .
docker build --target sandbox -t agent-runtime-sandbox:local .
```
