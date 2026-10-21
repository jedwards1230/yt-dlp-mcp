#!/bin/bash
# Hook: SessionEnd
# Fires once when a session terminates (not on resume).
#
# IMPORTANT: All informational messages go to stderr (>&2). This hook cannot
# block session end — its exit code is ignored. Use it for cleanup only.

set +e  # Never fail cleanup

# Read stdin (session info is passed here)
input=$(cat)

# Example: clean up temp files created during the session
# rm -rf /tmp/my-project-scratch 2>/dev/null || true

echo "[session-end] Done" >&2

exit 0
