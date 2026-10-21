#!/bin/bash
# Devcontainer entrypoint — runs on every container start via postStartCommand.
#
# Keep this script idempotent (safe to run multiple times).
# This script validates that tools are installed and prints a health summary.

set -euo pipefail

echo "=== Devcontainer Health Check ==="

# ---------------------------------------------------------------------------
# Tool version validation
# Verify tools installed by the Dockerfile are present and print versions.
# ---------------------------------------------------------------------------
check_tool() {
  local name="$1"
  local cmd="$2"
  if version=$($cmd 2>/dev/null); then
    printf "  %-18s %s\n" "$name" "$version"
  else
    printf "  %-18s %s\n" "$name" "NOT FOUND"
  fi
}

echo "Tools:"
check_tool "git" "git --version"
check_tool "jq" "jq --version"
check_tool "yq" "yq --version"
# TODO: Add project-specific tool checks:
# check_tool "go" "go version"
# check_tool "node" "node --version"
# check_tool "bun" "bun --version"
# check_tool "python3" "python3 --version"
# check_tool "golangci-lint" "golangci-lint --version"

# ---------------------------------------------------------------------------
# Secrets setup
# TODO: fetch secrets from a vault (1Password, AWS Secrets Manager, etc.)
#
# Example with 1Password CLI:
#   if command -v op &>/dev/null && [ -n "${OP_SERVICE_ACCOUNT_TOKEN:-}" ]; then
#     echo "Fetching secrets from 1Password..."
#     export MY_API_KEY=$(op read "op://vault/item/field")
#   fi
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Dependency installation
# TODO: install project dependencies
#
# Examples:
#   go mod download
#   npm install
#   pip install -r requirements.txt
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Shell aliases
# Source project shell aliases if they exist
# ---------------------------------------------------------------------------
if [ -f "$(dirname "$0")/../scripts/shell-aliases.sh" ]; then
  # shellcheck disable=SC1090
  source "$(dirname "$0")/../scripts/shell-aliases.sh"
fi

echo "=== Health Check Complete ==="
