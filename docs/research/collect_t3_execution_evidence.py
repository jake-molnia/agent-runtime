#!/usr/bin/env python3
"""Read-only source audit for the T3 sandbox architecture. Prints JSON evidence."""
import argparse
import json
from pathlib import Path
import re
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--t3', type=Path, required=True)
parser.add_argument('--runtime', type=Path, default=Path(__file__).resolve().parents[2])
parser.add_argument('--agent-sandbox', type=Path, required=True)
args = parser.parse_args()
checks = [
 ('sqlite-wal', 't3', 'apps/server/src/persistence/Sqlite.ts', r'journal_mode = WAL'),
 ('central-shutdown', 't3', 'apps/server/src/serverRuntimeStartup.ts', r'Effect.ensuring\(providerSessions.shutdown\)'),
 ('recover-before-worker', 't3', 'apps/server/src/serverRuntimeStartup.ts', r'const recovery = yield\* input.recover'),
 ('mcp-loopback', 't3', 'apps/server/src/mcp/McpSessionRegistry.ts', r'127\.0\.0\.1'),
 ('mcp-memory-state', 't3', 'apps/server/src/mcp/McpSessionRegistry.ts', r'records: new Map'),
 ('lineage-fork-subagent', 't3', 'packages/contracts/src/orchestrationV2.ts', r'relationshipToParent:.*fork.*subagent'),
 ('claude-sdk', 't3', 'apps/server/src/orchestration-v2/Adapters/ClaudeAdapterV2.ts', r'@anthropic-ai/claude-agent-sdk'),
 ('cursor-sdk', 't3', 'apps/server/src/orchestration-v2/Adapters/CursorAdapterV2.ts', r'@cursor/sdk'),
 ('muse-sdk', 't3', 'apps/server/src/provider/museSdk.ts', r'spawnMspConnection'),
 ('native-file-paths', 't3', 'apps/server/src/workspace/WorkspaceFileSystem.ts', r'NodeFSP.realpath'),
 ('native-search', 't3', 'apps/server/src/workspace/WorkspaceSearchIndex.ts', r'FileFinder.create'),
 ('native-pty', 't3', 'apps/server/src/terminal/NodePtyAdapter.ts', r'nodePty.spawn'),
 ('browser-localhost', 't3', 'apps/server/src/preview/ServerBrowser.ts', r'localhost:.*port'),
 ('checkpoint-ref-update', 't3', 'apps/server/src/vcs/GitVcsDriver.ts', r'update-ref'),
 ('settle-detached', 't3', 'apps/server/src/orchestration-v2/ThreadSettlementService.ts', r'run.completion.pipe\(Effect.forkDetach\)'),
 ('blocking-requests-recovery', 't3', 'apps/server/src/orchestration-v2/ProviderRuntimeRecoveryService.ts', r'responseCapability.type !== "message"'),
 ('hatchet-run-identity', 'runtime', 'hatchetbridge/bridge.go', r'input.Run.Key = ctx.WorkflowRunId'),
 ('hatchet-run-deadline', 'runtime', 'hatchetbridge/bridge.go', r'24\*time.Hour'),
 ('suspend-no-memory', 'runtime', 'sandbox/control.go', r'memory state is not preserved'),
 ('sandbox-pvc-ownership', 'agent_sandbox', 'controllers/sandbox_controller.go', r'SetControllerReference\(sandbox, pvc'),
 ('sandbox-pod-spec', 'agent_sandbox', 'controllers/sandbox_controller.go', r'PodTemplate.Spec.DeepCopy'),
 ('claim-warm-exclusions', 'agent_sandbox', 'extensions/controllers/sandboxclaim_controller.go', r'len\(claim.Spec.(Env|VolumeClaimTemplates)\)'),
]
roots = {'t3': args.t3, 'runtime': args.runtime, 'agent_sandbox': args.agent_sandbox}
results = []
for ident, root, relative, pattern in checks:
    path = roots[root] / relative
    matches = []
    if path.is_file():
        lines = path.read_text().splitlines()
        for i, line in enumerate(lines):
            if re.search(pattern, line):
                matches.append({'line': i+1, 'text': line.strip()})
    results.append({'id': ident, 'root': root, 'path': relative, 'matches': matches})
revision = subprocess.check_output(['git', '-C', str(args.t3), 'rev-parse', 'HEAD'], text=True).strip()
print(json.dumps({'t3_revision': revision, 'checks': results}, indent=2))
raise SystemExit(1 if any(not item['matches'] for item in results) else 0)
