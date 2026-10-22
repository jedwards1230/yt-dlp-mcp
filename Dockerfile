# syntax=docker/dockerfile:1

# --- Go builder ---
# Runs natively on the build host and cross-compiles to $TARGETARCH, so a
# multi-arch build never runs the compiler under QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS go-builder
WORKDIR /src

ARG TARGETOS
ARG TARGETARCH

# Dependency layer: rebuilt only when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ cmd/
COPY internal/ internal/
ARG BUILD_VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X github.com/jedwards1230/yt-dlp-mcp/internal/version.Value=${BUILD_VERSION}" \
      -o /out/yt-dlp-mcp ./cmd/yt-dlp-mcp

# --- Deno ---
# yt-dlp needs a JavaScript runtime to solve YouTube's player challenges. The
# bin image is just the static binary; pinning it here lets Dependabot's
# docker ecosystem bump it.
FROM denoland/deno:bin-2.9.8 AS deno

# --- Runtime ---
FROM python:3.14.9-slim-trixie

# tini runs as PID 1 and reaps orphans: when a call times out the whole
# yt-dlp process group is killed, and a JavaScript runtime it spawned would
# otherwise linger as a zombie under the Go server.
RUN apt-get update \
    && apt-get install -y --no-install-recommends tini \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 nonroot \
    && useradd --uid 65532 --gid 65532 --home-dir /tmp --no-create-home --shell /usr/sbin/nologin nonroot

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --root-user-action=ignore -r /tmp/requirements.txt \
    && rm /tmp/requirements.txt

COPY --from=deno /deno /usr/local/bin/deno
COPY --from=go-builder /out/yt-dlp-mcp /usr/local/bin/yt-dlp-mcp

# Everything that writes goes to /tmp, so the image runs with a read-only
# root filesystem plus a writable /tmp (an emptyDir in Kubernetes, --tmpfs
# with docker). Each yt-dlp call additionally gets its own scratch directory
# under /tmp with HOME, caches and DENO_DIR pointed into it.
ENV HOME=/tmp \
    XDG_CACHE_HOME=/tmp/.cache \
    DENO_DIR=/tmp/.deno \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    YTDLP_MCP_ADDR=:8080

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/yt-dlp-mcp"]
