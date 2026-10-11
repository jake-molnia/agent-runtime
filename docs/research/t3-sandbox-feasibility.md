# T3 sandbox feasibility

The completed review and recommended design are in [T3 on Agent Sandbox: reviewed architecture](t3-sandbox-architecture.md). That document supersedes the earlier feasibility drafts.

The fixed design keeps one central T3 server and its existing SQLite data on persistent storage. It extends this repository's agent-runtime and Hatchet workflows for Kubernetes Agent Sandbox lifecycle. A small T3 execution worker runs providers and workspace services in each allocation. New top-level conversations receive distinct workspaces/sandboxes; delegated descendants inherit their parent's sandbox with distinct conversation and MCP identities.

The full report includes a 20-item source-backed blocker register, precise ownership and recovery rules, retained-volume policy, provider/MCP coverage, feature compatibility, and staged acceptance tests. The [original runtime source audit](agent-runtime-t3-fit.md) remains supporting evidence, not a separate architecture recommendation.
