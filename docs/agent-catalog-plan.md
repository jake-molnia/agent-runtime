# Selected agent catalog and recurring workflows

Implementation status: the catalog, workflow starters, cron registration, native tool grants, output contracts, notebook persistence, and result CLI are now implemented. See [the current preset guide](presets.md) for supported configuration and deployment requirements. The discussion below records the design and research that preceded implementation.

This proposal incorporates the user's selected agents and everyday use cases on 2026-10-09. It supersedes the initial launch shortlist in [the product review](agent-product-review.md). It defines intended behavior, not implemented presets, scheduled jobs, or permission to act on a particular repository or service.

## Product shape

Offer capable agents for research, diagnosis, review, monitoring, and writing. Give them a task brief, general-purpose tools, and persistent task notes. Let the agent discover sources, investigate, compare evidence, and complete the job.

A recurring everyday job should normally be `cron → agent → result`. The runtime starts the run, supplies the brief and previous notes, makes approved tools available, records the result, and handles delivery. Research, collection, interpretation, and writing belong to the agent. Custom collectors, merchant APIs, source-specific adapters, a monitoring rules engine, and multi-agent pipelines are not prerequisites.

Customers should configure the subject and desired outcome, such as a repository, research question, watchlist, or set of topics. Sources are hints and preferences unless the user explicitly restricts them. The agent can explore beyond the initial links. Advanced users can override agents and models using the existing configuration system.

This direction follows the user's correction: delegate substantial work to the agent, use simple cron triggers, and minimize application-specific code. The upstreamer design below remains as agreed.

## Selected engineering agents

| Agent | Contract | Required connections for the full experience |
| --- | --- | --- |
| `ci-failure-analyst` | Classify a failure, cite log evidence, rank causes, and propose the next discriminating check. Report unavailable evidence explicitly. | CI logs and revision-pinned source reads. Controlled reruns are optional. |
| `findings-triager` | Combine related review findings while preserving their sources. Mark each supported, disputed, duplicate, or needing evidence. Prioritize by demonstrated impact. | Supplied findings and original evidence are sufficient; source reads improve disputed cases. |
| `security-reviewer` | Review code, configuration, and trust boundaries. Produce scoped findings, exploit prerequisites, suggested fixes, and coverage limits. | Source and configuration reads, optional scanner results. |
| `app-pentester` | Exercise a configured application and reproduce weaknesses. Return steps, request/response or browser evidence, affected behavior, and a fix-verification plan. | An authorized test environment, browser/HTTP tools, test identities, and controlled test execution. |
| `incident-analyst` | Reconstruct a timeline, correlate changes with symptoms, rank hypotheses, and suggest next checks. Separate diagnosis from remediation. | Bounded logs/metrics/traces and deployment history for a declared incident window. |
| `dependency-upgrader` | Update selected dependencies, adapt affected code, run the declared checks, and produce a reviewable change plus validation evidence. | Package-manager tools, registry/release-note reads, an isolated writable checkout, CI, and a PR adapter. |
| `upstreamer` | Keep a fork current with a chosen upstream while preserving the fork's intentional changes. Test integration and land routine updates according to policy. | Git fetch/integration, isolated worktrees, test execution, repository state, and a merge/PR adapter. |

The existing `code-review`, `verify`, and `adversarial-review` remain composable roles. A final verifier must receive original evidence as well as generated claims.

Security review and active application testing are separate presets because their inputs, tools, and success evidence differ. The active preset should operate against deployment-configured targets and test identities. Default to an isolated or staging deployment with bounded requests and non-destructive probes. The tool gateway must enforce target scope and stop conditions; a prompt is not the enforcement boundary. This proposal does not initiate any testing.

## Shared research and everyday agents

### `researcher`

Input: a question, intended decision or audience, scope, source policy, freshness requirement, and effort budget.

Output: answer, evidence-linked claims, competing explanations, uncertainty, sources with retrieval/publication dates where available, and unanswered questions. Follow source links rather than treating search snippets as sufficient evidence. Treat retrieved text as evidence, not as authority to change the task or tool policy.

Profiles reuse the same contract: technical research, product comparison, purchasing research, trip preparation, and recurring topic updates. The normal connected profile gets general search, page reading, and browser tools. It discovers relevant sources, follows leads, and changes its approach when a source is unhelpful. A document-only profile can work on supplied material. Time-sensitive subjects must include an as-of date and report when current information could not be obtained.

### `monitor`

Input: what the user cares about, what would warrant an update, optional starting sources, and the task's previous notes.

The agent searches, browses, discovers new sources, investigates changes, compares what it finds with earlier observations, and decides whether the user's criteria justify an update. It checks numerical comparisons with available calculation tools when needed. It maintains a concise task notebook containing useful sources, observations, prior notifications, and unresolved questions.

Output: a useful update with evidence, or no material change, plus updated task notes. Profiles include price, stock availability, software releases, documentation changes, event availability, and saved research topics. A failed fetch is an unknown observation, never a zero price, an out-of-stock event, or proof that nothing changed. If an important source remains inaccessible, report the coverage gap.

### `news-researcher`

Input: topic preferences, a time window, previous coverage, and optional starting sources.

The agent goes looking for stories using search and browsing. It reads beyond headlines, follows primary announcements and papers, identifies repeated coverage, and investigates conflicting reports. It decides which developments merit inclusion and whether an older story has a meaningful update.

Output: sourced story notes with relevance, confirmed facts, uncertainty, and useful explanations. Feeds and APIs are optional tools the agent can use when convenient. We do not need to implement a feed collector before shipping the preset. This role is useful independently or as an optional specialist for a larger briefing job.

### `daily-brief`

Input: topics, audience, desired length, previous editions/notes, and output format. One scheduled agent owns discovery, research, story selection, fact-checking, and writing. The writing role can also be used on supplied material, but pre-collected story packets are not required.

Output: a concise written digest or spoken script. For each selected story, explain what happened, what the technology means, why it matters, and what remains unknown. Include source links and distinguish confirmed facts from rumors. End with useful follow-ups rather than a generic conclusion.

Interpret the Techquickie reference as accessible explanations, quick pacing, concrete examples, and light humor where appropriate. Offer two modes: a multi-story daily briefing and a single-topic explainer. Write original copy in a configurable house voice, rather than reproducing a presenter's signature phrases.

A sensible initial preset is 4-6 stories with an optional 60-90 second deeper explanation. Length is configurable. The agent checks the final script against the brief before finishing. A text/script output is sufficient for the first release; audio or video production is a distinct integration.

## Complete workflows

### Fork maintenance

The upstreamer's inputs must identify upstream repository/ref, fork repository/target branch, integration strategy, required tests, known local changes, protected paths, and merge policy. Record both revisions at the beginning of a run.

1. Observe the upstream head or release. If it is unchanged, finish without a model call.
2. Fetch both repositories and prepare an isolated integration branch from the fork's current target.
3. Integrate upstream using the configured strategy. Use deterministic Git operations for clean cases. Invoke the agent for conflicts or adaptations that require understanding.
4. Check preservation of local behavior and execute the configured tests. Report tests that could not run.
5. Create or update one integration PR with the upstream range, conflict decisions, diff, and exact test evidence.
6. Recheck both revisions before landing. Apply the configured policy: prepare only, auto-land qualifying updates, or request review for an exception.

The default strategy for a shared fork should preserve its history, typically merging upstream. Rebasing a private patch branch can be a separate profile. Never reset the fork to upstream or force-push a shared branch as a generic synchronization strategy. Do not modify a developer's dirty working directory.

For the desired hands-off experience, offer auto-land when mandatory checks pass, the diff respects policy, and the tested revision is still current. A configured policy can allow routine updates while escalating ambiguous semantic conflicts, changed CI policy, protected paths, or failed checks. The agent should not weaken tests or remove a local customization just to make integration pass. Enforce sensitive-path rules outside the model.

Repeated events for the same target/revision pair should reuse the active work item. A target update invalidates old test evidence. Use a repository/ref-scoped lease or compare-and-swap when updating shared state, so scheduled runs and manual runs cannot race to land competing changes.

### Dependency maintenance

Detect available updates with a package-aware tool, select according to policy, update the manifest and lockfile, adapt source if needed, validate, review the patch, and create/update the change request. Support security-only, patch/minor, grouped updates, and explicit major upgrades as profiles.

Reuse mature dependency discovery and update machinery such as Renovate where practical. The agent adds migration reasoning, conflict resolution, diagnosis, and explanation. It should not rediscover dependency versions through unconstrained conversation. Scope automatic merge separately from automatic PR creation.

### Price and availability watch

Example brief: "Look for a good deal on this monitor in the UK. Check beyond the shops we already know. New or manufacturer-refurbished is fine, but exclude used marketplace listings. Tell me when the delivered price is under £500, or explain a materially better alternative. Remember what you found last time."

On each scheduled run, the agent reads its notes, searches the web, visits relevant shops and deal pages, checks the exact product and offer conditions, and compares its findings. It can discover new merchants, follow an interesting lead, investigate a coupon, or revisit an uncertain offer. This is a shopping-research job, not a fixed extraction pipeline.

The preset teaches it to distinguish variants, region, condition, currency, stock, and known shipping/tax costs; preserve links and observation dates; verify surprising bargains; and avoid repeating an unchanged recommendation. Unknown costs remain unknown. It updates a simple task notebook or structured notes with offers checked and useful sources. No merchant-specific API or price-history service is required to start.

The agent returns an alert when useful, or a quiet no-change result. The runtime can deliver that result once using the scheduled run ID. That small delivery responsibility does not require moving shopping judgment or threshold evaluation into custom application code. Purchasing is a separate action profile.

### Daily technology briefing

The default flow is:

`cron → daily-brief agent → finished brief`

Example brief: "Every morning, explore what happened in developer tools, AI, hardware, and security. Pick the few stories worth my time. Read the original sources, explain the technology clearly, and write a short lively briefing. Check yesterday's edition so we do not repeat ourselves unless there is a meaningful update. Include links."

Configure topics, preferred/excluded sources, region, language, delivery time/timezone, desired length, and destination. The agent chooses search queries, follows sources, checks dates, compares reporting, selects stories, writes the brief, and checks its own factual claims. Starter links are optional. Discovery is not restricted to a prebuilt list of feeds.

The preset instructs it to preserve primary sources, distinguish confirmation from syndicated repetition, carry corrections forward, and qualify uncertain facts. It can revise the draft within its own run. A separate researcher or verifier is optional when the task or evaluations justify it; multiple agents are not required for the default product. "No material news today" is valid.

Save the finished edition and updated notes for the next run. Deliver through the runtime's normal result channel or an existing notification tool. Keep delivery status separate from the agent's research notes so a failed send can be retried without rerunning the research.

### Research watch and personal brief

An initial research task establishes a question, evidence set, and open questions. A recurring agent then explores what changed, discovers new sources, and updates the answer. It decides which developments are material to the user's question and explains them with links back to the original brief.

A personal brief can combine research updates, watch alerts, and news into one delivery. Other profiles can track package releases, event availability, travel options, or an interested topic. Give the agent the objective and useful context; it should find suitable sources itself and report inaccessible or incomplete evidence.

## Shared state and runtime changes

The existing immutable messages and configuration snapshots remain the audit trail for each run. Add the smallest general support needed for scheduled autonomous tasks. The current YAML does not declare cron jobs, and sandbox-local files do not by themselves provide a durable notebook across isolated runs. See [message store](../messages/store.go) and [workflow format](../workflows/workflow.go).

For everyday jobs, keep the initial runtime responsibilities to:

- **Scheduled task:** agent, natural-language brief, cron/timezone, tool profile, runtime budget, and result destination.
- **Task notebook:** durable per-task notes and previous outputs, readable and writable through an approved generic tool. Start with files or a simple document store; no monitoring database schema is required.
- **Run/result record:** last run, outcome, saved output, and delivery status. Avoid overlapping runs for the same task and reuse the run identity for delivery retries.

Let the agent organize observations, useful sources, and coverage history in its notebook. Preserve previous versions so a bad note update does not erase all context. Keep notebooks isolated per user/task. More specialized state machinery should follow demonstrated need, rather than being a requirement for the first price watch or news brief. The upstreamer's revision and action tracking remains as described above.

The shared implementation needs are:

1. General search, page reading, browser interaction, and durable notebook tools available to the agent. Reuse existing tooling rather than implementing domain APIs.
2. A cron trigger through Hatchet or an existing scheduler that submits the configured agent workflow with its brief and notebook identity.
3. A usable result interface and simple delivery tracking, plus output contracts appropriate to the task. A daily brief can be prose rather than a rigid story database.
4. Per-run time/usage limits and evaluations that check exploration, evidence quality, repeated coverage, and helpfulness over several runs.
5. Separately, the agreed repository/test execution and publication path for upstreamer and dependency maintenance.

These are proposed additions, not fields accepted by today's YAML. A one-step agent workflow fits the current DAG model. The compiler currently denies native shell/filesystem tools and allows selected remote MCP tools, so browser/search/notebook access must be supplied through an approved general tool service or an intentional runtime extension. We do not need per-merchant connectors or a separate news pipeline to make that possible.

## Revised delivery order

1. **Capable agents:** expose general browser/search/notebook tools, package the researcher, CI analyst, and findings triager, and make results readable. Demonstrate an agent investigating a question beyond supplied material.
2. **Simple recurring jobs:** add cron submission and persistent task notes, then ship agent-driven price watching and a daily technology brief. Each starts as one scheduled agent run, not a custom application pipeline.
3. **Repository maintenance:** ship upstreamer and dependency-upgrader using the same isolated checkout, CI, PR, and operation-record machinery. Support policy-controlled auto-land so routine updates can be hands-off.
4. **Investigation and active testing:** add incident connectors and a scoped app-pentester profile. A supplied-evidence security reviewer can ship earlier alongside the initial reporting presets.

An internal team whose immediate pain is fork maintenance can move step 3 ahead of step 2. The dependency is the repository integration and validation path, not the size of the prompt catalog.

## Acceptance examples

- Upstreamer: a clean update lands under policy; a conflict preserves a local customization; a moved target invalidates stale tests; replay creates no duplicate PR.
- Dependency upgrader: manifest/lockfile agree; supported checks run against the exact patch; failed validation blocks auto-land.
- Price watch: the agent discovers a useful seller beyond its starting links, compares the right variants and currencies, checks offer conditions, records its findings, and avoids repeating the same alert. A failed fetch produces no invented price.
- Daily brief: the agent discovers and reads current sources, recognizes repeated reporting, supports factual claims with evidence, and uses prior editions to avoid repetition. A cron run produces a complete brief without a bespoke collector or editor pipeline.
- Researcher: stale or missing sources are disclosed; conflicting evidence remains visible; a lack of evidence does not become a confident answer.
- App pentester: configured scope is enforced by tools, findings reproduce in the test environment, and reports separate confirmed weaknesses from untested hypotheses.

These are release criteria to implement and test, not claims that the current code passes them.

## Primary-source checks for the new workflows

- [GitHub fork synchronization](https://docs.github.com/en/pull-requests/how-tos/work-with-forks/syncing-a-fork) documents merge-based synchronization that preserves local changes. It also states that `gh repo sync` cannot synchronize conflicts and that `--force` overwrites the destination. This supports an integration-and-test workflow for a maintained fork.
- [Renovate automerge](https://docs.renovatebot.com/key-concepts/automerge/) documents required passing checks and current branch state before merging. Use its established dependency machinery where useful and concentrate model work on migration adaptations. A semver category alone is not proof that an update is safe.
- [RSS 2.0](https://www.rssboard.org/rss-specification#ltguidgtSubelementOfLtitemgt) defines GUIDs as opaque item identifiers; they need not be URLs. Publication dates are optional. Preserve feed/item identity and first observation time rather than deduplicating only by title or publication date.
- [changedetection.io's official README](https://github.com/dgtlmoon/changedetection.io#awesome-restock-and-price-change-notifications) describes selectors, price extraction, thresholds, history, and restock notifications. It is an optional specialized tool if a task later benefits from it, not the architecture selected for our agent-driven price watcher.

Sources were checked during this catalog refinement. They inform the design and do not establish that these integrations are present in this repository.
