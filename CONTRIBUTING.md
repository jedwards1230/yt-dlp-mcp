# Contributing to yt-dlp-mcp

yt-dlp-mcp is a read-only MCP server in Go that runs yt-dlp for video search, metadata,
subtitle listings and transcripts, shipped as a container image and a Helm chart. All
changes go through the workflow below.

## Prerequisites

- Go at the version in `go.mod` (currently `go 1.26.0`; CI installs it from `go.mod`)
- [golangci-lint](https://golangci-lint.run/) v2 (CI pins v2.14.0)
- [Helm](https://helm.sh/) 3.8 or newer, for the chart
- Docker with BuildKit, i.e. the `buildx` plugin, to build the image. The legacy builder
  cannot build this Dockerfile: it uses cache mounts and `$BUILDPLATFORM`.
- [pre-commit](https://pre-commit.com/)

Unit tests need neither yt-dlp nor network access. Running the server outside Docker
also needs yt-dlp and yt-dlp-ejs (`pip install -r requirements.txt`, ideally in a
virtualenv) and [Deno](https://deno.com) on `PATH`.

## Build, test & lint

The commands CI runs, by job (`.github/workflows/ci.yml`):

```bash
# Test
go mod verify
go test -race -coverprofile=coverage.out -covermode=atomic ./...

# Lint
go vet ./... && go vet -tags live ./...
golangci-lint run   # also fails on any file gofmt would change
go mod tidy && git diff --exit-code go.mod go.sum
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /dev/null ./cmd/yt-dlp-mcp

# Helm chart
helm lint --strict deploy/helm/yt-dlp-mcp
helm template yt-dlp-mcp deploy/helm/yt-dlp-mcp > /dev/null
helm template yt-dlp-mcp deploy/helm/yt-dlp-mcp \
  --set networkPolicy.enabled=true \
  --set 'networkPolicy.ingressFrom[0].namespaceSelector.matchLabels.team=agents' \
  --set 'networkPolicy.egress.internet.exceptCIDRs={10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,169.254.0.0/16}' \
  --set 'networkPolicy.egress.internet.ports[0].port=443' \
  --set 'networkPolicy.egress.internet.ports[0].protocol=TCP' \
  --set auth.existingSecret=yt-dlp-mcp-token \
  --set 'config.allowedHosts={www.youtube.com,youtu.be}' \
  > rendered.yaml
grep -q 'kind: NetworkPolicy' rendered.yaml
grep -q 'name: YTDLP_MCP_AUTH_TOKEN' rendered.yaml
rm rendered.yaml   # local scratch file; never commit it

# pre-commit
pre-commit run --all-files --show-diff-on-failure

# Docker build (CI builds linux/amd64 only, then runs the image's smoke step)
docker buildx build --load --platform linux/amd64 --build-arg BUILD_VERSION=dev -t yt-dlp-mcp:ci .
```

The Docker job's smoke step runs the image with `--read-only --tmpfs /tmp --cap-drop ALL`
and checks that it runs as UID 65532, that `yt-dlp --version` equals the pin in
`requirements.txt`, that `deno` is present, and that `/healthz` answers. Run the live
smoke test below for the full path through every tool. A `Template placeholders` job
also fails if any tracked file outside `ci.yml` still holds the placeholder marker of
the repository template this project started from.

`go test ./...` never compiles the live smoke test, but both `go vet -tags live` and
golangci-lint (`build-tags: [live]` in `.golangci.yml`) do, so it must keep building.

Go code is formatted with gofmt, simplify on (`gofmt -s`). The `formatters` section of
`.golangci.yml` enables it, so `golangci-lint run`, and with it the Lint job, fails on a
file gofmt would change; `golangci-lint fmt` rewrites such files in place.

### Live smoke test

Not part of CI: it needs Docker and network access to YouTube. It starts the built
image hardened (read-only root, tmpfs `/tmp`, no capabilities, a random bearer token),
completes a real MCP handshake over streamable HTTP, calls all four tools against
public videos (one of them with only a named manual English track) and a video id that
does not exist, and checks from the debug log that every yt-dlp run was read-only. Run
it before merging a change to yt-dlp flags, the image, `requirements.txt`, or
transcript conversion:

```bash
docker buildx build --load -t yt-dlp-mcp:smoke .
go test -tags live -count=1 -v ./test/live/
```

All of its settings are optional environment variables (defined at the top of
`test/live/live_test.go`):

| Variable | Default | Purpose |
|---|---|---|
| `YTDLP_MCP_LIVE_IMAGE` | `yt-dlp-mcp:smoke` | Image to start. |
| `YTDLP_MCP_LIVE_URL` | unset | MCP endpoint of an already-running server, e.g. `http://localhost:8080/mcp`. No container is started, and the image and argv checks are skipped. |
| `YTDLP_MCP_LIVE_TOKEN` | unset | That server's bearer token, if it has one. |
| `YTDLP_MCP_LIVE_VIDEO` | a long-lived public talk | Public video id with manual English subtitles and automatic captions. |
| `YTDLP_MCP_LIVE_NAMED` | `UF8uR6Z6KLc` | A public video whose manual English track is a named track (`en-<track id>`) with no plain `en` track. |
| `YTDLP_MCP_LIVE_MISSING` | `aaaaaaaaaaa` | 11-character id of a video that does not exist. |
| `YTDLP_MCP_LIVE_QUERY` | `ted talk creativity schools` | Search query. |

What each part of the test proves is in [docs/TESTING.md](docs/TESTING.md).

## Documentation

Keep documentation current as part of the change, not as a follow-up — update the README
and any affected docs in the same PR. In particular:

- a new or changed `YTDLP_MCP_*` variable, flag, tool, tool parameter, output field or
  error message → [docs/CONTRACT.md](docs/CONTRACT.md), the README's configuration
  table, and the Helm chart (`values.yaml` and the templates) as it applies;
- a change to how a package works → its package doc comment, the "spoke" doc for that
  package, in the same PR;
- an intended change to transcript output → regenerate the golden files as described in
  [docs/TESTING.md](docs/TESTING.md) and explain the diff in the PR.

## Before you open a PR

- Make sure all CI checks pass locally first — run the formatter (`golangci-lint fmt`),
  the linter, and the tests.
- Run `pre-commit run --all-files` (this repo uses pre-commit hooks).
- For changes to yt-dlp flags, the image, `requirements.txt` or transcript conversion,
  run the live smoke test.

## Branching & commits

- Branch off `main`; never commit directly to `main`.
- Use [Conventional Commits](https://www.conventionalcommits.org/) prefixes (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`, …).
- Sign your commits where possible (`git commit -S`).
- Keep each PR focused; delete dead code rather than commenting it out.

## Pull requests

- Open the PR against `main`, as a draft while you iterate.
- Every PR runs CI; the final `CI` job passes only when every other job did, and is the
  check to wait for.
- An automated code review runs on each PR; address and resolve its threads like any
  other review. It skips draft PRs: mark the PR ready for review once CI is green. A
  review with blocking findings may turn the PR back into a draft; push the fixes,
  resolve the threads, and mark it ready again for a fresh review.
- Resolve **all** review threads before the PR is merged.
- A PR can be merged once CI is green and all review threads are resolved.

## Releases

Merging to `main` runs [`.github/workflows/release.yml`](.github/workflows/release.yml),
which decides from the merged PR whether to release:

1. A `semver:patch`, `semver:minor` or `semver:major` label always wins and cuts that
   release, even when the diff is dev-only. `semver:none` skips the release.
2. With no label, a PR whose changed files all match
   [`.github/.releaseignore`](.github/.releaseignore) (CI and agent config,
   `CONTRIBUTING.md`, `docs/`, tests and fixtures, linter config) releases nothing.
   Anything else releases as a patch: Go source, `go.mod`, `Dockerfile`,
   `requirements.txt`, the chart, and also `README.md` and `LICENSE`. Label a PR
   `semver:none` when it touches those but should not ship on its own. When the merged
   PR, its labels or its file list cannot be read, the workflow falls back to a patch.
3. Until the first `vX.Y.Z` tag exists, nothing is released without a label.

A release tags `vX.Y.Z` with AI-generated notes, pushes the multi-arch image to
`ghcr.io/jedwards1230/yt-dlp-mcp` as `X.Y.Z` and `latest`, pushes chart version `X.Y.Z`
to `oci://ghcr.io/jedwards1230/charts/yt-dlp-mcp`, and publishes the GitHub Release. The
chart's `version` and `appVersion` are rewritten on the tagged commit only; the values on
`main` are not a published release. A maintainer can also run the workflow by hand
(`workflow_dispatch`) with a bump type, or as a dry run that previews the version and
notes without publishing. A real run must be dispatched from `main`; a dry run may come
from any branch, e.g. to preview a change to `release.yml`. Before the first `vX.Y.Z`
tag, a real dispatch with bump type `patch` fails instead of cutting v0.0.1: choose
`minor` for v0.1.0, or `major` (a dry run only warns).

Dependabot bumps of `yt-dlp` and `yt-dlp-ejs` in `requirements.txt` are product changes
and release a patch on merge. That is usually the point: a site change is fixed by a
yt-dlp release, and it reaches users as a new image.
