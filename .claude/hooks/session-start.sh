#!/bin/bash
# Hook: SessionStart
# Fires once when a fresh Claude Code session begins (not on resume).
#
# Primary use: detect the runtime environment and install any tools that
# are not present in the container image. In Claude Code Web, each session
# starts from a clean ephemeral container, so tools must be installed here.
#
# IMPORTANT: All informational messages go to stderr (>&2).
# Only structured JSON should go to stdout (consumed by Claude as context).
# Always exit 0 — never block the session from starting.

set +e  # Never exit on error in session-start

# Detect Claude Code Web vs local devcontainer
if [ "${CLAUDE_CODE_REMOTE:-}" = "true" ]; then
  echo "[session-start] Running in Claude Code Web (ephemeral container)" >&2

  # TODO: Install tools needed for this project in web sessions.
  # Tools installed in the Dockerfile are NOT available in web sessions.
  # Examples:
  #   # Install Go
  #   GO_VERSION=1.24.3
  #   curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -xz -C /usr/local
  #   export PATH="/usr/local/go/bin:/root/go/bin:$PATH"
  #
  #   # Install yq
  #   YQ_VERSION=v4.52.4
  #   curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64" \
  #     -o /usr/local/bin/yq && chmod +x /usr/local/bin/yq

  echo "[session-start] TODO: add tool installation logic for web sessions" >&2
else
  echo "[session-start] Running in local devcontainer — tools pre-installed" >&2
fi

echo "[session-start] Done" >&2

# Always exit 0 — never block the session from starting
exit 0
