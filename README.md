# template-repo

Starter template for Claude Code-ready repositories. Provides a minimal,
opinionated scaffold that works in both local devcontainers and Claude Code Web
(ephemeral containers).

## What's Included

```
.claude/
  settings.json          # Hook wiring + minimal read-only permissions
  hooks/
    session-start.sh     # Detects Claude Code Web; add tool installs here
    pre-tool-use.sh      # Runs before every tool call (block/validate)
    post-tool-use.sh     # Runs after every tool call (auto-format)
    stop.sh              # Runs when Claude finishes a response (lint/test)
    subagent-stop.sh     # Runs when a subagent finishes
    session-end.sh       # Runs on session end (cleanup)
.devcontainer/
  devcontainer.json      # VS Code devcontainer config
  entrypoint.sh          # Runs on container start (secrets, deps, git config)
  Dockerfile             # Base image with git, curl, jq, yq, ca-certificates
.github/workflows/
  ci.yml                 # Test + lint (language-agnostic stub) + template-placeholders gate
  build.yml              # Docker build + push to GHCR
  release.yml            # Semver release (manual trigger)
docs/
  PRD.md                 # Purpose, users, requirements, non-goals, open questions
  TESTING.md             # Test-layer table + anti-patterns to avoid
.gitignore               # Common patterns + .claude/settings.local.json
.pre-commit-config.yaml  # trailing-whitespace, end-of-file, check-yaml, etc.
CLAUDE.md                # Project instructions for Claude Code — a map, not a manual
CONTRIBUTING.md          # Build, test, lint, PR, and release process
README.md                # This file
```

## Quick Setup

1. Copy this template into your new repo
2. Fill in `CLAUDE.md`, `CONTRIBUTING.md`, `docs/PRD.md`, and `docs/TESTING.md`
   with project-specific content — each has `TODO(template)` markers where a
   real answer belongs (see [Placeholder Gate](#placeholder-gate) below)
3. Edit `.devcontainer/Dockerfile` to add your language toolchain
4. Edit `.devcontainer/entrypoint.sh` to install dependencies and fetch secrets
5. Edit `.claude/hooks/session-start.sh` to install tools in Claude Code Web
6. Install pre-commit (`pip install pre-commit`) then run `pre-commit install` to enable git hooks

## Hook Overview

All hooks are unconfigured stubs. They exit 0 and print a notice. Replace the
body of each script with your project logic.

| Hook | When it fires | Common use |
|------|--------------|------------|
| `session-start.sh` | Fresh session only (not resume) | Install CLI tools in web sessions |
| `pre-tool-use.sh` | Before every tool call | Block dangerous commands |
| `post-tool-use.sh` | After every tool call | Auto-format modified files |
| `stop.sh` | When Claude finishes responding | Run linters / tests |
| `subagent-stop.sh` | When a subagent finishes | Aggregate results |
| `session-end.sh` | Session terminates | Cleanup temp files |

## GitHub Actions Workflows

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| `ci.yml` | Push to main, PRs | Test + lint stub, placeholder gate, and gate job for branch protection |
| `build.yml` | Push to main, tags | Docker build + push to GHCR with metadata tags |
| `auto-release.yml` | Push to main | Reads `semver:*` label from merged PR, calls `release.yml` |
| `release.yml` | Called by `auto-release.yml` or `workflow_dispatch` | Compute semver, create tag + GitHub release |
| `claude-pr-review.yml` | PRs (opened, sync, ready, reopened) | AI code review via Claude |

The CI workflow's `test` job is a language-agnostic stub with commented
examples for Go, Node/Bun, and Python. Uncomment and customize the section you
need.

## Placeholder Gate

`ci.yml` has a `template-placeholders` job that greps all tracked files for
the `TODO(template)` marker and fails if any remain. It's skipped (not run)
in `jedwards1230/template-repo` itself, so the template stays green — but it
runs in every repo generated *from* the template, so CI stays red until every
`TODO(template)` in `CLAUDE.md`, `CONTRIBUTING.md`, `docs/PRD.md`, and
`docs/TESTING.md` (and this README, once you've written a real one for your
project) has been replaced with real content.

Use the same marker for any new placeholder you add so the gate keeps
catching it.

## Releases

Releases are opt-in via PR labels. Label a pull request `semver:patch`,
`semver:minor`, or `semver:major` before merging. When the PR merges to
`main`, `auto-release.yml` reads the label and triggers a versioned release.
A merge with no `semver:*` label defaults to a patch bump, so an unlabeled
merge may still produce a release — add a `skip-release` label (or simply
don't merge) to avoid one.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, lint, PR, and release instructions.

## Claude Code Web

The `session-start.sh` hook checks `CLAUDE_CODE_REMOTE=true` to detect
ephemeral web containers and run one-time tool installation.
