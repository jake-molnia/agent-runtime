#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
AGENT_RUNTIME_TEST_OPENCODE_BINARY= go test ./...
AGENT_RUNTIME_TEST_OPENCODE_BINARY= go test -race ./definitions ./workflows ./notebooks ./packs ./hatchetbridge ./agentexec ./command
go vet ./...

task_verify_dir=$(mktemp -d)
trap 'rm -rf "$task_verify_dir"' EXIT
go build -trimpath -o "$task_verify_dir/agent-runtime" ./cmd/agent-runtime
mkdir "$task_verify_dir/config"
cp examples/definitions/deployment.yaml "$task_verify_dir/config/deployment.yaml"
export AGENT_DEFINITIONS_DIR="$task_verify_dir/config"
"$task_verify_dir/agent-runtime" workflows templates > "$task_verify_dir/templates"
while IFS= read -r preset; do
  "$task_verify_dir/agent-runtime" workflows init "$preset" "$preset"
done < "$task_verify_dir/templates"
"$task_verify_dir/agent-runtime" agents validate
"$task_verify_dir/agent-runtime" workflows inspect daily-brief > "$task_verify_dir/brief.json"
python3 - "$task_verify_dir/brief.json" <<'PY'
import json
import sys

plan = json.load(open(sys.argv[1]))
assert plan['workflow']['notebook'] is True
assert plan['workflow']['input']['brief']
assert len(plan['workflow']['steps']) == 1
assert plan['agents']['work']['agent']['name'] == 'daily-brief'
print('CLI starter → catalog → pinned workflow verified')
PY

if [[ -n "${AGENT_RUNTIME_TEST_OPENCODE_BINARY:-}" ]]; then
  go test ./command -run '^TestOpenCodeNative' -count=1 -v
else
  echo 'Native checks skipped: set AGENT_RUNTIME_TEST_OPENCODE_BINARY to OpenCode 2.0.26.'
fi
