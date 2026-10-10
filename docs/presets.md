# Agent presets and workflow starters

The runtime includes 16 general-purpose presets and seven specialized roles for its bundled PR review. A preset supplies instructions and an output contract. A workflow starter supplies an editable task brief and a single agent step. Models, credentials, tool services, and execution profiles come from deployment configuration.

The everyday presets own discovery and investigation. A price watcher searches for retailers and evaluates offers. A daily brief researches and writes its own stories. These presets do not require merchant APIs, fixed news feeds, or a separate collection pipeline.

## Available presets

| Preset | Finished work | Evidence and tools |
| --- | --- | --- |
| `code-review` | Actionable correctness, safety, and maintainability concerns. | Supplied changes and context; approved source tools improve coverage. |
| `verify` | Checks claims against requirements and evidence. | Original requirements, claims, and supporting material. |
| `adversarial-review` | Counterexamples, failure modes, and unsupported assumptions. | Supplied work and evidence; approved tools for further checks. |
| `ci-failure-analyst` | Diagnoses failures, ranks causes, and identifies the next discriminating check. | CI run logs, revision-pinned source, optional bounded reproduction tools. |
| `findings-triager` | Reconciles findings, removes duplicates, and preserves disputed claims and provenance. | Reviewer findings plus original evidence. |
| `security-reviewer` | Scoped security findings with exploit prerequisites and repair advice. | Source, configuration, and relevant architecture evidence. |
| `app-pentester` | Reproducible weaknesses in an explicitly authorized application. | Approved browser or HTTP tools, configured targets, test identities, and test limits. |
| `incident-analyst` | Impact assessment, timeline, competing hypotheses, and next checks. | Logs, metrics, traces, and change history for the incident window. |
| `dependency-upgrader` | Updated manifests and lockfiles, migration changes, and actual validation evidence. | An isolated checkout, package-manager and test tools, release guidance, optional publication tools. |
| `upstreamer` | Integrates upstream while preserving the fork's intentional behavior. | Repository tools, isolated integration, exact revisions, required checks, and a supplied merge policy. |
| `researcher` | A sourced answer with alternatives, uncertainty, and useful next steps. | Supplied material or general search and browsing tools. |
| `monitor` | Meaningful developments compared with previous observations. | Search and browsing, the user's update criteria, and task notes. |
| `price-watcher` | Worthwhile buying opportunities with verified conditions and costs. | Search and browsing, buying preferences, and task notes. |
| `news-researcher` | Sourced story notes, technical context, and confirmed versus uncertain claims. | Search and browsing, topics, time window, and prior coverage. |
| `daily-brief` | A finished digest or spoken script with accessible explanations and sources. | Search and browsing, audience preferences, and previous edition notes. |
| `opportunity-scout` | Relevant opportunities with fit, requirements, deadlines, and next actions. | Search and browsing plus the user's preferences and constraints. |

The first three presets retain their original unrestricted JSON output contract. The other 13 use the result envelope described below.

## Create a workflow starter

These commands list the builtins and create a price-watching workflow in your definitions directory:

```bash
export AGENT_DEFINITIONS_DIR="$PWD/config"
bin/agent-runtime workflows templates
bin/agent-runtime workflows init price-watcher monitor-deals
```

The `init` command creates `config/workflows/monitor-deals.yaml`. It refuses to overwrite an existing workflow and rejects symlink paths. It does not create or change `deployment.yaml`, contact a provider, or start a run.

Edit `input.brief` to describe the actual goal. The starter includes a sample objective, one agent step, and a commented schedule. New structured presets enable `notebook: true`; the three original presets do not. A generated starter has this shape:

```yaml
version: 1
input:
  brief: >-
    Find a worthwhile 32-inch OLED monitor deal delivered in the UK below GBP 700.
    Explore unfamiliar retailers, check reviews and offer conditions, and compare
    previous observations. Explain better alternatives. Do not purchase.
notebook: true
steps:
  work:
    agent: price-watcher
    input: input
output: work
```

`input` can contain extra task context such as repository references, required checks, topic preferences, or source restrictions. It must be JSON-compatible YAML. `run --input FILE` replaces the workflow's default input for that invocation.

## Configure the model and general tools

A builtin does not grant itself tools. Deployments can explicitly enable native tools, approved remote MCP services, or both. The runtime does not ship a search/browser service or a repository hosting integration.

The supported native permissions are `read`, `glob`, `grep`, `edit`, `shell`, and `webfetch`. Declare the allowed set in the execution profile and select the needed tools in the agent package. For example:

```yaml
# deployment.yaml, inside an otherwise configured execution profile:
profiles:
  web:
    tools: [webfetch]
    # Include pool, namespace, directory, and provider configuration as usual.
```

```yaml
# agents/researcher/agent.yaml
tools: [webfetch]
```

A profile grant alone does not enable a tool. An agent cannot select a native tool its profile does not grant. New snapshots pin the selected native permissions; changing the current configuration does not add tools to an already pinned run.

`webfetch` reads URLs. It is suitable for following documentation links and reading known pages, but it does not provide browser interaction or a search engine. Native `websearch` is deliberately unsupported because the provider setup requires separate consent and configuration. Use an existing general search/browser MCP service for open-ended discovery. No domain-specific merchant or news APIs are needed.

`edit` covers edits, writes, and patches. `shell` permits broad process, filesystem, and network access within the sandbox; it has no per-command allowlist. A profile that grants shell cannot rely on omitted `read`, `edit`, or `webfetch` permissions to restrict equivalent shell operations. Configure sandbox isolation and external access accordingly.

The [native examples](../examples/native/deployment.yaml) show a URL-reading researcher and an upstreamer with repository-capable native tools. They require the usual runtime deployment and provider credentials. The document-research workflow supplies a concrete brief; the upstream workflow requires your real repository task via `--input`. No schedule or git hosting credentials are configured.

For general web discovery, a deployment can use the following remote MCP structure. Replace the example model, infrastructure values, secret path, URL, and tool names with values supported by your deployment:

```yaml
version: 1
defaults:
  model: {provider: openai, id: gpt-5}
  execution: {profile: web, timeout_seconds: 900}
profiles:
  web:
    pool: agent-pool
    namespace: agents
    directory: /workspace
    secret_files:
      openai: /secrets/openai-key
    mcp: [web]
mcp_servers:
  web:
    url: https://web-tools.example.invalid/mcp
    tools: [search, browse]
```

Select that service in `agents/price-watcher/agent.yaml`:

```yaml
mcp: [web]
```

For MCP access, the deployment declares the endpoint and exact tools, the execution profile grants the service, and the agent selects it. All three are required. See the [definition configuration reference](../definitions/README.md) for MCP endpoint rules and custom agent overrides.

The `defaults` block registers every builtin with a model and execution profile. Use an agent package to override a builtin or inherit one under another name:

```yaml
# agents/buying-research/agent.yaml
extends: price-watcher
mcp: [web]
execution:
  timeout_seconds: 1200
```

Reference `buying-research` in the workflow's agent step. Custom instructions, output schemas, and skills use the existing agent-package format. If you replace the result schema, preserve the notebook fields required by a workflow with `notebook: true`, or disable that workflow option.

Ready-to-edit examples are in [examples/connected](../examples/connected/deployment.yaml). The directory includes research, daily briefing, price watching, and prepare-only fork maintenance. Its MCP endpoints use `.example.invalid` and cannot provide real tools until replaced. No schedules are enabled there.

## Run and read the result

Complete the [runtime deployment setup](agent-automations.md) first. Execution needs the configured Hatchet connection, Kubernetes sandbox pool, provider credentials, durable runtime storage, artifact storage, and runtime key.

Validate configuration before starting the worker:

```bash
bin/agent-runtime agents validate
bin/agent-runtime workflows validate
bin/agent-runtime workflows inspect monitor-deals
```

Choose a stable namespace for notebooks and run the worker in its own process:

```bash
export AGENT_DEPLOYMENT_ID=my-agents
bin/agent-runtime worker
```

Submit a workflow and print its finished report:

```bash
bin/agent-runtime run monitor-deals --wait --text
```

Without `--wait`, submission returns the run ID. Use it to inspect progress, retrieve the result, or cancel the run:

```bash
bin/agent-runtime runs inspect RUN_ID
bin/agent-runtime runs result RUN_ID --wait --text
bin/agent-runtime runs cancel RUN_ID
```

Omit `--text` to retain the full JSON result. The original three presets can return any JSON value, so text rendering requires either a JSON string or an object containing a `report` string.

## Result and notebook contract

New presets return one JSON object with exactly these fields:

```json
{
  "status": "completed",
  "report": "A Markdown report with evidence and relevant limits.",
  "sources": [
    {"url": "https://example.com/source", "title": "Source title"}
  ],
  "notebook": "Useful context to preserve for the next run."
}
```

`status` is one of the following values:

- `completed`: a finished result, including an initial research baseline.
- `no_change`: adequate investigation found no material update.
- `blocked`: a missing prerequisite prevents the requested outcome.

`report` is a nonempty Markdown string. `sources` contains links actually used and can be empty for supplied material or local evidence without URLs. The output schema rejects missing fields, unsupported statuses, and extra properties. Builtin notebooks are limited to 16,384 Unicode characters, which fit within the runtime limit of 64 KiB of UTF-8. Keep durable facts and open questions, and summarize older observations instead of copying whole reports.

For a notebook-enabled workflow, the runtime supplies the agent with this input envelope:

```json
{
  "task": {"brief": "The configured or overridden task input."},
  "notebook": "Previous task notes, or an empty string on the first run."
}
```

The returned `notebook` is a complete replacement. The preset instructs the agent to preserve useful previous observations, sources, preferences, unresolved questions, and prior coverage. An unchanged or blocked investigation must not erase useful context. Notebook workers require a stable `AGENT_DEPLOYMENT_ID`. Notes are scoped to that deployment ID and workflow name, so give independent watches different names and independent owners different deployment IDs. `AGENT_NOTEBOOK_DIR` selects the shared storage parent and defaults to `/state/notebooks`; all workers in one deployment use the same directory and ID.

A notebook is research context, not evidence that a message was delivered or a repository operation was atomic. The normal result path stores and displays the result. Email, chat, purchases, and other external actions require a separately configured and authorized tool.

## Enable recurring work

To run a workflow at 08:00 London time, add this block to its YAML:

```yaml
schedule:
  cron: '0 8 * * *'
  timezone: Europe/London
```

Cron expressions use five fields. The timezone is an IANA timezone and defaults to UTC when omitted. Scheduled workflows require default `input`. A notebook-enabled workflow has exactly one agent step. Its agent schema must directly declare a top-level object, require `status` and `notebook`, declare `notebook` as a string, and give `status` a nonempty enum drawn from `completed`, `no_change`, and `blocked`. Notebook v1 does not accept references or composed schemas for this contract, including `$ref`, `allOf`, `anyOf`, and `oneOf`. Ordinary workflows retain the existing JSON Schema support.

The [dedicated recurring example](../examples/recurring/workflows/morning-tech.yaml) enables a daily schedule. Configure its placeholder tool service before using it with a worker. The agent handles discovery, research, story selection, fact-checking, and writing in that one scheduled run.

## Supply repository and testing policy explicitly

The upstreamer starter uses a prepare-only policy. Supply both repositories and refs, the fork's customizations, protected paths, integration strategy, and mandatory checks. The agent can integrate changes with approved general repository tools and produce a reviewable patch. Publication or automatic landing requires explicit task policy and the appropriate tool access.

The preset instructs the agent to preserve local behavior, avoid force-pushes and resets to upstream, test the exact integrated revision, and recheck heads before landing. These instructions do not implement server-side branch protection, locking, target restrictions, or atomic merge checks. Configure those protections in the tool service or repository platform. Native shell access can prepare integrations and run checks directly in the sandbox; it does not enforce a prepare-only policy or a per-command publication restriction.

The dependency upgrader similarly needs an isolated writable checkout, package and test tools, an update policy, and exact validation commands. A successful-looking report is not a substitute for actual checks.

The app pentester needs explicit authorized targets, test identities, request limits, prohibited actions, and stop conditions before active testing. Its tool service must enforce scope. The preset cannot grant itself permissions, and a URL found during exploration does not become an authorized target.

## Verification

Run `scripts/verify-presets.sh` to test the catalog, recurring execution, notebook
replay, CLI result handling, and every generated starter. Set
`AGENT_RUNTIME_TEST_OPENCODE_BINARY` to OpenCode 2.0.26 to also exercise actual
native tool permissions and MCP handshakes against local fake providers. These
checks do not measure live-model task quality or deploy a Hatchet server.

## Runtime-owned PR review

Select the bundled workflow with `workflows init pr-review review-change`, or write
`use: pr-review` in `workflows/review-change.yaml`. The command writes only a
selector. It does not copy role prompts, schemas, or the graph into config.

The runtime owns `pr-reviewer`, `pr-adversarial`, `pr-security`, `pr-dependencies`,
`pr-verifier`, `pr-triager`, and `pr-writer`. Four independent investigations feed
the verifier, whose findings feed triage and writeup. The writer returns an
assessment and source-indexed findings. This is the analysis workflow; GitHub
admission and publication still belong to a trusted integration. Source-index
coverage must be validated by that integration before posting.

The roles inherit deployment model/profile defaults and grant no tools themselves.
At adoption, optional `agents/<role>/agent.yaml` files select different models,
timeouts, and approved native/MCP tools. The investigative roles need repository
access; triage and writer can operate without tools. Configuration need not carry
any role instruction or schema files.

All role prompts live at `definitions/builtins/<name>/instructions.md`. Shared
schemas live at `definitions/schemas/`; the bundled graph is
`workflows/presets/pr-review.yaml`. Generic starter task briefs are Markdown under
`packs/briefs/`. The runtime compiles these assets into its binary.

`use` cannot be mixed with authored `steps` or `output`. Default input and schedules
remain optional. The expanded graph and resolved agents are captured in the run's
snapshot, so replay never consults a newer pack definition. The multi-step PR pack
does not use the single-agent notebook feature.
