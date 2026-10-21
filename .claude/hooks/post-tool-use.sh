#!/bin/bash
# Hook: PostToolUse
# Fires after every tool call. Receives the tool output as JSON on stdin.
#
# IMPORTANT: This hook fires on EVERY tool call. Keep it fast and silent.
# All informational messages go to stderr (>&2). Stdout is consumed by Claude.
#
# Example: auto-format Go files after Edit
#   if [ "$tool_name" = "Edit" ] && [[ "$file_path" == *.go ]]; then
#     gofmt -w "$file_path" 2>/dev/null || true
#   fi

set -euo pipefail

# Read stdin (tool output JSON is always passed here)
output=$(cat)
tool_name=$(echo "$output" | jq -r '.tool_name // empty' 2>/dev/null || true)
file_path=$(echo "$output" | jq -r '.tool_input.file_path // .tool_input.path // empty' 2>/dev/null || true)

# Example: auto-format Go files after Edit/Write
# if [[ "$tool_name" =~ ^(Edit|Write)$ ]] && [[ "$file_path" == *.go ]]; then
#   gofmt -w "$file_path" 2>/dev/null || true
# fi

exit 0
