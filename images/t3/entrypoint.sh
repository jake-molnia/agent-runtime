#!/bin/sh
set -eu
umask 077
mkdir -p "$HOME" "$CODEX_HOME" "$T3_WORKER_STATE_DIR"
/usr/local/bin/t3-configure-git
exec agent-runtime t3-session
