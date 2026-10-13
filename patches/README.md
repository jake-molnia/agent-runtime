# OpenCode MCP tool refresh

The sandbox builds OpenCode 2.0.26 from the official source archive pinned by
commit and SHA-256 in `upstream.json`, using Bun 1.4.2 and the upstream lockfile.
`scripts/build-opencode.sh` applies `opencode-mcp-tool-refresh.patch` before the
upstream CLI build. This is a downstream source correction, not the unmodified
npm binary. It affects both Linux architectures. The embedded web UI is omitted;
the CLI and HTTP API remain available.

OpenCode's MCP connection reports `connected` before its debounced tool-change
subscriber updates the model's tool registry. `SessionContext.select` calls
`McpTool.flush` before taking its tool snapshot, but the original implementation
only waits for the first discovery fiber. Later connection changes can therefore
leave the first model request without its approved tools.

The correction runs the existing synchronous `reconcile` operation inside
`flush`, after initial discovery. It reads the cached connected MCP catalog under
the existing registration semaphore, updates the existing tool registry, and
preserves plugin override precedence. It does not add network requests or delays.

The runtime separately calls the location-scoped `/api/integration` endpoint,
which awaits plugin activation, before connecting approved MCP servers. This
prevents a cold checkout directory from reporting a configured server as missing.
Both barriers precede the first model prompt.

Native integration checks exercise a nested checkout directory, tool availability
in the first model request, broker revocation, and exclusion of unselected servers.
The `verify-opencode` Docker target runs those existing tests against the compiled
binary. The source correction was also checked against the upstream MCP tests,
lint, and package type checks.

Remove this patch when an upstream release synchronously refreshes connected MCP
tools before the model's tool snapshot, then rerun the native checks without a
fixed readiness delay. OpenCode 2.0.28 still has the same race.
