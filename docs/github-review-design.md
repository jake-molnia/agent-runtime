# Specialist review, publication, and separate security investigation

Decision date: 2026-10-14.

## Review completes on GitHub

Deployments select the runtime-owned pack:

```yaml
# workflows/review-agents.yaml
use: pr-review
```

The GitHub integration selects the final verifier as the only source of publishable findings:

```yaml
candidate_steps: [verify]
writeup_step: writeup
```

The graph already runs general, adversarial, security, and dependency investigations independently. All four reports and the original PR context reach `pr-verifier`. Its findings pass through triage and writing before trusted publication. Triage groups duplicate issues; it cannot discard verified findings. The writer covers every verified source index exactly once, and the reducer derives the location and highest priority from those sources.

Review ends when publication completes. The user's babysit workflow can resume from GitHub and dispatch a separate future fix workflow. Review should not start a fix agent, push commits, or wait for a repair. A future fix workflow needs its own checkout, authorization, validation, and stale-head handling. GitHub is the handoff, not an implicit call from the writer.

Sources: [bundled graph](../workflows/presets/pr-review.yaml), [verification prompt](../definitions/builtins/pr-verifier/instructions.md), [trusted reduction](../githubreview/reduction.go), [publication](../githubreview/publication.go).

## Clean comments with details on demand

Cursor's [Bugbot demo](https://cursor.com/bugbot) uses concrete consequence titles and short explanations. Adopt that presentation approach without copying branding or adding editor/fix buttons that our system does not implement.

A fictional example, not a finding in this repository:

> **[P2] Route preferences leak between users**
>
> When one request sets `avoidTolls`, later requests inherit that preference. `Object.assign` modifies the shared defaults rather than building a new options object. Copy the defaults before applying request options.

An optional collapsed Details section can hold a focused reproduction and its observed result, a causal trace, or decisive documentation context. The main paragraph must remain understandable without opening it. The trusted publisher appends a permalink at the reviewed commit; off-diff findings keep their existing source-link heading.

The narrow contract addition is an optional `details` string on `FindingWording`, bounded to 1,800 characters like the explanation. The existing assessment, source indices, title, and explanation remain required. Existing writeups without details remain valid. The writer returns content without HTML wrappers; trusted code adds the disclosure element and title formatting. Explanation and details remain GitHub Markdown, subject to GitHub's renderer and sanitization. They are not safe HTML for arbitrary embedding elsewhere.

This field organizes supplied evidence. It does not prove a command executed. The writer must distinguish actual results from proposed checks and preserve conditions, versions, and source URLs. Exact logs, rejected hypotheses, and operational details remain private; the assessment should briefly identify a material gap that prevents a meaningful conclusion. Enforcing public coverage disclosure or execution receipts requires a separate evidence contract.

Implementation remains in the existing boundaries:

- `definitions/builtins/pr-writer/instructions.md` defines concise wording and when details help.
- `definitions/schemas/review-writeup.schema.json` bounds the optional content.
- `githubreview.Reduce(reports, writeup)` validates source coverage and renders the title and disclosure wrapper.
- `reviewRequests(input, key, summary, findings, lines)` handles pinned links, placement, chunking, and publication markers.
- `examples/github-review` selects the pack and supplies model/MCP overrides without copied prompts or schemas.

The example now runs seven agent stages rather than the old example's four. The pack already had those stages; selecting it increases example execution work. Its model assignments are examples, not an accuracy or cost recommendation.

## Deepsec is a useful starting point for a separate workflow

Vercel's [deepsec repository](https://github.com/vercel-labs/deepsec) describes a source-investigation system with pattern scanning, agent investigation, revalidation, persistent findings, exports, and optional sandbox fan-out. Its [PR mode](https://github.com/vercel-labs/deepsec/blob/main/docs/reviewing-changes.md) supports `process --diff` and explicit file lists. Changed files without pattern matches still receive an investigation. The [revalidator](https://github.com/vercel-labs/deepsec/blob/main/docs/architecture.md) classifies findings as true positive, false positive, fixed, or uncertain.

Deepsec is not a guarantee of an executed exploit or a comprehensive deployed-application penetration test. Its full scans target old vulnerabilities across large repositories. The README explicitly notes that those scans can cost thousands or tens of thousands of dollars. PR-scoped investigation is the appropriate starting point for per-PR use; whole-repository sweeps belong on a separate schedule or explicit request.

Recommended security workflow, still a design rather than an installed integration:

1. Resolve the pinned base/head and the changed attack surface. Read the service's trust boundaries, supported deployment model, and existing mitigations. Record the tested scope.
2. Run a pinned Deepsec version in a disposable worker with trusted configuration outside the PR checkout. Select changed files from the pinned comparison and allow investigation of supporting code. Do not let PR-authored executable Deepsec configuration or plugins inherit worker credentials.
3. Build a disposable instance of the affected application where practical, using fixture data and synthetic identities. Reproduce relevant authorization, tenant-isolation, injection, or state-transition failures against that instance. Use source traces when runtime reproduction is unavailable and label that evidence accurately.
4. Verify each candidate independently against both revisions and existing protections. A finding newly discovered by Deepsec is not automatically a vulnerability introduced by the PR. Its net-new finding accounting refers to recorded findings, so our verifier still needs the base comparison.
5. Export a structured report and bounded reproduction artifacts. Distinguish reproduced, source-supported, uncertain, and blocked investigations. A failed app setup is a blocked test, not evidence of a clean security review.
6. Publish an appropriate summary through a trusted publisher. Preserve detailed security artifacts in an access-controlled destination when they contain sensitive reproduction material. Hand any eventual remediation to the separate fix workflow.

Execution belongs inside the disposable application environment, with explicit target scope, controlled egress, test accounts, and runtime/cost limits. This task did not run Deepsec or attack any application. The current `pr-security` specialist continues source review and local experiments on each review.

Before enabling independent security publication, fix one integration constraint: `reviewKey` and completion markers are keyed by repository, PR, and head, intentionally independent of graph revision. A security run and normal review can therefore suppress one another. Introduce a stable review-kind identity, such as code review versus security review, through admission, records, publication markers, and legacy reconciliation. Keep graph revisions out of that identity so changing configuration does not repost a completed review. PR serialization may remain shared while completion is per kind. Migration and retry tests are required before rollout.

## Synthesis and verification

Two designs were compared: minimal adoption/presentation changes using existing report types, and a richer evidence schema with execution receipts and structured coverage gaps. The minimal design is the base because the requested graph already exists and GitHub publication already owns the relevant invariants. The one addition from the structured approach is the optional bounded details field, which lets the renderer own disclosure markup. Full evidence variants, receipt validation, and public coverage policy are deferred until the security/benchmark contracts justify them.

An independent design judge also favored the minimal base with bounded optional details. Its main objection to the larger design was migration scope, and its main caution for the chosen design was that presentation prose cannot attest execution. The judgment covered the design documents; implementation validation came from the focused tests and CLI checks.

Both alternatives preserve the existing separation between agent prose and trusted publication. Neither design justifies a second graph copied into deployment configuration, or an automatic fix phase attached to review. The richer evidence design has a stronger long-term audit contract, but introduces a coordinated schema migration that is unnecessary for concise comments.

Focused tests cover the actual example's resolved graph and tool selection, verified-only publication input, formatting, optional details, size bounds, pinned links, and existing retry/reconciliation behavior. These tests establish wiring and rendering, not review accuracy. The separate benchmark planning session owns the evaluation design across all workflows and model choices.
