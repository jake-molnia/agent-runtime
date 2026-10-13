# Agent packages and workflow product research

Implementation status: the catalog, workflow starters, cron registration, native tool grants, output contracts, notebook persistence, and result CLI are now implemented. See [the current preset guide](presets.md) for supported configuration and deployment requirements. The discussion below records the design and research that preceded implementation.

Research date: 2026-10-09. Scope: first-party agent and workflow documentation, compared with this repository's current catalog and YAML workflow contracts. These sources establish documented capabilities and useful design patterns. They do not establish customer demand, willingness to pay, or measured quality for the proposed agents.

The recommendation is to ship a small collection of complete task packages. Each needs instructions, explicit input and output contracts, sample evidence, representative evaluations, a readable result, and a precise tool requirement. Adding agent names alone would leave most of the integration work with the customer. The candidate choices below are the research shortlist; the [consolidated product review](agent-product-review.md) selects the final recommended ordering after inspecting the runtime.

## Existing capability boundary

The repository currently embeds only `code-review`, `verify`, and `adversarial-review`. Each has a generic prompt and an output schema of `{}`, accepting any valid JSON. None includes a model, connector, or execution authority. See [builtin.go](../definitions/builtin.go) and the [catalog reference](../definitions/README.md).

The current YAML engine can express fixed sequences and parallel branches that converge on one result. It passes whole JSON values, or ordered arrays of those values. It does not expose conditions, dynamic loops, approval/resume nodes, or selectors. No production workflows are registered automatically. See the [workflow reference](../workflows/README.md).

Approved remote MCP tools can expand an agent's capabilities, but the profile and agent must both select the approved connection. Native shell and filesystem tools remain denied. Broker network identity and authorization are deployment responsibilities. See [setup and ownership](agent-automations.md).

All agent and workflow names below are recommendations, not shipped features. An agent that analyzes supplied evidence can fit the current runtime without new tools. A package that fetches evidence or performs actions also needs a working integration and its deployment configuration.

## What the primary sources establish

1. **GitHub custom agents combine specialization with tool configuration.** GitHub documents agent profiles with a description, instructions, tool selection, and MCP configuration. Its example is a README specialist. Profiles can be shared at repository, organization, and enterprise scope. This supports distributing focused, reusable packages with explicit capability requirements. It does not prove that any particular specialist is more accurate than a general agent. [About custom agents](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-custom-agents).

2. **Claude Code distinguishes investigation from action.** Its documentation lists read-only Explore and Plan agents separately from a general-purpose agent with broader tools. It provides custom code reviewer, debugger, data scientist, and read-only database examples. Each subagent has separate context, a prompt, and tool constraints. This supports labeling our catalog by actual access and expected output, with separate analysis and action packages. These Claude features are not available merely because this repository accepts `SKILL.md` content. [Create custom subagents](https://code.claude.com/docs/en/sub-agents).

3. **Fixed chains and parallel review are useful first workflow patterns.** Anthropic describes prompt chaining, routing, parallelization, dynamic orchestration, and evaluator/optimizer loops. It recommends the simplest effective implementation and says evaluation loops fit tasks with clear criteria and measurable improvement. This supports starting with this repository's existing fixed DAG, then adding conditional or iterative behavior only for a demonstrated task. The article also emphasizes execution or tool results as evidence of actual progress. [Building effective agents](https://www.anthropic.com/engineering/building-effective-agents).

4. **Human review needs persisted execution state and resume semantics.** LangGraph documents interrupts that save state through a checkpointer, expose a payload to the caller, and resume using the same thread ID. It documents approval, editing generated output, and reviewing tool calls. It explicitly warns that code before the interruption can execute again when the node resumes. The product implication is to design approval and retry behavior together with idempotent actions. This is an architectural comparison, not a recommendation to replace Hatchet. [Interrupts](https://docs.langchain.com/oss/python/langgraph/interrupts).

5. **Other workflow frameworks make state and outcomes inspectable.** CrewAI documents structured state, persisted flows, conditional routing, human feedback, and aggregate token usage across a flow. These are useful references for product behavior beyond a successful enqueue request. The current page redirected to version 1.15.26 during research. This does not imply every capability should become a field in our YAML format. [Flows](https://docs.crewai.com/en/concepts/flows).

6. **Approval can be attached to specific tools.** n8n documents selective approval of AI tool calls, showing the proposed tool and parameters through channels such as chat or Slack. Approval executes the proposed action; denial cancels it. This supports an eventual reviewable publication step, especially for sending messages or changing external records. That is distinct from a model producing a JSON field named `approved`. [Human-in-the-loop for tools](https://docs.n8n.io/build/integrate-ai/ai-examples/human-in-the-loop-for-tools).

All six pages were fetched successfully as live documentation with `maxAge: 0`. Documentation changes over time; these are observations from the research date, not compatibility commitments. Some original URLs redirected to the canonical URLs above.

## Candidate agent catalog

The groups describe the minimum capability needed for the promised result. A read integration can improve an input-only agent, but it must remain honest about absent or incomplete evidence.

### Useful on supplied evidence

1. **`spec-review`** takes a brief, acceptance criteria, and constraints. It returns ambiguities, contradictions, missing decisions, and testable acceptance criteria. It should ask for missing information instead of inventing product decisions.
2. **`test-plan`** takes requirements, a change description or diff, and existing tests. It returns prioritized cases with expected behavior and a mapping to requirements. It proposes tests; it does not claim to execute them.
3. **`ci-diagnose`** takes failure logs, revision metadata, and relevant source excerpts. It returns observed failures, ranked causes with supporting evidence, and the next discriminating check. A missing log or file produces a request for evidence, not a confident diagnosis.
4. **`release-notes`** takes merged changes, issue summaries, audience, and release metadata. It returns a draft grouped by user-visible behavior, breaking changes, and required actions, linked to the supplied evidence. Publishing remains separate.
5. **`issue-triage`** takes an issue, a taxonomy, and optional candidate duplicates. It returns a recommended category, rationale, missing reproduction information, and duplicate candidates only from supplied evidence. It does not set labels or assign people.
6. **`security-review`** takes code, trust boundaries, and dependency context. It returns evidence-backed findings, exploit prerequisites, suggested mitigations, and unexamined areas. Treat this as a scoped review, not a security certification.
7. **`docs-draft`** takes a change, existing documentation, and intended audience. It returns draft edits, examples, and claims that need verification. It should preserve commands and APIs from evidence rather than fabricate runnable examples.
8. **`evidence-extract`** takes a document and a task-specific schema. It returns structured facts with source spans and explicit missing fields. This is useful beyond software, including intake forms and operational reports. Deterministic parsing should handle data that does not require a model.
9. **`support-draft`** takes a customer question, approved knowledge excerpts, and response policy. It returns a cited response draft or an escalation reason. It cannot read customer accounts, promise refunds, or send the reply without additional access.

### Require read integrations for the advertised experience

10. **`change-impact`** searches source, call sites, tests, and configuration to identify affected consumers and verification targets. It needs a repository reader tied to a revision. A diff-only version must describe its narrower coverage.
11. **`research-brief`** searches and reads approved sources, then returns a cited answer with contradictory evidence and open questions. It needs search/fetch tools and source provenance. A summarizer over uploaded documents is a separate, smaller promise.
12. **`incident-investigate`** correlates an incident window with logs, metrics, deployments, and a runbook. It returns a timeline, hypotheses, and proposed checks. It needs bounded observability queries and deployment metadata; remediation is a separate action.

### Require execution or write integrations

13. **`test-repair`** reproduces a failure, changes a test or implementation, reruns relevant checks, and returns a patch with actual run evidence. It needs repository writes and a test runner. It must distinguish an implementation defect from an incorrect test.
14. **`dependency-upgrade`** edits dependency manifests and lockfiles, checks migration notes, runs validation, and returns a reviewable patch. It needs registry/docs reads, controlled package execution, and repository writes.
15. **`implement-change`** applies a bounded approved specification and returns a patch plus verification evidence. It needs an isolated writable checkout and execution tools. Publishing a PR adds a further credentialed integration.

## Five new packages to launch first

Start with `spec-review`, `test-plan`, `ci-diagnose`, `release-notes`, and `issue-triage`, alongside stronger packaged versions of the existing three agents. This is a prioritization judgment based on current runtime fit, observable outputs, and low connector requirements. It is not a demand ranking.

All five can demonstrate useful behavior on a checked-in input fixture. They cover common engineering handoffs and can later gain GitHub or CI input adapters without changing their core task. A customer should be able to try each with supplied data before connecting a repository or granting write access.

For each package, ship:

- A name that promises one outcome, instructions, supported input fields, an output schema, and failure behavior for missing evidence.
- A sample input and expected result shape, including an example where the correct answer is that evidence is insufficient.
- A readable rendering of the structured result and evidence links.
- A declared capability class: supplied input, read integrations, or action integrations. Missing required capabilities should be detected before a model run.
- Evaluation fixtures, with checks for fabricated evidence, omitted required findings, and schema violations. Task-specific checks matter more than whether JSON parses.
- A package version, tested model choices, and observed latency and token usage from evaluations. These require measurements before making performance claims.

The current catalog has an output schema field but no input schema field. Initially, package documentation and fixtures can define the expected input. Enforced input validation is a product addition.

## Three packaged workflows that fit the current engine

These graphs use fixed steps and whole-value handoffs. They need authored packages and example data, but do not require dynamic branching. A verifier must receive the original evidence as well as generated claims.

1. **Change review.** Run `code-review`, `adversarial-review`, and `test-plan` independently on the supplied change packet, then pass `[input, review, adversarial, tests]` to a verifier configured for the result contract. Return deduplicated findings and suggested verification, with uncertain claims separate from confirmed ones. A PR URL input and automatic posting are later integration features.

2. **Issue to implementation brief.** Run `issue-triage`, then `spec-review` on `[input, triage]`, then `test-plan` on `[input, spec]`, then verify `[input, triage, spec, tests]`. Return a clear brief, acceptance criteria, missing decisions, and proposed tests. Missing information should remain in the result. This declarative package has no wired interaction flow for asking the reporter and resuming. Native permission/form waits exist underneath, but the customer-facing interaction still needs an integration.

3. **Release digest.** Draft `release-notes`, review the draft with `adversarial-review` on `[input, draft]`, then verify `[input, draft, review]`. Return the supported final draft and any unresolved factual gaps. State explicitly that approval and publication are outside this first package.

Specialize the final verifier's output contract for each workflow instead of expecting the generic `{}` schema to produce a stable product response. Keep the single-agent variant available where evaluations show that a longer workflow adds cost without useful improvement.

## Product gaps suggested by this comparison

These are recommendations grounded in the capability boundaries above. The broader repository review should confirm which adjacent adapters or operational tools already cover part of each requirement.

- **A working first result.** Provide a template command that copies an agent/workflow package, checks prerequisites, runs a sample, and renders its output. The current docs lead with configuration and enqueueing a run.
- **Task contracts.** Add input validation and useful output schemas, rather than relying on any JSON value being a good handoff. Name handoff fields when expanding the format; ordered arrays make package composition harder to inspect.
- **Connectors customers can actually configure.** A broker URL and tool allowlist do not supply repository checkout, CI logs, search, or credentials. Offer a few supported adapters with a connection test, explicit scope, and sample data.
- **Run visibility.** Users need step status, readable intermediate and final outputs, evidence, errors, token usage, and cancellation. Existing durable snapshots are a useful base for explaining which configuration produced a run.
- **Conditional outcomes and human decisions.** Add deterministic gates and a persisted approval/resume contract before claiming full automated triage, repair, or publication workflows. An LLM can recommend a branch today; the YAML engine cannot select it.
- **Action reliability.** Define deduplication keys, revision checks, retry rules, and the exact payload an approval authorizes. A retried agent step should not create duplicate comments or overwrite a newer revision.
- **Quality evidence for presets.** Evaluate whether a package solves its named task and whether additional reviewers help. Document limitations and retain failing examples. A larger catalog is not evidence of better results.

The strongest initial product promise is repeatable, evidence-backed engineering analysis with reviewable structured results. A general autonomous coding product requires repository and execution integrations that these presets alone cannot provide.
