#!/usr/bin/env bash
# Link the Claude Code side of the orchestrator (agents + skills) into ~/.claude.
# Usage: ./install-claude.sh [--uninstall]
# Symlinks only; refuses to replace a real file that is not ours. Safe to re-run.
set -euo pipefail
here=$(cd -- "${BASH_SOURCE[0]%/*}" 2>/dev/null && pwd -P || pwd -P)
claude_home=${CLAUDE_CONFIG_DIR:-$HOME/.claude}
mkdir -p "$claude_home/agents" "$claude_home/skills"
link() { # link <source> <target>
  local src=$1 dst=$2
  if [[ -L $dst ]]; then
    [[ $(readlink "$dst") == "$src" ]] && { echo "ok      $dst"; return; }
    rm "$dst"
  elif [[ -e $dst ]]; then
    echo "SKIP    $dst exists and is not a symlink (move it aside to adopt ours)"; return
  fi
  ln -s "$src" "$dst" && echo "linked  $dst -> $src"
}
unlink_ours() { local dst=$1; if [[ -L $dst && $(readlink "$dst") == "$here"/* ]]; then rm "$dst" && echo "removed $dst"; fi; }
if [[ ${1:-} == "--uninstall" ]]; then
  for f in "$here"/claude/agents/*.md; do unlink_ours "$claude_home/agents/$(basename "$f")"; done
  for skill in orchestrator away; do unlink_ours "$claude_home/skills/$skill"; done
  exit 0
fi
for f in "$here"/claude/agents/*.md; do link "$f" "$claude_home/agents/$(basename "$f")"; done
for skill in orchestrator away; do link "$here/claude/skills/$skill" "$claude_home/skills/$skill"; done
echo "Claude Code side installed. Start the Anthropic-native conductor with:  claude --agent conductor"
