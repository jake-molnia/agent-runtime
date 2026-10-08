# Tailnet deployment ownership

Workers require `AGENT_DEPLOYMENT_ID` when `TAILSCALE_CLIENT_ID` is set or any agent definition has tailnet tags. Missing ownership configuration prevents worker startup.

Use a stable, non-secret ID such as `production-us-east-agent-runtime`. Each independent deployment sharing a tailnet needs a unique ID, including deployments in different Kubernetes clusters. Replicas of one deployment need the same ID and the same complete set of namespaces in `AGENT_DEFINITIONS_FILE`. Keep the ID stable across restarts and credential rotation. OAuth client IDs, the shared `tag:agent-sandbox` tag, and Kubernetes namespace names alone do not identify a deployment.

`tailnet.NewScope` hashes the deployment ID and the sorted, deduplicated namespace set into a 24-character hexadecimal scope. Device hostnames use `ar-<scope>-<32-character claim hash>`, which fits the 63-character hostname limit. No additional Tailscale policy tags are required. The scope is an ownership convention for trusted workers, not an authorization boundary against malicious tailnet administrators.

`Issue` accepts a claim name and returns the scoped hostname used for sandbox enrollment. Direct cleanup uses the persisted scoped hostname. When cancellation reconstructs only a claim name, `Engine.Cleanup` derives the same scoped hostname before revocation. `Revoke` deletes a device only when its hostname belongs to the client's scope and it carries `tag:agent-sandbox`.

The reaper lists claims in every configured namespace before inspecting devices. A failed namespace list, continuation token, empty namespace set, or invalid claim name prevents deletion. A nil claim map means unknown inventory; a non-nil empty map means a complete empty inventory. Only locally scoped devices with the application tag and a known creation time at least five minutes old can be reaped. Active claims protect their scoped devices.

Changing the namespace set changes the scope. This preserves devices whose claims are outside the new inventory, even if those devices were created by an earlier configuration of the same deployment. Reordering or duplicating namespaces does not change ownership.

## Upgrade existing deployments

1. Stop old workers that run the unscoped reaper before starting upgraded workers.
2. Set a unique `AGENT_DEPLOYMENT_ID` for each deployment and distribute the same value and namespace configuration to all its replicas.
3. Start upgraded workers. New identities receive scoped hostnames.
4. After independently confirming device ownership and that the associated runs have ended, manually remove legacy `ar-<32-character claim hash>` devices. The upgraded reaper and direct cleanup preserve these ambiguous devices. A persisted auth-key ID can still be revoked without deleting a legacy device.

If you change the deployment ID or namespace set, devices with the previous scope also require separate cleanup after their runs finish. Do not infer that those devices are stale from the new worker's claim inventory. Claims and workflow state retain their existing names and structure.

For library callers, set `Client.Scope` using `NewScope`, pass raw claim names to `Issue` and `Reap`, and provide a complete claim inventory for exactly the namespaces used to construct the scope. Use `Client.Hostname` when reconstructing a device hostname. Missing or malformed scopes cause identity issuance, revocation, and reaping to fail before API requests.
