// Code generated from openapi.json; DO NOT EDIT.
package opencode

import (
	"context"
	"net/http"
)

// AgentGet invokes GET /api/agent/{agentID}. The caller owns the response body.
func (c *Client) AgentGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/agent/{agentID}", args)
}

// AgentList invokes GET /api/agent. The caller owns the response body.
func (c *Client) AgentList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/agent", args)
}

// CommandList invokes GET /api/command. The caller owns the response body.
func (c *Client) CommandList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/command", args)
}

// ConfigGet invokes GET /api/config. The caller owns the response body.
func (c *Client) ConfigGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/config", args)
}

// ConfigShells invokes GET /api/config/shell. The caller owns the response body.
func (c *Client) ConfigShells(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/config/shell", args)
}

// CredentialActivate invokes POST /api/credential/{credentialID}/activate. The caller owns the response body.
func (c *Client) CredentialActivate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/credential/{credentialID}/activate", args)
}

// CredentialRemove invokes DELETE /api/credential/{credentialID}. The caller owns the response body.
func (c *Client) CredentialRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/credential/{credentialID}", args)
}

// CredentialUpdate invokes PATCH /api/credential/{credentialID}. The caller owns the response body.
func (c *Client) CredentialUpdate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PATCH", "/api/credential/{credentialID}", args)
}

// DebugLocationEvict invokes DELETE /api/debug/location. The caller owns the response body.
func (c *Client) DebugLocationEvict(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/debug/location", args)
}

// DebugLocationList invokes GET /api/debug/location. The caller owns the response body.
func (c *Client) DebugLocationList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/debug/location", args)
}

// EventSubscribe invokes GET /api/event. The caller owns the response body.
func (c *Client) EventSubscribe(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/event", args)
}

// ExperimentalConfigUpdate invokes PATCH /api/experimental/config. The caller owns the response body.
func (c *Client) ExperimentalConfigUpdate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PATCH", "/api/experimental/config", args)
}

// ExperimentalFsWrite invokes POST /api/experimental/fs/write. The caller owns the response body.
func (c *Client) ExperimentalFsWrite(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/fs/write", args)
}

// ExperimentalGenerateText invokes POST /api/experimental/generate. The caller owns the response body.
func (c *Client) ExperimentalGenerateText(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/generate", args)
}

// ExperimentalIntegrationWellknownAdd invokes POST /api/experimental/integration/wellknown. The caller owns the response body.
func (c *Client) ExperimentalIntegrationWellknownAdd(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/integration/wellknown", args)
}

// ExperimentalMcpAdd invokes PUT /api/experimental/mcp/{server}. The caller owns the response body.
func (c *Client) ExperimentalMcpAdd(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PUT", "/api/experimental/mcp/{server}", args)
}

// ExperimentalMcpConnect invokes POST /api/experimental/mcp/{server}/connect. The caller owns the response body.
func (c *Client) ExperimentalMcpConnect(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/mcp/{server}/connect", args)
}

// ExperimentalMcpDisconnect invokes POST /api/experimental/mcp/{server}/disconnect. The caller owns the response body.
func (c *Client) ExperimentalMcpDisconnect(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/mcp/{server}/disconnect", args)
}

// ExperimentalMcpRemove invokes DELETE /api/experimental/mcp/{server}. The caller owns the response body.
func (c *Client) ExperimentalMcpRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/experimental/mcp/{server}", args)
}

// ExperimentalMigrationV1Status invokes GET /api/experimental/migration/v1. The caller owns the response body.
func (c *Client) ExperimentalMigrationV1Status(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/migration/v1", args)
}

// ExperimentalSessionExport invokes GET /api/experimental/session/{sessionID}/export. The caller owns the response body.
func (c *Client) ExperimentalSessionExport(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/session/{sessionID}/export", args)
}

// ExperimentalSessionImport invokes POST /api/experimental/session/import. The caller owns the response body.
func (c *Client) ExperimentalSessionImport(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/session/import", args)
}

// ExperimentalSessionInstructionsEntryList invokes GET /api/experimental/session/{sessionID}/instructions/entries. The caller owns the response body.
func (c *Client) ExperimentalSessionInstructionsEntryList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/session/{sessionID}/instructions/entries", args)
}

// ExperimentalSessionInstructionsEntryPut invokes PUT /api/experimental/session/{sessionID}/instructions/entries/{key}. The caller owns the response body.
func (c *Client) ExperimentalSessionInstructionsEntryPut(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PUT", "/api/experimental/session/{sessionID}/instructions/entries/{key}", args)
}

// ExperimentalSessionInstructionsEntryRemove invokes DELETE /api/experimental/session/{sessionID}/instructions/entries/{key}. The caller owns the response body.
func (c *Client) ExperimentalSessionInstructionsEntryRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/experimental/session/{sessionID}/instructions/entries/{key}", args)
}

// ExperimentalSessionSkill invokes POST /api/experimental/session/{sessionID}/skill. The caller owns the response body.
func (c *Client) ExperimentalSessionSkill(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/session/{sessionID}/skill", args)
}

// ExperimentalSessionStats invokes GET /api/experimental/session/stats. The caller owns the response body.
func (c *Client) ExperimentalSessionStats(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/session/stats", args)
}

// ExperimentalSessionWait invokes POST /api/experimental/session/{sessionID}/wait. The caller owns the response body.
func (c *Client) ExperimentalSessionWait(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/session/{sessionID}/wait", args)
}

// FormList invokes GET /api/form. The caller owns the response body.
func (c *Client) FormList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/form", args)
}

// FsFind invokes GET /api/fs/find. The caller owns the response body.
func (c *Client) FsFind(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/fs/find", args)
}

// FsList invokes GET /api/fs/list. The caller owns the response body.
func (c *Client) FsList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/fs/list", args)
}

// FsRead invokes GET /api/fs/read/*. The caller owns the response body.
func (c *Client) FsRead(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/fs/read/*", args)
}

// IntegrationCommandCancel invokes DELETE /api/integration/{integrationID}/connect/command/{attemptID}. The caller owns the response body.
func (c *Client) IntegrationCommandCancel(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/integration/{integrationID}/connect/command/{attemptID}", args)
}

// IntegrationCommandConnect invokes POST /api/integration/{integrationID}/connect/command. The caller owns the response body.
func (c *Client) IntegrationCommandConnect(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/integration/{integrationID}/connect/command", args)
}

// IntegrationCommandStatus invokes GET /api/integration/{integrationID}/connect/command/{attemptID}. The caller owns the response body.
func (c *Client) IntegrationCommandStatus(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/integration/{integrationID}/connect/command/{attemptID}", args)
}

// IntegrationConnectKey invokes POST /api/integration/{integrationID}/connect/key. The caller owns the response body.
func (c *Client) IntegrationConnectKey(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/integration/{integrationID}/connect/key", args)
}

// IntegrationGet invokes GET /api/integration/{integrationID}. The caller owns the response body.
func (c *Client) IntegrationGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/integration/{integrationID}", args)
}

// IntegrationList invokes GET /api/integration. The caller owns the response body.
func (c *Client) IntegrationList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/integration", args)
}

// IntegrationOauthCancel invokes DELETE /api/integration/{integrationID}/connect/oauth/{attemptID}. The caller owns the response body.
func (c *Client) IntegrationOauthCancel(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/integration/{integrationID}/connect/oauth/{attemptID}", args)
}

// IntegrationOauthComplete invokes POST /api/integration/{integrationID}/connect/oauth/{attemptID}/complete. The caller owns the response body.
func (c *Client) IntegrationOauthComplete(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/integration/{integrationID}/connect/oauth/{attemptID}/complete", args)
}

// IntegrationOauthConnect invokes POST /api/integration/{integrationID}/connect/oauth. The caller owns the response body.
func (c *Client) IntegrationOauthConnect(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/integration/{integrationID}/connect/oauth", args)
}

// IntegrationOauthStatus invokes GET /api/integration/{integrationID}/connect/oauth/{attemptID}. The caller owns the response body.
func (c *Client) IntegrationOauthStatus(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/integration/{integrationID}/connect/oauth/{attemptID}", args)
}

// LocationGet invokes GET /api/location. The caller owns the response body.
func (c *Client) LocationGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/location", args)
}

// LocationReload invokes POST /api/location/reload. The caller owns the response body.
func (c *Client) LocationReload(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/location/reload", args)
}

// McpList invokes GET /api/mcp. The caller owns the response body.
func (c *Client) McpList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/mcp", args)
}

// McpResourceCatalog invokes GET /api/mcp/resource. The caller owns the response body.
func (c *Client) McpResourceCatalog(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/mcp/resource", args)
}

// ModelDefault invokes GET /api/model/default. The caller owns the response body.
func (c *Client) ModelDefault(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/model/default", args)
}

// ModelList invokes GET /api/model. The caller owns the response body.
func (c *Client) ModelList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/model", args)
}

// PermissionRequestList invokes GET /api/permission/request. The caller owns the response body.
func (c *Client) PermissionRequestList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/permission/request", args)
}

// PermissionSavedList invokes GET /api/permission/saved. The caller owns the response body.
func (c *Client) PermissionSavedList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/permission/saved", args)
}

// PermissionSavedRemove invokes DELETE /api/permission/saved/{id}. The caller owns the response body.
func (c *Client) PermissionSavedRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/permission/saved/{id}", args)
}

// PersistentPtyConnect invokes GET /api/experimental/persistent-pty/{ptyID}/connect. The caller owns the response body.
func (c *Client) PersistentPtyConnect(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/persistent-pty/{ptyID}/connect", args)
}

// PluginCheck invokes POST /api/plugin/check. The caller owns the response body.
func (c *Client) PluginCheck(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/plugin/check", args)
}

// PluginList invokes GET /api/plugin. The caller owns the response body.
func (c *Client) PluginList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/plugin", args)
}

// PluginUpdate invokes POST /api/plugin/update. The caller owns the response body.
func (c *Client) PluginUpdate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/plugin/update", args)
}

// ProjectList invokes GET /api/project. The caller owns the response body.
func (c *Client) ProjectList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/project", args)
}

// ProjectUpdate invokes PATCH /api/project/{projectID}. The caller owns the response body.
func (c *Client) ProjectUpdate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PATCH", "/api/project/{projectID}", args)
}

// ProviderGet invokes GET /api/provider/{providerID}. The caller owns the response body.
func (c *Client) ProviderGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/provider/{providerID}", args)
}

// ProviderList invokes GET /api/provider. The caller owns the response body.
func (c *Client) ProviderList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/provider", args)
}

// PtyConnect invokes GET /api/pty/{ptyID}/connect. The caller owns the response body.
func (c *Client) PtyConnect(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/pty/{ptyID}/connect", args)
}

// PtyConnectToken invokes POST /api/pty/{ptyID}/connect-token. The caller owns the response body.
func (c *Client) PtyConnectToken(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/pty/{ptyID}/connect-token", args)
}

// PtyCreate invokes POST /api/pty. The caller owns the response body.
func (c *Client) PtyCreate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/pty", args)
}

// PtyGet invokes GET /api/pty/{ptyID}. The caller owns the response body.
func (c *Client) PtyGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/pty/{ptyID}", args)
}

// PtyList invokes GET /api/pty. The caller owns the response body.
func (c *Client) PtyList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/pty", args)
}

// PtyRemove invokes DELETE /api/pty/{ptyID}. The caller owns the response body.
func (c *Client) PtyRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/pty/{ptyID}", args)
}

// PtyUpdate invokes PUT /api/pty/{ptyID}. The caller owns the response body.
func (c *Client) PtyUpdate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PUT", "/api/pty/{ptyID}", args)
}

// ReferenceList invokes GET /api/reference. The caller owns the response body.
func (c *Client) ReferenceList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/reference", args)
}

// RpcCall invokes POST /api/rpc/{rpcID}/{method}. The caller owns the response body.
func (c *Client) RpcCall(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/rpc/{rpcID}/{method}", args)
}

// ServerExperimentalPersistentPtyConnectToken invokes POST /api/experimental/persistent-pty/{ptyID}/connect-token. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyConnectToken(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/persistent-pty/{ptyID}/connect-token", args)
}

// ServerExperimentalPersistentPtyCreate invokes POST /api/experimental/session/{sessionID}/terminal. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyCreate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/session/{sessionID}/terminal", args)
}

// ServerExperimentalPersistentPtyGet invokes GET /api/experimental/persistent-pty/{ptyID}. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/persistent-pty/{ptyID}", args)
}

// ServerExperimentalPersistentPtyHandoff invokes POST /api/experimental/persistent-pty/handoff. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyHandoff(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/persistent-pty/handoff", args)
}

// ServerExperimentalPersistentPtyList invokes GET /api/experimental/session/{sessionID}/terminal. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/session/{sessionID}/terminal", args)
}

// ServerExperimentalPersistentPtyRead invokes GET /api/experimental/session/{sessionID}/terminal/read. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyRead(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/session/{sessionID}/terminal/read", args)
}

// ServerExperimentalPersistentPtyRemove invokes DELETE /api/experimental/persistent-pty/{ptyID}. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/experimental/persistent-pty/{ptyID}", args)
}

// ServerExperimentalPersistentPtyShutdown invokes POST /api/experimental/persistent-pty/shutdown. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyShutdown(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/experimental/persistent-pty/shutdown", args)
}

// ServerExperimentalPersistentPtySnapshot invokes GET /api/experimental/persistent-pty/{ptyID}/snapshot. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtySnapshot(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/persistent-pty/{ptyID}/snapshot", args)
}

// ServerExperimentalPersistentPtyUpdate invokes PUT /api/experimental/persistent-pty/{ptyID}. The caller owns the response body.
func (c *Client) ServerExperimentalPersistentPtyUpdate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PUT", "/api/experimental/persistent-pty/{ptyID}", args)
}

// ServerInfo invokes GET /api/info. The caller owns the response body.
func (c *Client) ServerInfo(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/info", args)
}

// SessionActive invokes GET /api/session/active. The caller owns the response body.
func (c *Client) SessionActive(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/active", args)
}

// SessionBackground invokes POST /api/session/{sessionID}/background. The caller owns the response body.
func (c *Client) SessionBackground(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/background", args)
}

// SessionCommand invokes POST /api/session/{sessionID}/command. The caller owns the response body.
func (c *Client) SessionCommand(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/command", args)
}

// SessionCompact invokes POST /api/session/{sessionID}/compact. The caller owns the response body.
func (c *Client) SessionCompact(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/compact", args)
}

// SessionContext invokes GET /api/session/{sessionID}/context. The caller owns the response body.
func (c *Client) SessionContext(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/context", args)
}

// SessionCreate invokes POST /api/session. The caller owns the response body.
func (c *Client) SessionCreate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session", args)
}

// SessionDiff invokes GET /api/session/{sessionID}/diff. The caller owns the response body.
func (c *Client) SessionDiff(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/diff", args)
}

// SessionEnvironment invokes PUT /api/session/{sessionID}/environment. The caller owns the response body.
func (c *Client) SessionEnvironment(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PUT", "/api/session/{sessionID}/environment", args)
}

// SessionFork invokes POST /api/session/{sessionID}/fork. The caller owns the response body.
func (c *Client) SessionFork(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/fork", args)
}

// SessionFormCancel invokes DELETE /api/session/{sessionID}/form/{formID}. The caller owns the response body.
func (c *Client) SessionFormCancel(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/session/{sessionID}/form/{formID}", args)
}

// SessionFormCreate invokes POST /api/session/{sessionID}/form. The caller owns the response body.
func (c *Client) SessionFormCreate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/form", args)
}

// SessionFormGet invokes GET /api/session/{sessionID}/form/{formID}. The caller owns the response body.
func (c *Client) SessionFormGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/form/{formID}", args)
}

// SessionFormList invokes GET /api/session/{sessionID}/form. The caller owns the response body.
func (c *Client) SessionFormList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/form", args)
}

// SessionFormReply invokes POST /api/session/{sessionID}/form/{formID}/reply. The caller owns the response body.
func (c *Client) SessionFormReply(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/form/{formID}/reply", args)
}

// SessionGenerate invokes POST /api/session/{sessionID}/generate. The caller owns the response body.
func (c *Client) SessionGenerate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/generate", args)
}

// SessionGet invokes GET /api/session/{sessionID}. The caller owns the response body.
func (c *Client) SessionGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}", args)
}

// SessionInboxCancel invokes DELETE /api/session/{sessionID}/inbox/{inboxID}. The caller owns the response body.
func (c *Client) SessionInboxCancel(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/session/{sessionID}/inbox/{inboxID}", args)
}

// SessionInboxList invokes GET /api/session/{sessionID}/inbox. The caller owns the response body.
func (c *Client) SessionInboxList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/inbox", args)
}

// SessionInboxUpdate invokes PATCH /api/session/{sessionID}/inbox/{inboxID}. The caller owns the response body.
func (c *Client) SessionInboxUpdate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PATCH", "/api/session/{sessionID}/inbox/{inboxID}", args)
}

// SessionInterrupt invokes POST /api/session/{sessionID}/interrupt. The caller owns the response body.
func (c *Client) SessionInterrupt(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/interrupt", args)
}

// SessionList invokes GET /api/session. The caller owns the response body.
func (c *Client) SessionList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session", args)
}

// SessionLog invokes GET /api/experimental/session/{sessionID}/log. The caller owns the response body.
func (c *Client) SessionLog(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/experimental/session/{sessionID}/log", args)
}

// SessionMessageGet invokes GET /api/session/{sessionID}/message/{messageID}. The caller owns the response body.
func (c *Client) SessionMessageGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/message/{messageID}", args)
}

// SessionMessageList invokes GET /api/session/{sessionID}/message. The caller owns the response body.
func (c *Client) SessionMessageList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/message", args)
}

// SessionMove invokes POST /api/session/{sessionID}/move. The caller owns the response body.
func (c *Client) SessionMove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/move", args)
}

// SessionPermissionCreate invokes POST /api/session/{sessionID}/permission. The caller owns the response body.
func (c *Client) SessionPermissionCreate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/permission", args)
}

// SessionPermissionGet invokes GET /api/session/{sessionID}/permission/{requestID}. The caller owns the response body.
func (c *Client) SessionPermissionGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/permission/{requestID}", args)
}

// SessionPermissionList invokes GET /api/session/{sessionID}/permission. The caller owns the response body.
func (c *Client) SessionPermissionList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/session/{sessionID}/permission", args)
}

// SessionPermissionReply invokes POST /api/session/{sessionID}/permission/{requestID}/reply. The caller owns the response body.
func (c *Client) SessionPermissionReply(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/permission/{requestID}/reply", args)
}

// SessionPrompt invokes POST /api/session/{sessionID}/prompt. The caller owns the response body.
func (c *Client) SessionPrompt(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/prompt", args)
}

// SessionRemove invokes DELETE /api/session/{sessionID}. The caller owns the response body.
func (c *Client) SessionRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/session/{sessionID}", args)
}

// SessionRevertClear invokes DELETE /api/session/{sessionID}/revert. The caller owns the response body.
func (c *Client) SessionRevertClear(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/session/{sessionID}/revert", args)
}

// SessionRevertCommit invokes POST /api/session/{sessionID}/revert/commit. The caller owns the response body.
func (c *Client) SessionRevertCommit(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/revert/commit", args)
}

// SessionRevertStage invokes POST /api/session/{sessionID}/revert/stage. The caller owns the response body.
func (c *Client) SessionRevertStage(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/revert/stage", args)
}

// SessionShell invokes POST /api/session/{sessionID}/shell. The caller owns the response body.
func (c *Client) SessionShell(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/shell", args)
}

// SessionSwitchAgent invokes POST /api/session/{sessionID}/agent. The caller owns the response body.
func (c *Client) SessionSwitchAgent(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/agent", args)
}

// SessionSwitchModel invokes POST /api/session/{sessionID}/model. The caller owns the response body.
func (c *Client) SessionSwitchModel(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/model", args)
}

// SessionSynthetic invokes POST /api/session/{sessionID}/synthetic. The caller owns the response body.
func (c *Client) SessionSynthetic(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/synthetic", args)
}

// SessionUpdate invokes PATCH /api/session/{sessionID}. The caller owns the response body.
func (c *Client) SessionUpdate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "PATCH", "/api/session/{sessionID}", args)
}

// SessionView invokes POST /api/session/{sessionID}/view. The caller owns the response body.
func (c *Client) SessionView(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/session/{sessionID}/view", args)
}

// ShellCreate invokes POST /api/shell. The caller owns the response body.
func (c *Client) ShellCreate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/shell", args)
}

// ShellGet invokes GET /api/shell/{id}. The caller owns the response body.
func (c *Client) ShellGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/shell/{id}", args)
}

// ShellList invokes GET /api/shell. The caller owns the response body.
func (c *Client) ShellList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/shell", args)
}

// ShellOutput invokes GET /api/shell/{id}/output. The caller owns the response body.
func (c *Client) ShellOutput(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/shell/{id}/output", args)
}

// ShellRemove invokes DELETE /api/shell/{id}. The caller owns the response body.
func (c *Client) ShellRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/shell/{id}", args)
}

// SkillList invokes GET /api/skill. The caller owns the response body.
func (c *Client) SkillList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/skill", args)
}

// VcsBase invokes GET /api/vcs/base. The caller owns the response body.
func (c *Client) VcsBase(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/vcs/base", args)
}

// VcsBranchList invokes GET /api/vcs/branch. The caller owns the response body.
func (c *Client) VcsBranchList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/vcs/branch", args)
}

// VcsDiff invokes GET /api/vcs/diff. The caller owns the response body.
func (c *Client) VcsDiff(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/vcs/diff", args)
}

// VcsGet invokes GET /api/vcs. The caller owns the response body.
func (c *Client) VcsGet(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/vcs", args)
}

// VcsStatus invokes GET /api/vcs/status. The caller owns the response body.
func (c *Client) VcsStatus(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/vcs/status", args)
}

// WebsearchProviders invokes GET /api/websearch/provider. The caller owns the response body.
func (c *Client) WebsearchProviders(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/websearch/provider", args)
}

// WebsearchQuery invokes POST /api/websearch. The caller owns the response body.
func (c *Client) WebsearchQuery(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/websearch", args)
}

// WorktreeCreate invokes POST /api/worktree. The caller owns the response body.
func (c *Client) WorktreeCreate(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/worktree", args)
}

// WorktreeList invokes GET /api/worktree. The caller owns the response body.
func (c *Client) WorktreeList(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "GET", "/api/worktree", args)
}

// WorktreeRefresh invokes POST /api/worktree/refresh. The caller owns the response body.
func (c *Client) WorktreeRefresh(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "POST", "/api/worktree/refresh", args)
}

// WorktreeRemove invokes DELETE /api/worktree. The caller owns the response body.
func (c *Client) WorktreeRemove(ctx context.Context, args Arguments) (*http.Response, error) {
	return c.Do(ctx, "DELETE", "/api/worktree", args)
}
