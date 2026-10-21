#!/bin/bash
# Hook: PreToolUse
# Fires before every tool call. Receives the tool input as JSON on stdin.
#
# IMPORTANT: This hook fires on EVERY tool call. Keep it fast and silent.
# All informational messages go to stderr (>&2). Stdout is consumed by Claude.
#
# Exit codes:
#   0 — allow the tool call to proceed
#   2 — block the tool call (Claude sees your stderr output as the reason)

set -euo pipefail

# Read stdin (tool input JSON is always passed here)
input=$(cat)
tool_name=$(echo "$input" | jq -r '.tool_name // empty' 2>/dev/null || true)

# Example: block dangerous rm commands
# command=$(echo "$input" | jq -r '.tool_input.command // empty' 2>/dev/null || true)
# if [ "$tool_name" = "Bash" ] && echo "$command" | grep -q 'rm -rf /'; then
#   echo "Blocked: dangerous rm command" >&2
#   exit 2
# fi

exit 0
