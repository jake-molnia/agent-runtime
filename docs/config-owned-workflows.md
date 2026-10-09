# Config ownership

The default workflow authoring path is data-only YAML, documented in
[generic configuration](agent-automations.md). No custom Go worker, input-builder
functions, field selectors or domain-specific argument mapping is required.

The runtime ships generic default agents, not production workflows. Your config
repo selects defaults or overrides them, chooses models/approved MCP connections,
and supplies `workflows/<name>.yaml`. The worker resolves and registers those
files automatically. A verify agent accepts arbitrary input; its instructions and
actual authorized tools determine what it can verify or publish.

The config source examined at `a1cc652afa6e995a2e714277b361e371a83137c0` contained
reviewer, verifier, adversarial and writer roles under
`packages/agent-tasks/agent_tasks/workflows/pr_review`. Its application-specific
candidate routing, source-index reduction, model choices and publishing decisions
are not copied into the generic runtime or its default agents. Config can express
its desired graph with named steps and generic scalar/list inputs, and define
custom agents for application-specific formatting or reduction.

The Go `RegisterMessageDAG` API remains available to trusted adapter authors for
advanced integration. It is not a requirement for normal workflow configuration.
Publishing/repository/test brokers are trusted integrations and must enforce real
authorization; prompt text and capability names cannot provide missing tools.

No config-repo deployment or production-agent migration is claimed by runtime
unit/native smoke tests. Files in that repo remain externally owned.
