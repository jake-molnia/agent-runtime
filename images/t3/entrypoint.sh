#!/bin/sh
set -eu
umask 077
mkdir -p "$HOME" "$CODEX_HOME" "$T3_WORKER_STATE_DIR"
for directory in "$HOME/.agents" "$HOME/.claude"; do
    mkdir -p "$directory"
    if [ ! -e "$directory/skills" ] && [ ! -L "$directory/skills" ]; then
        ln -s /opt/agent-skills "$directory/skills"
    fi
done
/usr/local/bin/t3-configure-git
exec agent-runtime t3-session
