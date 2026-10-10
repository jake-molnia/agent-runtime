# Runtime definitions and deployment configuration

The runtime owns bundled agent instructions, output schemas, and workflow packs.
Deployment configuration selects a pack, models, execution profiles, tool grants,
and task input. Adopting a pack does not require copying its implementation.

```yaml
# workflows/review-change.yaml
use: pr-review
```

The `pr-review` pack includes general, adversarial, security, and dependency
investigations followed by verification, triage, and writeup. Its graph is in
`workflows/presets/pr-review.yaml`; role instructions are in
`definitions/builtins/<name>/instructions.md`. See [the preset guide](presets.md).

Custom workflows and instruction overrides remain supported for deployments that
need them. They are optional. Model and tool overrides can be supplied without
replacing the bundled instructions. Full resolved definitions are snapshotted.

GitHub event admission, pinned repository access, and publication are trusted
integration responsibilities. Selecting the analysis pack does not create that
integration or authorize posting comments. The Go `RegisterMessageDAG` API remains
available for advanced integrations.
