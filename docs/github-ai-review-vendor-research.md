# GitHub AI review vendor research

Research date: 2026-10-14. This note covers official product documentation fetched during this research. It describes documented capabilities, not measured accuracy on our repository. Availability, defaults, and billing have changed during 2026; older comparisons are often wrong. No paid reviews were run. The local implementation comparison is separate.

## Anthropic Claude

The managed [Claude Code Review service](https://code.claude.com/docs/en/code-review) runs specialized agents in parallel against the diff and surrounding code. A verification stage checks candidate findings against actual code behavior, then deduplicates and ranks results. The documentation does not promise an executed reproducer for every finding. Comments include an expandable explanation of why the issue was flagged and how it was verified.

It distinguishes Important, Nit, and Pre-existing findings. Default review focuses on correctness rather than formatting or missing tests. `CLAUDE.md` supplies scoped project context; newly introduced violations default to nits. Root `REVIEW.md` reaches finding and verification agents directly and guides ranking/reporting. It can define severity, evidence requirements, excluded paths, nit budgets, and convergence policies such as reporting only Important issues after the first pass.

The product supports once-after-creation, every-push, and manual review modes. `@claude review` requests one review; `@claude review always` subscribes a PR to future pushes. This changed in July 2026. Fork PRs require an authorized manual request each time. On later reviews, fixed findings can have their threads resolved automatically. Reactions provide feedback collected after merge. Replies to inline findings do not start a conversation or a fix.

GitHub check output contains severity counts in a machine-readable comment and annotations independently of inline comments. Its conclusion stays neutral; teams can implement their own gating using the counts. This prevents a line-position failure from hiding the entire result.

The service is research preview for Team and Enterprise, excludes ZDR/HIPAA configurations, averages about 20 minutes, and documents token billing with typical review cost of $15–25. Those are vendor averages, not a cost estimate for this repo. [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions) is a separate configurable CI integration; its existence does not imply every managed-service feature is included in a basic action workflow.

## OpenAI Codex

The fetched [official GitHub integration documentation](https://developers.openai.com/codex/third-party/github), now served through [ChatGPT Learn](https://learn.chatgpt.com/docs/third-party/github), describes a native GitHub review focused on serious problems. Its current explicit default is P0 and P1 findings. Do not repeat older P1/P2 descriptions without checking the current product.

Reviews can run automatically using repository and personal trigger settings, or through `@codex review`. A one-off comment can narrow focus, such as database migrations. Codex reads applicable root and nested `AGENTS.md` files, with a recommended `Code Review Rules` section. The docs recommend consequential repository-specific rules, safe paths or exceptions, and leaving deterministic checks in CI.

The same page describes a distinct Security Review in research preview. It examines the diff with repository context and configured threat models/security guidance. It can run alongside regular review or through `@codex security review`, with a full Security Report in its associated task. This is more specific than a generic security reviewer prompt, but the GitHub page does not establish exact verification internals or guarantee exploitation tests.

After review, `@codex fix the P1 issue` starts a cloud task with PR context and can push a correction when permitted. General `@codex` requests also support tasks such as fixing CI failures.

Unknown from the fetched integration page: a universal multi-agent verification topology, persistent team-learning system, precise incremental-diff algorithm, guaranteed auto-resolution behavior, and flat per-review pricing. These should not be marked absent; they were not established by this source. GitHub review must also be distinguished from configurable [Codex GitHub Action](https://developers.openai.com/codex/github-action) workflows.

## GitHub Copilot

[Copilot code review documentation](https://docs.github.com/en/copilot/concepts/agents/code-review) describes agentic full-project context gathering using GitHub Actions runners. If those capabilities fail or runners are unavailable, it can fall back to a more limited review. It supports GitHub-hosted, larger hosted, and self-hosted runners. A custom implementation should make this kind of reduced coverage visible rather than report an unconditional clean review.

Copilot can use repository skills and MCP servers for context from tickets, documentation, service catalogs, and incidents. GitHub and Playwright MCP servers are enabled by default according to the current page. Repository instructions and skills come from the PR head branch. Repository MCP configuration is shared with the cloud agent and can be disabled specifically for reviews.

Review effort has Lite and Balanced modes, with Balanced using longer analysis through a higher-reasoning model. GitHub controls the model mix; users cannot swap arbitrary review models. Automatic reviews can be configured by users, repositories, and organizations, including draft reviews and every-push reviews. Either user settings or a ruleset can enable review; overlapping configurations do not create duplicate reviews.

Findings offer suggested changes and can be sent to Copilot cloud agent to create a new PR against the original branch. That handoff is public preview. Optional Copilot approvals are also public preview: by default they do not satisfy required approvals, but enabled settings allow approving reviews to count. A new push dismisses the approval. An assertion that Copilot can never approve a PR is stale.

The current cost model has AI credits plus Actions minutes. The docs estimate $0.05–1 in credits for Lite and $0.25–5 for Balanced, excluding Actions. These are estimates, not fixed prices. Organization members without paid individual licenses can use review when the required organization policies are enabled. Some file categories, including dependency management files, logs, and SVGs, are excluded according to the current page.

## CodeRabbit

The [architecture documentation](https://docs.coderabbit.ai/overview/architecture) explicitly describes full-repository sandboxed cloud execution, agentic exploration, 50+ static analyzers/linters/SAST tools, and specialized Review, Verification, Chat, Pre-Merge Checks, and remediation agents. This is a documented combination of deterministic checks and model analysis. It is not evidence that all 50 tools run on every PR or that every issue gets an executed test.

[Incremental reviews](https://docs.coderabbit.ai/configuration/auto-review) focus on commits added since the last review. Manual commands distinguish incremental review from a full review. The default auto-pause occurs after five reviewed commits, limiting repeated review churn; users can disable the pause or resume manually. Controls include drafts, labels, description opt-in, title exclusions, author exclusions, and base branches. A new push is therefore a policy decision with a budget consequence.

Its [knowledge base](https://docs.coderabbit.ai/knowledge-base) incorporates learnings, guidelines, linked issues, multi-repository context, MCP servers, and web search. [Learnings](https://docs.coderabbit.ai/knowledge-base/learnings) are stored preferences extracted from conversations, with repository/organization/path relevance. The product explicitly acknowledges added learnings in its replies. Some organizations can require approval for chat-sourced learnings. Formal standards belong in review instructions or coding guidelines. Opting out of retained knowledge deletes stored knowledge, while stateless guideline and web context remain available.

The official documentation index also lists [linked-issue requirement validation](https://docs.coderabbit.ai/issues/pr-validation), [CI failure analysis](https://docs.coderabbit.ai/pr-reviews/cicd-pipeline-analysis), [pre-merge checks](https://docs.coderabbit.ai/pr-reviews/pre-merge-checks), [Autofix](https://docs.coderabbit.ai/finishing-touches/autofix), and [review outcome metrics](https://docs.coderabbit.ai/guides/dashboard-metrics). Those are concrete product areas worth comparing, but their deeper semantics were not individually audited here.

[Current plans](https://docs.coderabbit.ai/management/plans) are Free/OSS, Essentials, Team, Advanced, and Enterprise. Essentials is $24/developer/month annually or $30 monthly; Team $48/$60; Advanced $72/$90. Review allowances and files-per-review limits apply. Free cloud PR usage is summaries only, while qualifying OSS receives Team features with its own limits. Coding-agent and security scanning costs can be separate. Old “unlimited Pro reviews” shorthand obscures these limits.

## Cursor Bugbot

The [current Bugbot documentation](https://cursor.com/docs/bugbot) includes automatic and manual PR review, PR comment context, team/repository rules, learned rules, MCP, local review synchronization, and optional Autofix.

Rules can live in root and nested `.cursor/BUGBOT.md` files. Cursor editor `.cursor/rules/*.mdc` files do not automatically apply to Bugbot. Rule provenance is visible with `bugbot run verbose=true`, including truncated or omitted rules. The documented limits are 30,000 characters per rule and 100,000 combined. Team rules take priority when space runs out. This makes instruction coverage observable.

Learning can infer rules from repository history or accept `@cursor remember [fact]`. Rules can be scoped to paths. Rule analytics show findings, PRs, accepted issues, and acceptance rates. The current docs have a surprising operational caveat: on platforms with the learning switch, manually entered repository rules also depend on that switch being enabled.

Local `/review-bugbot` reviews store a patch ID. If the SCM review sees the same patch, it skips redundant work. Bugbot reads existing top-level and inline PR comments to avoid duplicate suggestions and build on prior discussion. These are distinct from merely rerunning on every push.

Autofix starts a Cloud Agent and can push to a new branch or the existing PR branch. Existing-branch mode has a maximum of three attempts per PR to prevent loops. Teams configure defaults and users can override them. Autofix uses Cloud Agent credits and requires on-demand usage and storage. MCP support is Team/Enterprise only.

Bugbot moved to usage-based billing in May 2026. The docs link legacy seat pricing separately. Do not compare its historical $40/seat price as though that were the current universal contract. The fetched page does not establish the exact internal candidate-verifier architecture or a guaranteed reproducer for each bug.

## Qodo

[Qodo Review overview](https://docs.qodo.ai/code-review/overview) describes specialized agents that inspect bugs, rule violations, linked-spec gaps, linked-Figma deviations, cross-repository conflicts, and repository skill findings. Context includes the codebase, architecture, PR history, tickets, and organizational standards. Its [Context Engine](https://docs.qodo.ai/core-concepts/context-engine) connects those sources and produces contextual evidence. Claims about improved precision remain vendor claims until tested on comparable PRs.

[Relevance](https://docs.qodo.ai/code-review/relevance) is a concrete feedback feature in beta on GitHub/GitLab. It shows related historical PRs and classifies findings based on whether similar issues were typically accepted, ignored, or had mixed outcomes. This exposes the historical evidence to the reviewer rather than only changing an opaque prompt.

[Resolved-finding tracking](https://docs.qodo.ai/code-review/track-resolved-findings) rechecks every open finding against subsequent changes. It considers the whole PR, so a fix elsewhere can resolve an issue. It falls back to the originally cited code when the full changeset is unavailable. Statuses distinguish fixed, dismissed, and outdated. Resolved inline comments are closed where the provider supports it, and the review can retain a visible history.

Qodo's current index identifies [Review Standards](https://docs.qodo.ai/governance/review-standards), [Rule Miner](https://docs.qodo.ai/governance/rule-enforcement/rule-miner), [cross-repository review](https://docs.qodo.ai/governance/cross-repo-code-review), and a [Review Resolver skill](https://docs.qodo.ai/agentic-toolbox/agentic-toolbox-review-resolver-skill). These are relevant product capabilities, but detailed plan eligibility and each remediation behavior need a trial or deeper configuration audit.

[Current billing](https://docs.qodo.ai/pricing-and-usage) is workspace-based credits, unlimited users, monthly renewal and configurable overage cap, rather than per-engineer provisioning. The 14-day full trial ends unless billing is active. The documentation does not support quoting an old per-seat price as current.

## Greptile

[Repo Clusters](https://www.greptile.com/docs/code-review/cross-repo-context) group related repositories and clone other members read-only for review. Explicit `context.repos` configuration takes priority. Clusters require at least two repositories, have a documented 20 GB combined size ceiling, and can be suggested from shared contributors over 90 days. This is a configured, repeatable source of cross-repo context.

[Memory and learning](https://www.greptile.com/docs/how-greptile-works/memory-and-learning) observes human comments, replies, reactions, and whether findings were addressed in commits. It infers conventions and changes noise filtering. Its numerical improvement examples and timelines are vendor claims, not independent measurements or service guarantees.

[TREX](https://www.greptile.com/trex.md) goes beyond source inspection by running the PR branch in a sandbox. It can start services, mocks, targeted tests, API calls, and browser agents. Findings include logs, screenshots, traces, scripts, video, or API output. The advertised approximately 20% additional bug detection is not a benchmark we verified. This capability is nevertheless a useful reference for designing an explicit “reproduced” evidence level.

The [official product index](https://www.greptile.com/llms.txt) links graph-based codebase context, scoped `.greptile/` rules, local CLI review, MCP, handoff to coding agents, analytics, review tiers, self-hosting, and optional auto-approval. Those should be treated as documented product areas rather than assumed default behavior in every account. Pricing was not researched in detail for this vendor.

## What the evidence supports

The common pattern is a review system around the model: controlled context collection, candidate filtering, durable finding status, feedback, remediation, and measurable cost/quality. Several products separate finding generation from verification. Others make the verification mechanism less public, so lack of documentation cannot establish lack of verification.

Useful comparison criteria are verified-finding rate, accepted/fixed findings, false-positive dismissals, recurrence after a dismissal, duplicate comments across pushes, coverage gaps, time to first result, total review cost per PR, and fix-loop attempts. Vendor feature lists establish what can be configured; they do not establish which reviewer performs best on this codebase.
