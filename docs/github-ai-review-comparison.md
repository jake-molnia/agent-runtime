# GitHub AI review compared with agent-runtime

Research date: 2026-10-14. Local source revision: `49fcf3f82b1d37bef957eb3302ad8d1515df4c9c`.

Follow-up correction: the runtime-owned [`pr-review` pack](../workflows/presets/pr-review.yaml) already had four independent specialists followed by one verifier, triage, and writing at the inspected revision. The verification-order gap below applies to the older GitHub example, not that bundled pack. This thread has now aligned the example with the pack and added optional collapsed details and pinned source links. The analysis below records the pre-change example; see [the current design](github-review-design.md) for the implemented changes and separate security proposal.


Our runtime already has a credible multi-agent review architecture and careful publication controls. The largest gaps are consistent verification of every finding, continuity across commits, review-specific context, and feedback about whether the reviewer is useful. Adding more agents is not the first improvement I would make.

This comparison covers the checked-in runtime and `examples/github-review`, not the live production configuration. Production prompts, models, tools, and policies belong to a separate config repository. The checked-in example has placeholder repository IDs and publication disabled. A capability absent here might exist in deployment configuration or an external service. Vendor behavior below describes publicly documented products and architecture, not private internal review pipelines or a comparative accuracy benchmark. The [vendor research notes](github-ai-review-vendor-research.md) contain the detailed sources and product limitations.

## What our implementation already does

The example graph runs `review`, then `verify` and `adversarial` in parallel, then `writeup`. Review uses `gpt-6-sol`, verification and writing use `gpt-6-luna`, and the adversarial stage uses `glm-5.3`, all through Aperture. These are example configuration choices, not hardcoded product dependencies. Models, instructions, tools, and graph topology are configurable. Sources: [workflow](../examples/github-review/workflows/review-agents.yaml), [agent definitions](../examples/github-review/agents), [deployment](../examples/github-review/deployment.yaml).

The integration provides several concrete safeguards:

- Repository and GitHub App installation allowlists, signed webhook verification, and separate request/config permission to publish.
- Exact base/head checkout per stage, with a temporary repository-scoped contents-read token removed before the agent starts. Publication credentials remain on the worker.
- Per-PR serialization, 30-day admission deduplication, current-head checks before stages and publication, and periodic checks during active work.
- Validation of candidate message identities and structured output. The writer cannot substitute its own candidate array, invent source indices, change source locations, or lower the highest priority within a merged group.
- Inline findings where GitHub permits them, body links for other locations, and persisted publication parts reconciled after uncertain API responses.

Sources: [integration documentation](github-review.md), [checkout](../githubreview/checkout.go), [review DAG](../hatchetbridge/review.go), [stage guards](../hatchetbridge/review_guard.go), [reducer](../githubreview/reduction.go), [publication](../githubreview/publication.go).

These controls establish authorization, provenance, and repeat-safe publication. They do not establish that an agent's bug claim is correct. The runtime can run native tools against the repository, but the example does not require a reproduction or test result for every finding.

## How the products differ

| Product | Documented approach and useful features | Difference from our reference setup |
| --- | --- | --- |
| [Claude managed Code Review](https://code.claude.com/docs/en/code-review) | Specialist agents, candidate verification, severity/deduplication, `REVIEW.md`, fixed-thread resolution, neutral checks and annotations. Its GitHub Action is a separate CI integration. | Similar overall architecture, but our adversarial discoveries bypass verification. We lack finding lifecycle tracking and GitHub check output. |
| [OpenAI Codex](https://learn.chatgpt.com/docs/third-party/github) | Current default reports P0/P1 issues; scoped `AGENTS.md`, automatic or `@codex review`, separate Security Review preview, and `@codex` fix tasks. | We control models and graph topology, but lack a packaged GitHub conversation/fix flow and explicit scoped review-rule handling. Exact hosted verification internals are not established by the docs. |
| [GitHub Copilot](https://docs.github.com/en/copilot/concepts/agents/code-review) | Agentic project context on Actions runners, skills/MCP, configurable effort, suggested changes, cloud-agent handoff, optional approvals in preview. | Our native tools/MCP provide a foundation for comparable investigation. We lack the packaged context and remediation flow, review effort product controls, and optional approval integration. |
| [CodeRabbit](https://docs.coderabbit.ai/overview/architecture) | Full checkout, Review/Verification agents, static analyzers, [incremental reviews](https://docs.coderabbit.ai/configuration/auto-review), granular triggers, and [learned preferences](https://docs.coderabbit.ai/knowledge-base/learnings). | The full checkout and agent separation are familiar. Incremental reuse, retained feedback, deterministic analyzer integration, and review-specific trigger controls are the larger gaps. |
| [Cursor Bugbot](https://cursor.com/docs/bugbot) | Scoped `BUGBOT.md`, learned rules, PR discussion context, rule coverage diagnostics, local/remote patch deduplication, bounded Autofix. | We have event deduplication, but not patch-level deduplication, learned policy, observable rule coverage, or a fix loop. |
| [Qodo](https://docs.qodo.ai/code-review/overview) | Context from specs, designs, PR history, and related repos; [historical relevance](https://docs.qodo.ai/code-review/relevance); [fixed/dismissed/outdated tracking](https://docs.qodo.ai/code-review/track-resolved-findings). | Intent context is not guaranteed in our input. We lack history-aware finding status and a feedback model that distinguishes a fix from a dismissal. |
| [Greptile](https://www.greptile.com/docs/code-review/cross-repo-context) | Configured read-only related-repo checkout, [learning](https://www.greptile.com/docs/how-greptile-works/memory-and-learning), and [TREX sandbox validation](https://www.greptile.com/trex.md) with execution artifacts. | We can execute tools, but do not package reproducible validation, runtime evidence, or related-repository context as a review contract. |

Full-repository access is already present locally. That does not imply an indexed dependency graph or consistent cross-repository retrieval, and an index is not automatically necessary to get good reviews. Our configurable model mix and infrastructure control are useful strengths. They do not prove higher accuracy or lower total cost than a hosted product.

Billing is not directly comparable: some products meter tokens or credits, others combine subscriptions with allowances, and our costs include model usage plus infrastructure and operations. The [research notes](github-ai-review-vendor-research.md) record fetched pricing details and caveats. None of those prices establishes cost per useful finding on our PRs.

## The most important gaps in our example

### New adversarial findings bypass verification

The verifier receives the original review. The adversarial stage also receives that review, investigates additional failure cases, and returns its own candidates. Both outputs are authoritative inputs to the writer. The writer must retain every source index, merging duplicates where appropriate. There is no subsequent verifier for the adversarial discoveries. Calling the stage independent in its prompt does not make it a blind independent review, since it sees the first review.

Anthropic describes specialist discovery followed by a verification step for candidate findings. Our reference graph does not apply that sequence to every candidate. Sources: [our workflow](../examples/github-review/workflows/review-agents.yaml), [adversarial instructions](https://github.com/jake-molnia/agent-runtime/blob/49fcf3f82b1d37bef957eb3302ad8d1515df4c9c/examples/github-review/agents/adversarial-review/instructions.md), [writer instructions](https://github.com/jake-molnia/agent-runtime/blob/49fcf3f82b1d37bef957eb3302ad8d1515df4c9c/examples/github-review/agents/review-writeup/instructions.md), [Claude review documentation](https://code.claude.com/docs/en/code-review).

A small configuration change could make both discovery stages feed one final verifier, with only verified candidates passed to the writer. If independent discovery is desired, give both discovery stages the PR context without sharing the initial findings. Evaluate this against the current graph before adopting it. Preserve the reducer's source-coverage invariant after filtering rather than letting the writer silently discard findings.

### We preserve publication state, not finding history

Our durable record contains the review owner, status, review ID, and publication requests. Its completion identity is repository, PR, and head SHA. That prevents duplicate publication for the same head. It does not identify the same underlying bug across different commits, remember a dismissal, or resolve a previously published thread after a fix.

The GitHub API interface exposes canonical PR identity, files, prior reviews, and creation of a review. It does not expose thread resolution, reactions, or discussion replies. The review integration explicitly rejects notebook-enabled workflows. Sources: [review types and identity](../githubreview/review.go), [store](../githubreview/store.go), [workflow registration](../hatchetbridge/review.go).

Incremental review needs two separate capabilities: reuse prior analysis where appropriate, and reconcile findings across pushes. Neither follows automatically from webhook deduplication. Stable issue identities should tolerate moved lines and associate GitHub threads with fixed, dismissed, recurring, or unresolved issues.

### Review evidence and limitations are mostly prose

Candidate fields are `priority`, `path`, `line`, `title`, and `explanation`. There is no explicit evidence record, reproduced/not-reproduced status, confidence, introduced-versus-pre-existing classification, or test result reference. Priorities accept P1 through P3, but not P0.

Candidate reports require a `limitations` field, yet the reducer does not carry it into its final output. The writer can voluntarily mention limitations in its assessment, but trusted code does not preserve them. An incomplete investigation can therefore look more conclusive than it was. Sources: [candidate schema](https://github.com/jake-molnia/agent-runtime/blob/49fcf3f82b1d37bef957eb3302ad8d1515df4c9c/examples/github-review/agents/verify/output.schema.json), [reducer](../githubreview/reduction.go).

The next version should preserve coverage limitations and attach evidence to claims. A reproduction is useful when feasible; a precise code trace can support issues that cannot be reproduced locally. Unsupported candidates should remain visible in the internal audit record without being posted as established bugs. Numeric model confidence alone is not calibrated evidence.

### Access to context is not a context policy

The guaranteed input contains PR identity and base/head revisions. It does not contain the PR title/body, linked acceptance criteria, prior discussion, CI results, or dependency-repository context. Agents can investigate through native tools and configured MCP access, so these are not necessarily inaccessible. The example simply does not guarantee that they are retrieved or used.

Likewise, trusted config owns instructions, but the integration does not define an explicit contract for scoped repository review rules, rule provenance, or safe handling of rule changes in a PR. Sources: [input and prompt](../githubreview/review.go), [workflow input assembly](../hatchetbridge/review.go), [review instructions](https://github.com/jake-molnia/agent-runtime/blob/49fcf3f82b1d37bef957eb3302ad8d1515df4c9c/examples/github-review/agents/code-review/instructions.md).

A useful review package would retrieve bounded PR intent, relevant discussion, and check results when available, and record missing context. Repository-specific review rules can remain in trusted config or be read from an explicitly trusted revision. Preserve the existing rule that arbitrary PR content cannot authorize tools or publication.

### GitHub interaction is limited

Configured event actions are `opened`, `synchronize`, `reopened`, and `ready_for_review`. Manual execution is a runtime CLI command. There is no integrated `@bot review`, conversational explanation, authorized fix command, pause/resume command, or per-PR review subscription.

Publication uses `COMMENT`, enforced in the client. There is no GitHub Check Run lifecycle, structured suggestion field, contents-write fix workflow, or approve/request-changes mode in this integration. An agent could put a suggestion fence inside freeform prose, but that is not a validated patch workflow. Sources: [integration policy](../githubreview/integration.go), [client](../githubreview/client.go), [publication](../githubreview/publication.go).

Neutral checks showing queued, running, partial, failed, stale, and completed states would be useful before automated approval. Comment-only review is a reasonable default and is also the behavior of Claude's managed reviewer. Approval support is a product choice, not a universal measure of review quality.

### We measure execution, not review usefulness

Telemetry exports lifecycle timings and run/event counters. Agent execution configuration provides timeouts. The inspected integration does not implement a review-quality dashboard, accepted/rejected finding feedback, a per-review token ledger, or enforceable token/cost budgets. Upstream provider limits may exist outside this repository. Sources: [telemetry](../telemetry/telemetry.go), [definition configuration](../definitions/catalog.go).

The existing tests exercise identity validation, schema handling, state changes, and publication recovery. Those are valuable integration tests. They do not measure false positives or missed bugs on real PRs. No model-backed comparison was run for this research.

## Recommended order of work

1. **Make the existing review more trustworthy.** Verify every discovery path, preserve limitations, define introduced-versus-pre-existing behavior, and establish a high-signal publication policy. These changes belong mainly in the reference workflow and prompts, with schema/reducer work for evidence and limitations.
2. **Measure before adding agents.** Build a reviewed historical PR set with known bugs and clean changes. Compare a single reviewer, the current graph, and discovery followed by final verification. Track actionable precision, recall on labeled defects, duplicate rate, incomplete runs, latency, and observed usage. Have humans adjudicate findings without knowing the tool that produced them.
3. **Add finding continuity.** Track stable findings and GitHub thread IDs across heads. Recheck prior issues, resolve confirmed fixes, preserve dismissals, and post only genuinely new or changed findings. Handle rebases and moved code explicitly.
4. **Improve context and GitHub visibility.** Supply PR intent and relevant checks, support bounded trusted review rules, publish neutral check status, and add an authorized manual re-review command. Keep same-head replay semantics explicit so a command cannot accidentally defeat publication deduplication.
5. **Collect feedback and control cost.** Store ratings and dismissal reasons, expose review usefulness and cost, and add budget enforcement at the provider call boundary. Treat learned rules as proposed scoped policy, with provenance and a way to disable them.
6. **Add fixes once findings earn trust.** Start with validated suggestions or a separate fix branch plus tests. Keep review publication and code-write credentials separate. Do not make automatic approval or automatic application a prerequisite for a good reviewer.

The first investment should be finding quality and continuity. We already have configurable multi-agent execution and a real checkout. A new model or another reviewer will not by itself teach the system that a developer dismissed the same issue two commits ago.
