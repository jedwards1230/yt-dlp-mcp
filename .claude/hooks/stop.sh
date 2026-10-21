#!/bin/bash
# Hook: Stop
# Fires when Claude finishes responding to a user turn.
#
# IMPORTANT: All informational messages go to stderr (>&2). Stdout is
# consumed by Claude.
#
# Exit codes:
#   0 — allow the stop
#   2 — block the stop (Claude sees your stderr output as the reason and
#       keeps going)

set -euo pipefail

# Read stdin (session/transcript info is passed here)
input=$(cat)

# Example: run the linter/tests and block the stop on failure
# if ! golangci-lint run 2>/tmp/lint.log; then
#   echo "Lint failed — fix before stopping:" >&2
#   cat /tmp/lint.log >&2
#   exit 2
# fi

exit 0
