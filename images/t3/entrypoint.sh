#!/bin/sh
set -eu
umask 077
mkdir -p "$HOME" "$CODEX_HOME" "$T3_WORKER_STATE_DIR"
/usr/local/bin/t3-configure-git
exec node /opt/t3/dist/execution-worker.mjs
