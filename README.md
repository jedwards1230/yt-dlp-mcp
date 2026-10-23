# yt-dlp-mcp

A read-only [MCP](https://modelcontextprotocol.io) server that lets AI agents search
YouTube and read video metadata, subtitle listings and clean transcripts. Every tool
call runs [yt-dlp](https://github.com/yt-dlp/yt-dlp) with a fixed, locked-down argument
list and returns small, structured, paginated results. Nothing is ever downloaded, and
there is no write surface.

It is one Go binary serving MCP over streamable HTTP on a single port, published as a
multi-arch container image (linux/amd64, linux/arm64) with yt-dlp, yt-dlp-ejs and Deno
inside, and as a Helm chart.

## Tools

| Tool | What it does |
|---|---|
| `search` | Searches YouTube and returns up to 25 videos with id, title, URL, and channel and duration when the listing has them. |
| `get_info` | Trimmed metadata for one video: title, channel, duration, upload date, description (first 1500 characters unless asked for all), chapters, counts, and which subtitle tracks exist. |
| `list_subtitles` | A video's subtitle languages, split into manual (uploaded) tracks and automatic captions, with the original-language automatic track flagged. |
| `get_transcript` | Subtitles as clean, de-duplicated plain text, optionally timestamped, paginated by caption line with `next_offset` and `has_more`. A manual track in `lang` wins over automatic captions, including YouTube's named tracks such as `en-eEY6OEpapPo`. |

URL arguments take a video URL on the server's host allowlist or a bare 11-character
YouTube video id. A typical flow is `search`, then `get_info` or `list_subtitles`, then
`get_transcript`, paging with `next_offset`. A failed call says what went wrong and what
to try next: an unavailable or nonexistent video, for example, comes back as an error
carrying yt-dlp's reason, not as empty metadata. Every input and output field is
specified in [docs/CONTRACT.md](docs/CONTRACT.md) §5, every error in §6.

## Quick start

### Docker

```bash
docker run --read-only --tmpfs /tmp -p 127.0.0.1:8080:8080 ghcr.io/jedwards1230/yt-dlp-mcp:<version>
```

`<version>` is a release number such as `0.1.0`; `latest` is also published. The
container runs as UID 65532 and needs nothing writable except `/tmp`. Publishing on
`127.0.0.1` keeps the endpoint on your machine: with no token configured it is open to
anyone who can reach it (see [Security model](#security-model)). Check it with
`curl http://localhost:8080/healthz`, which answers `ok`.

### Helm

```bash
helm install yt-dlp-mcp oci://ghcr.io/jedwards1230/charts/yt-dlp-mcp --version <version>
```

The chart version always equals the image version it deploys. The defaults are hardened
(non-root, read-only root filesystem, all capabilities dropped, no service-account
token) but leave `/mcp` unauthenticated and the NetworkPolicy off. Every `config.*`
value maps to one environment variable below, `auth.existingSecret` supplies the bearer
token, and the full list is in
[`deploy/helm/yt-dlp-mcp/values.yaml`](deploy/helm/yt-dlp-mcp/values.yaml). To try it
from your machine:

```bash
kubectl port-forward svc/yt-dlp-mcp 8080:8080
```

### From source

With Go, plus yt-dlp and yt-dlp-ejs (`pip install -r requirements.txt` pins the versions
the image uses) and [Deno](https://deno.com) on `PATH`:

```bash
go run ./cmd/yt-dlp-mcp -addr 127.0.0.1:8080
```

To build the image yourself, use BuildKit; the legacy `docker build` builder cannot
handle the Dockerfile's cache mounts and `$BUILDPLATFORM`:

```bash
docker buildx build --load -t yt-dlp-mcp .
```

## Connecting an MCP client

The endpoint is `http://localhost:8080/mcp`, streamable HTTP. Any MCP client that
supports streamable HTTP can use it; in clients configured with an `mcpServers` JSON
block it typically looks like this (field names vary slightly between clients):

```json
{
  "mcpServers": {
    "yt-dlp": {
      "type": "http",
      "url": "http://localhost:8080/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Drop `headers` when the server has no token. The server is stateless, so it issues no
session id and any replica can answer any request.

## Configuration

Environment variables; each one except the token also has a command-line flag
(`yt-dlp-mcp -h` lists them). Accepted ranges and the flag names are in
[docs/CONTRACT.md](docs/CONTRACT.md) §3. Invalid values stop the server at startup, with
every problem listed.

| Variable | Default | Meaning |
|---|---|---|
| `YTDLP_MCP_ADDR` | `:8080` | Listen address. |
| `YTDLP_MCP_YTDLP_PATH` | `yt-dlp` | The yt-dlp executable, by name on `PATH` or by path. The server refuses to start if it cannot find it. |
| `YTDLP_MCP_ALLOWED_HOSTS` | `youtube.com`, `www.youtube.com`, `m.youtube.com`, `music.youtube.com`, `youtu.be`, `youtube-nocookie.com`, `www.youtube-nocookie.com` | Comma-separated exact hostnames a URL argument may use. No wildcards: subdomains must be listed. Every YouTube input is fetched from `www.youtube.com`, so keep it on any custom list. |
| `YTDLP_MCP_TIMEOUT` | `60s` | Deadline for one tool call, including any wait for a free yt-dlp slot. Keep a gateway's timeout above it. |
| `YTDLP_MCP_MAX_OUTPUT_BYTES` | `16777216` (16 MiB) | Cap on yt-dlp's stdout (a run that exceeds it is stopped) and on a subtitle file it writes (a larger one is refused). |
| `YTDLP_MCP_MAX_CONCURRENCY` | `4` | yt-dlp processes allowed at once, shared by all tools. |
| `YTDLP_MCP_AUTH_TOKEN` | unset (no auth) | Bearer token required on `/mcp`. Environment only, so it never appears in a process list. Set but blank stops the server at startup, so an empty Secret cannot turn auth off; unset it to run without auth. |
| `YTDLP_MCP_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Logs are JSON on stderr; `debug` adds every yt-dlp command line. |
| `YTDLP_MCP_CACHE_TTL` | `10m` | How long a fetched transcript is kept, so paging through it runs yt-dlp once. The cache holds at most 32 transcripts and about 64 MiB of text. `0` disables it. |

Size memory by concurrency: a YouTube call peaks near 360 MiB, most of it Deno solving
YouTube's player challenge, so budget roughly 400 MiB per slot. The chart's default
memory limit, 2Gi, covers the default four slots plus the server and its cache; raise
it with `config.maxConcurrency`, or the pod is OOM-killed when every slot is busy.

## Security model

yt-dlp will fetch almost any URL it is given and runs a JavaScript runtime, so the
server treats it as untrusted machinery and fences it in:

- **Host allowlist, checked before anything runs.** Every `url` argument must be an
  `http` or `https` URL whose host is exactly on `YTDLP_MCP_ALLOWED_HOSTS`, or a bare
  YouTube video id. Other schemes (including yt-dlp pseudo-URLs such as `ytsearch:`),
  credentials in the URL, IP addresses and numeric shorthands, non-default ports,
  trailing-dot hosts, and percent-encoded or non-ASCII hosts are rejected before a
  process starts. yt-dlp gets a URL rebuilt from the parsed parts, never the raw input,
  and YouTube URLs are reduced to `https://www.youtube.com/watch?v=<id>`, so a playlist
  or channel never fans out into many fetches.
- **Never a shell.** yt-dlp is executed directly with an argument list. The URL or
  search query is always the last argument, after a `--` end-of-options marker, so it
  can never be read as a flag.
- **Read-only flags on every run:** `--skip-download`, `--ignore-config`,
  `--no-plugin-dirs`, `--no-remote-components`, `--no-cookies`,
  `--no-cookies-from-browser`, `--no-cache-dir`, and `--use-extractors default,-generic`,
  which turns off yt-dlp's generic "fetch any web page" extractor. Each call runs in its
  own scratch directory under `/tmp`, removed when the call ends (and at the next start
  if the server was killed mid-call), with a minimal environment that never contains the
  server's token.
- **Bounded.** One deadline covers the wait for a slot and the run itself; on timeout,
  cancellation or an output overflow the whole process group is killed, Deno included,
  and once yt-dlp exits the group is killed again so nothing it started outlives the
  call. Output is capped, concurrency is capped, and every response is bounded: at most
  25 search results, descriptions truncated by default, a compact automatic-caption
  list, and transcripts served in pages of up to 40000 bytes (a single longer caption
  line comes back alone).
- **Optional bearer token.** With `YTDLP_MCP_AUTH_TOKEN` set, `/mcp` requires
  `Authorization: Bearer <token>` (the scheme in any case) and answers `401` otherwise;
  the token is compared as a SHA-256 digest in constant time. `/healthz` is always open
  and reveals nothing. A set but blank value is a startup error, not "no auth". **With
  the variable unset, anyone who can reach the port can use the tools**, so the server
  must then sit behind something that decides who may: a loopback bind, an MCP gateway
  that authenticates its callers, a NetworkPolicy, or a combination.

### Egress and the NetworkPolicy

The allowlist decides what a caller can point yt-dlp at, not every connection yt-dlp
then makes: an extractor also talks to the site's API and media hosts, and yt-dlp follows
HTTP redirects. For the default YouTube hosts that is YouTube's own infrastructure. A
host you add is only as safe as wherever it can redirect to, so in Kubernetes enable the
chart's NetworkPolicy. Its egress rule allows `0.0.0.0/0` minus
`networkPolicy.egress.internet.exceptCIDRs`, which by default carves out the private
ranges `10.0.0.0/8`, `172.16.0.0/12` and `192.168.0.0/16` and the link-local
`169.254.0.0/16`: the backstop that keeps a redirect away from your internal network and
from cloud metadata endpoints. (With the generic extractor off, an added host also needs
a dedicated yt-dlp extractor to work at all.)

The policy is off by default. A typical setup:

```yaml
networkPolicy:
  enabled: true
  # Who may call the http port. Empty allows any source, still on that port only.
  ingressFrom:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: <namespace-of-your-gateway>
  egress:
    allowDNS: true          # UDP and TCP 53 to egress.dnsTo (default: kube-system/kube-dns)
    internet:
      enabled: true         # 0.0.0.0/0 minus exceptCIDRs (default: the ranges above)
      ports:
        - port: 443
          protocol: TCP
```

Setting `exceptCIDRs` replaces the default list rather than adding to it (Helm replaces
lists, it does not merge them), so a custom list must repeat the defaults it keeps, and
`exceptCIDRs: []` removes the carve-out entirely. Add your cluster's pod and service
ranges if the defaults do not already cover them, and port 80 if an allowed host serves
plain HTTP.

## Documentation

- [docs/CONTRACT.md](docs/CONTRACT.md): the stable interface: tools and their fields,
  HTTP endpoints, configuration, error messages, the image and the chart.
- [docs/TESTING.md](docs/TESTING.md): how the server is tested, from the fake yt-dlp to
  the live smoke test.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for prerequisites, build, test and lint commands,
and the pull request and release flow.

## License

[MIT](LICENSE)
