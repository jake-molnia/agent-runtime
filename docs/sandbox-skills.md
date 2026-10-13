# Bundled engineering skills

The worker and sandbox images contain 45 pstack skills and 18 Matt Pocock
engineering skills. They contain no Nix environment or T3-specific skill bundle.

[`skills.lock.json`](../skills.lock.json) pins source commits and archive SHA-256
checksums. [`install-skills.py`](../scripts/install-skills.py) downloads those
archives, verifies them, and preserves supporting files, scripts, and licenses.
It exposes the selected skills through `/opt/agent-skills` and keeps complete
source layouts under `/opt/agent-skill-bundles`. Files install with read-only modes, but the root agent can modify its own sandbox. `inventory.json` records installed skill names, source
revisions, and content hashes. `sources.json` records the build lock, and
`files.json` hashes the supporting source files. Snapshots pin the complete
manifest digest. Sandbox startup checks files and catalog destinations against
that manifest before accepting a saved snapshot.

Pstack's `tdd` and `teach` use the names `pstack-tdd` and `pstack-teach` to avoid
collisions. Matt's engineering skills retain their upstream names. Supporting
files from the rest of Matt's collection remain available for relative references,
but those other categories are not entries in the bundled catalog.

Agent configuration selects bundled skills with `builtin_skills`:

```yaml
version: 1
builtin_skills: [code-review, diagnosing-bugs, pstack-tdd]
```

Native tools are unrestricted in the sandbox. `builtin_skills` activates and
pins the bundled catalog; it does not restrict the skill tool to the listed
names. Supporting references and the whole sandbox filesystem are accessible.
Installing a skill does not create external APIs or grant server-side access.
Aperture and trusted publication integrations still own their authorization.
Existing config-owned skill text remains supported through agent packages.

To verify packaging outside a container:

```sh
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/install-skills.py \
  --bundles /tmp/agent-skill-bundles \
  --catalog /tmp/agent-skills
```

Use fresh destination directories. To update the bundle, change source revisions
and archive checksums together, inspect upstream changes, and rebuild both images.
Keep worker and sandbox images on the same release so their skill inventories
match.

For the opt-in native checks, install the catalog and bundles as sibling
directories named `catalog` and `bundles`, then run:

```sh
AGENT_RUNTIME_TEST_OPENCODE_BINARY=/absolute/path/to/opencode2 \
AGENT_RUNTIME_TEST_SKILLS_DIRECTORY=/absolute/path/to/catalog \
  go test ./command -run '^TestOpenCodeNative' -count=1
```
