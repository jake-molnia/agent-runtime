# Sandbox desktop and tools

Each sandbox has a minimal XFCE desktop, a terminal, a file manager, and one
visible Chromium instance. AIO controls desktop screenshots and input. Playwright
controls pages in that same browser through CDP. Task files belong in `/workspace`.

The sandbox image includes upstream `aiod` and `computer-use` binaries at version
`0.9.2`, with architecture-specific checksums verified during the image build.
Playwright MCP is pinned to `0.0.83`. Document conversion uses
`markitdown-mcp==0.0.1a7` and `markitdown==0.1.8`.
The worker image contains no desktop services. VS Code Server and Jupyter are
not installed or started.

The image uses AIO's standalone daemons with our existing non-root Debian image.
This keeps the upstream tool implementations while giving `agent-runtime` one
owner for startup and shutdown. It avoids the prebuilt Computer image's second
supervisor and bundled applications. Browser, terminal, document, and desktop
tools still share the same filesystem and display.

## Tool connections

The [example deployment](../examples/definitions/deployment.yaml) defines two MCP
connections with exact tool names:

- `aio` exposes shell and code execution, file operations, a text editor,
  environment and package information, skill loading, desktop screenshots,
  mouse and keyboard input, and document conversion.
- `browser` exposes Playwright navigation, accessibility snapshots, element and
  coordinate actions, screenshots, PDF output, uploads, dialogs, console and
  network inspection, JavaScript execution, tracing, and recording tools.

AIO forwards `documents_convert_to_markdown` to the local MarkItDown MCP server.
Document conversion produces text for the agent to read. Playwright's
`browser_pdf_save` instead renders a browser page as a PDF.

The Playwright server enables `vision,pdf,devtools` and attaches to the existing
Chromium CDP endpoint. Its allowed Host header is `127.0.0.1:8931`, matching the configured
MCP URL. The example omits `browser_close` because the browser session belongs to
the runtime. Keep at least one browser window open when using desktop actions or
`browser_tabs`; closing the last window can end the supervised browser process.

Both MCP servers operate on the same desktop state. A browser navigation appears
in desktop screenshots, and desktop input changes the pages Playwright inspects.
The browser and desktop share focus, so agents should inspect the current page or
display when switching control methods.

## Agent permissions

The example profile grants `[aio, browser]`. Each example built-in override selects
those same connections in `agents/<name>/agent.yaml`. The overrides retain the
built-in agent instructions and add a short sandbox skill. Embedded built-ins
remain tool-free when used without these overrides.

Deployment grants and agent selections are both required. The compiler grants
the listed tools individually at global and agent scope, after a default deny
rule. Adding a server to the registry alone gives an agent no tools. Removing a
connection from an agent's `mcp` list removes its tools from that agent's session.
See the [MCP grant reference](../definitions/README.md#remote-mcp-grants).

These connections grant broad control inside the sandbox. Shell execution,
arbitrary code, file editing, and desktop terminal input all act as the sandbox
user. Separate service homes and clean process environments reduce accidental
credential exposure. They are not access-control barriers between processes that
share a UID. The sandbox is the isolation boundary.

## Local service endpoints

Desktop services listen on loopback inside each sandbox:

- AIO MCP: `http://127.0.0.1:18091/mcp`.
- Playwright MCP: `http://127.0.0.1:8931/mcp`.
- MarkItDown MCP: `http://127.0.0.1:8932/mcp`, registered through AIO.
- Chromium CDP: `http://127.0.0.1:9222`.
- Computer worker: `http://127.0.0.1:18100`.
- noVNC desktop viewer: `http://127.0.0.1:6080/vnc.html`.

For a sandbox pod in Kubernetes, a local port-forward provides desktop viewing:

```sh
kubectl -n agents port-forward pod/<sandbox-pod> 6080:6080
```

Open `http://127.0.0.1:6080/vnc.html` while the port-forward is active. Keep the
forward bound to the local machine. These desktop endpoints have no independent
user authentication and are not public service ports.

## Lifecycle and verification

`agent-runtime serve` owns desktop startup and shutdown alongside `sandboxd` and
OpenCode. Services run as the sandbox's unprivileged user. Desktop readiness
contributes to the existing supervisor `/health` endpoint. Required service exit
fails the runtime instead of leaving a healthy-looking partial desktop.

The desktop requires writable temporary state and browser profile storage.
Its service home is separate from OpenCode's credential home and `/workspace`.
The workspace stays empty during startup so repository preparation can clone into
it. Desktop configuration comes from `/usr/local/share/agent-runtime/desktop`;
deployments can replace it through `SANDBOX_DESKTOP_CONFIG`. `SANDBOX_NOVNC_WEB`
can similarly select a different noVNC asset directory. Playwright's FFmpeg
package lives in `PLAYWRIGHT_BROWSERS_PATH`, defaulting to
`/opt/playwright-browsers`, outside each session's temporary home.
Chromium's process sandbox also depends on the container runtime's user-namespace,
seccomp, and capability settings. Image architecture support alone does not prove
the desktop works under a particular cluster security configuration.

Run the behavioral checks against services inside a sandbox:

```sh
make desktop-smoke
```

To build and test the image with Docker:

```sh
make desktop-image-smoke
```

The image test uses a private container, a 1 GiB shared-memory mount, and an
unconfined seccomp profile so Chromium can create its own sandbox namespaces.
Production should use the equivalent allowance supported by its sandbox runtime.
The test starts `agent-runtime serve`, verifies the tools, kills the computer
worker, and checks that the runtime and its descendants stop. On a machine with
the desktop dependencies installed, the same check can run without Docker:

```sh
python3 desktop/smoke.py --start /path/to/agent-runtime --check-failure \
  --workspace /path/to/workspace --output-dir /tmp/desktop-evidence
```

Omit `--check-failure` to test normal shutdown. The script saves MCP inventories,
desktop screenshots, converted text, and a result file. The opt-in native OpenCode
MCP integration test additionally verifies that PNG image content reaches a local
mock model endpoint through the real harness:

```sh
AGENT_RUNTIME_TEST_OPENCODE_BINARY=/path/to/opencode2 \
  go test ./command -run '^TestOpenCodeNativeMCPIntegration' -count=1
```

Behavioral verification must demonstrate both directions of shared control:
change a fixture page through Playwright, observe it in an AIO desktop screenshot,
then change it through desktop input and read the result through Playwright.
A non-browser application verifies full desktop input. Screenshot image content
must also pass through the actual harness to the model. MCP tool discovery alone
does not prove these behaviors.

The [upstream example research](computer-use-examples.md) records the source
implementations behind this integration.
