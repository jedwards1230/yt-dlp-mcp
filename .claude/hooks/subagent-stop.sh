#!/bin/bash
# Hook: SubagentStop
# Fires when a subagent (Task tool invocation) finishes.
#
# IMPORTANT: All informational messages go to stderr (>&2). Stdout is
# consumed by Claude.
#
# Exit codes:
#   0 — allow the stop
#   2 — block the stop (Claude sees your stderr output as the reason)

set -euo pipefail

# Read stdin (subagent transcript info is passed here)
input=$(cat)

# Example: aggregate subagent results into a shared log
# echo "$input" | jq -r '.transcript_path // empty' >> /tmp/subagent-runs.log

exit 0
