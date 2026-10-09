package definitions

import "fmt"

// NativeTools returns permission actions supported by the pinned OpenCode runtime.
// The edit action grants edit, write, and patch; shell grants command execution.
func NativeTools() []string {
	return []string{"read", "glob", "grep", "edit", "shell", "webfetch"}
}

func validateNativeTools(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if !contains(NativeTools(), name) || seen[name] {
			return fmt.Errorf("unsupported or repeated native tool action: %s", name)
		}
		seen[name] = true
	}
	return nil
}

const compiledNativePolicy = "opencode-v2.0.26:authored-primary:exact-native:no-title:v4"
const compiledNativeSchemaPolicy = "opencode-v2.0.26:authored-primary:exact-native:no-title:output-contract:v4"
const compiledNativeMCPPolicy = "opencode-v2.0.26:authored-primary:exact-native-mcp:ready-no-title:v4"
const compiledNativeMCPSchemaPolicy = "opencode-v2.0.26:authored-primary:exact-native-mcp:ready-no-title:output-contract:v4"

func (snapshot Snapshot) nativePolicy() string {
	if len(snapshot.MCPServers) > 0 {
		if len(snapshot.Agent.Schema) > 0 {
			return compiledNativeMCPSchemaPolicy
		}
		return compiledNativeMCPPolicy
	}
	if len(snapshot.Agent.Schema) > 0 {
		return compiledNativeSchemaPolicy
	}
	return compiledNativePolicy
}

func (snapshot Snapshot) guidesOutput() bool {
	switch snapshot.CompiledPolicy {
	case compiledSchemaPolicy, compiledMCPSchemaPolicy, compiledNativeSchemaPolicy, compiledNativeMCPSchemaPolicy:
		return true
	default:
		return false
	}
}
