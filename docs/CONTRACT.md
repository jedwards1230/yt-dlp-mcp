# yt-dlp-mcp contract

What this repository promises its consumers: MCP clients, operators who run the image,
and anything that installs the Helm chart. Sections are numbered so reviews and other
documents can cite a clause (`CONTRACT §5.4`). Where a test, golden file or CI job
enforces a clause, the clause names it instead of restating it; change the artifact and
this document in the same PR.

## §1 Scope

1. **Read-only.** The server returns search results, metadata, subtitle listings and
   transcripts. It never downloads media, never writes outside a per-call scratch
   directory that it removes, and has no tool that takes a filesystem path or an output
   location.
2. **One backend.** Every tool answers by running the `yt-dlp` executable (§7). The
   server has no other outbound network client.
3. **Bounded.** Every response and every subprocess is limited in size and time (§5,
   §7).

## §2 HTTP surface

One listener, `YTDLP_MCP_ADDR` (default `:8080`). Paths match exactly; anything else
answers `404`.

| Path | Methods | Auth | Behavior |
|---|---|---|---|
| `/mcp` | `POST` (MCP streamable HTTP) | bearer token when `YTDLP_MCP_AUTH_TOKEN` is set | The MCP endpoint. Stateless: no `Mcp-Session-Id` is issued, `GET` and `DELETE` answer `405`, and any replica can serve any request. |
| `/healthz` | `GET`, `HEAD` | never | `200` with body `ok`. Other methods answer `405`. |

- **Auth.** With a token, `/mcp` requires `Authorization: Bearer <token>`: the scheme
  name in any case, one or more spaces, then exactly the token. The presented and
  configured tokens are compared as SHA-256 digests in constant time, so neither timing
  nor a length mismatch reveals anything. Anything else answers `401` with
  `WWW-Authenticate: Bearer realm="yt-dlp-mcp", error="invalid_token"`, and the token is
  never echoed. With `YTDLP_MCP_AUTH_TOKEN` unset, `/mcp` is open and the deployment
  must restrict who can reach it; set but blank stops startup (§3). Enforced by
  `TestBearerAuth`, `TestValidBearer`, `TestNoAuthWhenTokenEmpty`, `TestHealthz`
  (`internal/mcpserver/http_test.go`) and `TestAuthToken`
  (`internal/config/config_test.go`).
- **Disconnects.** For a client speaking MCP protocol `2026-07-28` or later, a tool
  call is canceled when its HTTP request ends, which kills its yt-dlp process. For an
  older client a disconnect does not cancel the call; it runs until yt-dlp finishes or
  `YTDLP_MCP_TIMEOUT` expires (§7).
- **Identity.** `initialize` reports `serverInfo.name` `yt-dlp-mcp` and the build
  version, and `instructions` describe the search, info, transcript flow.
- **Shutdown.** On `SIGTERM` or `SIGINT` the server stops accepting connections and
  gives in-flight calls 25 s, then closes the remaining connections, which cancels
  their calls on the terms of **Disconnects** above. Enforced by
  `TestServeGracefulShutdown` (`internal/mcpserver/http_test.go`).

## §3 Configuration

Environment variables, each overridable by a flag except the token. Precedence is flag,
then variable, then default; surrounding whitespace is trimmed. Any invalid value stops
startup with every problem listed, as does an unknown flag or argument, and the server
also refuses to start when the yt-dlp executable cannot be found. Enforced by
`TestDefaults`, `TestEnvAndFlagPrecedence`, `TestInvalid`, `TestAuthToken`,
`TestMultipleErrorsReported`, `TestHelp` (`internal/config/config_test.go`). What each
variable is for, for operators: the README's Configuration section.

| Variable | Flag | Default | Accepted |
|---|---|---|---|
| `YTDLP_MCP_ADDR` | `-addr` | `:8080` | non-empty `host:port` |
| `YTDLP_MCP_YTDLP_PATH` | `-yt-dlp-path` | `yt-dlp` | a name on `PATH` or a path |
| `YTDLP_MCP_ALLOWED_HOSTS` | `-allowed-hosts` | `youtube.com,www.youtube.com,m.youtube.com,music.youtube.com,youtu.be,youtube-nocookie.com,www.youtube-nocookie.com` | comma list of exact, case-insensitive DNS names with at least two labels; no IP addresses, ports, wildcards, trailing dots or non-ASCII; at least one entry |
| `YTDLP_MCP_TIMEOUT` | `-timeout` | `60s` | Go duration, `1s` to `10m`; bounds the whole call, slot wait included (§7) |
| `YTDLP_MCP_MAX_OUTPUT_BYTES` | `-max-output-bytes` | `16777216` | whole bytes, `65536` to `268435456` |
| `YTDLP_MCP_MAX_CONCURRENCY` | `-max-concurrency` | `4` | `1` to `64` |
| `YTDLP_MCP_AUTH_TOKEN` | none: environment only | unset, meaning no auth | any string that is not blank after trimming; set but blank (for example a Secret key holding an empty value) stops startup instead of turning auth off |
| `YTDLP_MCP_LOG_LEVEL` | `-log-level` | `info` | `debug`, `info`, `warn`, `error`, in any case |
| `YTDLP_MCP_CACHE_TTL` | `-cache-ttl` | `10m` | Go duration, `0` to `24h`; `0` disables the transcript cache (§5.4) |

`-version` prints the version and exits; `-h` prints the flags. Logs are JSON on stderr.
The token is never logged.

## §4 URL acceptance

Every `url` argument is resolved by `urlguard.Allowlist.Resolve` before any process
starts. Accepted: a bare 11-character YouTube video id (`[A-Za-z0-9_-]{11}`), or an
`http`/`https` URL of at most 2048 bytes whose lowercased host is exactly on the
allowlist. Rejected with an explanatory error: other schemes (including yt-dlp
pseudo-URLs such as `ytsearch5:`), userinfo, IP literals and numeric shorthands
(`127.1`, `2130706433`, `0x7f.0.0.1`), explicit non-default ports, trailing-dot hosts,
percent-encoded or non-ASCII hosts, and any whitespace, control character or backslash.

YouTube URLs (`watch?v=`, `youtu.be/`, `/shorts/`, `/embed/`, `/live/`, `/v/`, `/e/`)
are canonicalized to `https://www.youtube.com/watch?v=<id>`; playlist and channel URLs
are rejected. YouTube inputs therefore need `www.youtube.com` on the allowlist, and so
does `search` (§5.1). Other hosts are rebuilt from their parsed scheme, host, path and
query, without userinfo, port or fragment. yt-dlp never receives the raw input.

Enforced by `TestResolveAccepts`, `TestResolveRejects`, `TestResolveCustomAllowlist`,
`TestNewAllowlist` (`internal/urlguard/urlguard_test.go`) and, end to end, by
`TestSSRFRejectedBeforeExec` (`internal/mcpserver/server_test.go`), which also asserts
that yt-dlp never starts for a rejected URL.

## §5 Tools

Exactly four tools. All are annotated read-only, non-destructive, idempotent and
open-world, and declare an output schema. A successful result carries
`structuredContent` and the same JSON as one text item. Input schemas carry the defaults
and enums below; the SDK validates arguments against them (enums are case-sensitive)
before a handler runs. Pinned by `TestToolList`
(`internal/mcpserver/server_test.go`). Failures follow §6. Optional output fields are
omitted when the site does not report them.

### §5.1 `search`

| Input | Type | Default | Rules |
|---|---|---|---|
| `query` | string | required | 1 to 200 characters, no control characters or newlines |
| `limit` | integer | `10` | `0` means the default; clamped to 1 to 25 |

Output: `query`, `limit` (as applied), `count`, `results[]` of `id`, `title`, `url`, and
when the flat listing has them `channel`, `channel_id`, `duration_seconds`, `duration`
(`m:ss` or `h:mm:ss`), `view_count`, `live_status`. Runs a YouTube search, so it fails
unless `www.youtube.com` is allowed. Enforced by `TestSearchTool`,
`TestSearchNeedsYouTubeAllowed` (`internal/mcpserver/server_test.go`), `TestSearch`
(`internal/ytdlp/runner_test.go`).

### §5.2 `get_info`

| Input | Type | Default | Rules |
|---|---|---|---|
| `url` | string | required | §4 |
| `full_description` | boolean | `false` | otherwise the description is cut to 1500 characters |
| `include_tags` | boolean | `false` | adds the uploader's `tags` |

Output: `id`, `title`, `url`, `channel`, `channel_id`, `channel_url`, `channel_handle`
(`@name`), `duration_seconds`, `duration`, `upload_date` (`YYYY-MM-DD`), `live_status`,
`availability`, `language`, `age_limit`, `view_count`, `like_count`, `comment_count`,
`description`, `description_truncated`, `description_length` (full length in
characters), `chapters[]` of `start` (`mm:ss` or `hh:mm:ss`), `start_seconds`,
`end_seconds`, `title`; `categories`, `tags`, `has_subtitles`, `subtitle_languages`
(manual track codes), `has_automatic_captions`, `automatic_captions_original` (such as
`en-orig`), `automatic_captions_count`. Enforced by `TestGetInfoTool`,
`TestGetInfoErrors`, `TestDescriptionTruncation` (`internal/mcpserver/server_test.go`),
`TestInfo` (`internal/ytdlp/runner_test.go`).

### §5.3 `list_subtitles`

| Input | Type | Default | Rules |
|---|---|---|---|
| `url` | string | required | §4 |
| `include_translations` | boolean | `false` | also list every machine-translated automatic track |

Output: `video_id`, `title`, `url`, `language`, `manual[]` and `automatic[]` of `code`,
`name`, `original`; `automatic_total`, `automatic_omitted`, `unrequestable`, `hint`. By
default `automatic` holds only the original-language track (the code ending in `-orig`,
flagged `original: true`) and `automatic_omitted` counts the rest; when the site marks
no original track, every automatic track is listed. Both lists are always arrays, never
null. Manual codes include YouTube's named tracks (`en-eEY6OEpapPo`, see §5.4).

Every code in `manual`, `automatic` and `hint` is accepted as `get_transcript`'s `lang`.
A code the site reports that `get_transcript` could not request (one that fails the
§5.4 `lang` rule) is listed under `unrequestable` instead, which YouTube never needs.
`hint` suggests `get_transcript` arguments: the manual track in the video's spoken
language, else English, else the first, and the original automatic track. Enforced by
`TestListSubtitlesTool`, `TestListSubtitlesCodesRequestable` (a property test over
random track codes), `TestNamedTracksEndToEnd`, `TestPreferredManual`
(`internal/mcpserver/server_test.go`), `TestSubtitlesNames`
(`internal/ytdlp/runner_test.go`); against real YouTube, by the `named_manual_track`
subtest of `TestLiveSmoke`.

### §5.4 `get_transcript`

| Input | Type | Default | Rules |
|---|---|---|---|
| `url` | string | required | §4 |
| `lang` | string | `en` | a code from `list_subtitles`: a letter, then 1 to 63 letters, digits, `-` or `_` (`^[A-Za-z][A-Za-z0-9_-]{1,63}$`), and never `all` in any case |
| `source` | string | `any` | `any`, `manual` or `auto`; see track choice below |
| `timestamps` | boolean | `false` | prefix each line with `[mm:ss]`, or `[hh:mm:ss]` from one hour on |
| `offset` | integer | `0` | first segment to return; at least 0 and below `total_segments` |
| `limit` | integer | `500` | segments per page; `0` means the default; clamped to 1 to 2000 |

**Track choice.** For `lang`, in order: the manual track coded exactly `lang`; else
the first named manual track in that language, which YouTube codes
`<lang>-<track id>` (`en-eEY6OEpapPo` for `en`; the id has more than 8 characters, so a
region or script code such as `en-GB`, `zh-Hant` or `es-419` never counts as one); else
the automatic track `lang`. `source=manual` stops after the manual steps,
`source=auto` takes only the automatic track. A named or region track can also be
requested directly by its full code.

**Pagination is by segment**, one cleaned caption line. A page ends at `limit` segments
or before the line that would take `text` past 40000 bytes of UTF-8 (newlines and
timestamps included), whichever comes first, and always holds at least one segment, so
a single longer line comes back alone. Output: `video_id`, `title`, `url`, `lang` (the
code of the track actually used: the requested `lang`, or the named track's full code),
`source` (`manual` or `auto`, the kind of track actually used), `auto_translated`
(present and `true` when the automatic track is a machine translation rather than the
spoken language), `format` (`json3`, `vtt` or `srt`), `offset`, `returned`,
`total_segments`, `next_offset` (equal to `total_segments` when done), `has_more`, and
`text` with one segment per line.

A fetched transcript is cached in memory for `YTDLP_MCP_CACHE_TTL`, keyed by canonical
URL, the requested `lang` and `source`, so later pages cost no yt-dlp run. The cache
holds at most 32 transcripts and about 64 MiB, evicting the least recently used until
both bounds hold; a transcript larger than the whole budget is not cached, so each of
its pages fetches again. The cache is per process: with several replicas a page may be
served by one that has to fetch again.

Enforced by `TestGetTranscriptTool`, `TestGetTranscriptAutomatic`,
`TestGetTranscriptErrors`, `TestNamedTracksEndToEnd`
(`internal/mcpserver/server_test.go`), `TestTranscriptCache`,
`TestTranscriptCacheByteBudget`, `TestTranscriptCacheDisabled`
(`internal/mcpserver/cache_test.go`), `TestPaginate`, `TestPaginateCountsBytes`
(`internal/transcript/transcript_test.go`), `TestTranscript`,
`TestTranscriptNamedTracks`, `TestIsNamedTrack`, `TestYouTubeKeysAccepted`,
`TestValidateLang` (`internal/ytdlp/runner_test.go`); conversion per §8. Against real
YouTube, the `named_manual_track` subtest of `TestLiveSmoke` checks that the default
`lang=en` picks a named manual track.

## §6 Errors

A failed tool call is an MCP tool result with `isError: true` and one text item that
says what went wrong and what to do instead. The shapes below are stable; wording
around them may improve, so the named tests pin the parts an agent relies on.

| Situation | Message shape | Test |
|---|---|---|
| argument fails the input schema | `validating "arguments": …` (from the SDK, e.g. a `source` outside the enum) | `TestGetTranscriptErrors` |
| host not allowed | `host "example.com" is not allowed; allowed hosts: …` | `TestResolveRejects` |
| not a URL or id | `"…" is not an http(s) URL or an 11-character YouTube video id` | `TestResolveRejects` |
| playlist or channel URL | `could not find a video id in "…"; pass a single-video URL …` | `TestResolveRejects` |
| `search` without YouTube | `search uses YouTube, but www.youtube.com is not on this server's allowed host list` | `TestSearchNeedsYouTubeAllowed` |
| bad query | `query is required`, `query is N characters; the limit is 200`, `query must not contain control characters or newlines` | `TestSearchTool` |
| bad language | `language "…" is not a subtitle language code like "en", "pt-BR", "en-orig" or "en-eEY6OEpapPo"; call list_subtitles for the codes this video has` | `TestValidateLang`, `TestGetTranscriptErrors` |
| video unavailable (removed, private, never existed) | `video "<id>" is unavailable: <reason>` (`the video is unavailable: …` when yt-dlp reported no id), from every URL tool. The reason is yt-dlp's last `ERROR:` line, else its last extractor warning, else `yt-dlp found no title, channel or other metadata for it; it may be private, removed or never have existed` | `TestUnavailableVideo`, `TestDecodeInfoUnavailable`, `TestUnavailableReason`, `TestUnavailableVideoTools`; live: `unavailable_video` in `TestLiveSmoke` |
| missing language | `no subtitles in language "de"; available manual: en, fr; auto: en-orig, en, … (N more) (call list_subtitles for names)` (`manual `/`automatic ` before `subtitles` when `source` is set). When the video lists no tracks at all and yt-dlp warned why (a bot check, for example), `; yt-dlp warned: <warning>` follows | `TestTranscriptErrors`, `TestSummarizeAutomaticCaps`, `TestUnavailableVideo` |
| unsupported track format | `the en track is only available as "ttml", which this server cannot convert (supported: json3, vtt, srt)`; when that is a manual track and automatic captions exist in `lang`, `; automatic captions exist in en, so retry with source="auto"` follows | `TestTranscriptErrors`, `TestGetTranscriptErrors` |
| page past the end | `offset N is past the end of the transcript (M segments; valid offsets are 0-(M-1))` | `TestPaginate`, `TestGetTranscriptTool` |
| timeout | `yt-dlp timed out after 60s (YTDLP_MCP_TIMEOUT); the site may be slow or blocking requests — retry later`, or, when the call queued for a slot, `…, Ns of it waiting for a free yt-dlp slot (YTDLP_MCP_MAX_CONCURRENCY); the server is busy — retry shortly` | `TestTimeoutKillsProcessGroup`, `TestTimeoutCoversSlotWait` |
| no slot before the deadline | `server busy: all N yt-dlp slots (YTDLP_MCP_MAX_CONCURRENCY) stayed in use for 60s; retry shortly` | `TestBusy` |
| stdout over the cap | `yt-dlp output exceeded the N-byte limit (YTDLP_MCP_MAX_OUTPUT_BYTES) and was stopped` | `TestOutputCapStopsProcess` |
| subtitle file over the cap | `the en subtitle track is N bytes, larger than the M-byte limit (YTDLP_MCP_MAX_OUTPUT_BYTES)` | `TestSubtitleFileCap` |
| yt-dlp error | `yt-dlp failed: <its last ERROR line, prefix removed, cut at 600 bytes>` | `TestExitErrorMessage`, `TestGetInfoErrors`, `TestStderrTailBounded`, `TestStderrSummary` |
| request canceled | `the request was canceled before yt-dlp finished` (or `… while waiting for a free yt-dlp slot`) | `TestCanceled` |
| yt-dlp missing at call time | `could not start yt-dlp at "…" (YTDLP_MCP_YTDLP_PATH): …` | `TestStartFailure` |

The tests above live in `internal/urlguard/urlguard_test.go`,
`internal/ytdlp/runner_test.go`, `internal/ytdlp/runner_unix_test.go`,
`internal/transcript/transcript_test.go`, `internal/mcpserver/server_test.go` and, for
the live subtest, `test/live/live_test.go`.

## §7 yt-dlp invocation

- **Argv only.** `exec.CommandContext` with no shell. The arguments are `BaseArgs`, the
  operation's flags, `--`, then the target: the canonical URL from §4, or
  `ytsearch<limit>:<query>`. The only user-derived flag value is `--sub-langs`,
  validated per §5.4. Enforced by `TestArgvSafety`, `TestNoShell`, `TestValidateLang`.
- **`BaseArgs`** (`internal/ytdlp/runner.go`), on every run: `--ignore-config`,
  `--no-plugin-dirs`, `--no-remote-components`, `--use-extractors default,-generic`,
  `--no-cache-dir`, `--no-cookies`, `--no-cookies-from-browser`, `--color never`,
  `--no-progress`. With `--skip-download` (added by every operation) these are the
  read-only set pinned by `TestReadOnlyFlags`.
- **Per operation** (`internal/ytdlp/ops.go`): `search` adds `--flat-playlist
  --skip-download` and one `-O` template. Single-video operations add `--no-playlist
  --playlist-items 1 --skip-download --ignore-no-formats-error` and `-O` field-selection
  templates that keep stdout to a few KB. `--ignore-no-formats-error` keeps the metadata
  of a video with no downloadable formats, but also makes yt-dlp report an unavailable
  video with exit status 0 and a placeholder record; the server recognizes that record
  (no id, or no title, channel or uploader of the extractor's own) and returns the §6
  "unavailable" error instead. `get_transcript` also adds `--no-simulate` (`-O` implies
  `--simulate`, which would skip writing subtitles),
  `--extractor-args youtube:skip=translated_subs`, `--write-subs` and/or
  `--write-auto-subs`, `--sub-langs`, `--sub-format json3/vtt/srt`, `-P <scratch dir>`
  and `-o transcript`. The `--sub-langs` value is `<lang>` for `source=auto` and
  otherwise `<lang>,<lang>-<named-track pattern>`, a regular expression the server
  builds from the validated `lang` (§5.4) so one run fetches every candidate track.
  Enforced by `TestSearch`, `TestArgvSafety`, `TestTranscript`,
  `TestTranscriptNamedTracks`, `TestYouTubeKeysAccepted`, `TestUnavailableVideo`.
- **Environment.** A fixed allowlist: `HOME`, `TMPDIR`, `XDG_CACHE_HOME`,
  `XDG_CONFIG_HOME` and `DENO_DIR` inside the scratch directory, Python UTF-8 settings
  and `NO_COLOR`, plus `PATH`, `LANG`, `LC_ALL`, `TZ`, `SSL_CERT_FILE`, `SSL_CERT_DIR`
  and the `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`/`ALL_PROXY` variables (either case)
  passed through. Nothing else, in particular not the token. Enforced by
  `TestChildEnvSanitized`.
- **Scratch directory.** One per call under the server's temporary directory (`/tmp` in
  the image), named `yt-dlp-mcp-<pid>-<random>` and removed when the call returns. A
  server killed mid-call (SIGKILL, an out-of-memory kill) cannot clean up, so at startup,
  before serving, the server removes such directories: only directories owned by its
  own user whose `<pid>` is its own or no longer running, never following symbolic
  links, so a live server sharing the directory keeps its own. Enforced by
  `TestTranscript` (`internal/ytdlp/runner_test.go`) and `TestRemoveStaleScratch`
  (`internal/ytdlp/runner_unix_test.go`).
- **Limits.** One deadline of `YTDLP_MCP_TIMEOUT` covers the whole call: waiting for a
  slot, running yt-dlp and reading its output, so a call that queued has less time to
  run. At most `YTDLP_MCP_MAX_CONCURRENCY` yt-dlp processes run at once. stdout beyond
  `YTDLP_MCP_MAX_OUTPUT_BYTES` stops the run, only the last 64 KiB of stderr is kept,
  and a subtitle file larger than the cap is refused unread. On Unix, the image's
  platform, yt-dlp runs in its own process group: a timeout, an overflow or a canceled
  request SIGKILLs the whole group, and the group is killed again after every run,
  successful or not, so a JavaScript runtime child that outlived yt-dlp dies before the
  call returns. Enforced by `TestTimeoutCoversSlotWait`, `TestConcurrencyLimit`,
  `TestBusy`, `TestOutputCapStopsProcess`, `TestStderrTailBounded`,
  `TestSubtitleFileCap`, `TestCanceled` (`internal/ytdlp/runner_test.go`) and
  `TestTimeoutKillsProcessGroup`, `TestRunKillsLeftoverChildren`
  (`internal/ytdlp/runner_unix_test.go`).

All §7 tests above are in `internal/ytdlp/runner_test.go` unless noted. Against the real
binary, `TestLiveSmoke` (`test/live/live_test.go`) re-checks the target-after-`--` rule
and the read-only flags on every yt-dlp run it logs.

## §8 Transcript conversion

`internal/transcript` parses json3, WebVTT and SRT into segments, strips tags, inline
word timestamps and ASS override blocks, decodes HTML entities in VTT and SRT, drops
invisible direction marks, and collapses whitespace. In VTT and SRT a cue starts at a
`-->` timing line and ends at an empty line or the next timing line; a line holding only
spaces or tabs also ends it when the next timing line follows directly or after one cue
identifier, so two cues never merge and no timing line or cue number leaks into the
text. A start time that is negative or too large to represent becomes 0. Enforced by
`TestParseCueSeparators`, `TestStartTimesClamped`, `TestFormatTimestamp`
(`internal/transcript/transcript_test.go`). Automatic tracks get rolling
cleanup: each cue line becomes a segment, a line equal to the previous one is dropped,
and a line that extends the previous one replaces it while keeping the earlier start.
Manual cues are joined into one line, and only an identical line at the same timestamp
is dropped, so a repeated chorus survives. The exact output for each fixture, with and
without timestamps, is the golden files `internal/transcript/testdata/*.golden`,
checked by `TestGolden`; the de-duplication rules by
`TestRollingAutoCaptionsDeduplicated` and `TestCleanRules`
(`internal/transcript/transcript_test.go`).

## §9 Container image

- `ghcr.io/jedwards1230/yt-dlp-mcp:<X.Y.Z>` and `:latest`, for `linux/amd64` and
  `linux/arm64`, labelled `org.opencontainers.image.version` and
  `org.opencontainers.image.source`.
- Runs as UID and GID 65532. Entrypoint `/usr/bin/tini -- /usr/local/bin/yt-dlp-mcp`,
  so arguments to `docker run` are server flags. `EXPOSE 8080`, with
  `YTDLP_MCP_ADDR=:8080` set in the image.
- Works with a read-only root filesystem as long as `/tmp` is writable: `HOME`,
  `XDG_CACHE_HOME` and `DENO_DIR` point under `/tmp`.
- Contains yt-dlp and yt-dlp-ejs at the versions pinned in `requirements.txt`, and Deno
  from a pinned `denoland/deno:bin-<version>` stage. Remote JavaScript components are
  never fetched.
- Checked by the CI `Docker build` job (runs as 65532 read-only with no capabilities,
  yt-dlp matches the pin, `deno` present, `/healthz` answers) and by the `image`
  subtest of `TestLiveSmoke`.

## §10 Helm chart

Chart `deploy/helm/yt-dlp-mcp`, name `yt-dlp-mcp`, published to
`oci://ghcr.io/jedwards1230/charts/yt-dlp-mcp`, Kubernetes `>=1.25`. Resources are named
by the standard `fullname` helper (`fullnameOverride` and `nameOverride` honored;
release `yt-dlp-mcp` gives `yt-dlp-mcp`). Pods carry `app.kubernetes.io/name:
yt-dlp-mcp` and `app.kubernetes.io/instance: <release>`, which are also the Service and
NetworkPolicy selectors; the Deployment, Service, Secret and NetworkPolicy also carry
`helm.sh/chart`, `app.kubernetes.io/version` and `app.kubernetes.io/managed-by`. The
container port and the Service port are both named `http`, and the chart sets
`YTDLP_MCP_ADDR=:8080`, so the container listens on 8080.

| Values key | Maps to or controls |
|---|---|
| `image.repository`, `image.tag` (`""` means `appVersion`), `image.pullPolicy`, `imagePullSecrets` | container image |
| `config.allowedHosts` (list; `[]` keeps the server default) | `YTDLP_MCP_ALLOWED_HOSTS` |
| `config.timeout`, `config.maxOutputBytes`, `config.maxConcurrency`, `config.logLevel`, `config.cacheTTL` | `YTDLP_MCP_TIMEOUT`, `YTDLP_MCP_MAX_OUTPUT_BYTES`, `YTDLP_MCP_MAX_CONCURRENCY`, `YTDLP_MCP_LOG_LEVEL`, `YTDLP_MCP_CACHE_TTL` |
| `config.ytdlpPath` (`""` keeps the image's yt-dlp) | `YTDLP_MCP_YTDLP_PATH` |
| `auth.existingSecret` and `auth.existingSecretKey` (default `token`), or `auth.token` (stored in a chart-created Secret `<fullname>-auth`) | `YTDLP_MCP_AUTH_TOKEN` |
| `extraEnv` | extra `EnvVar` entries |
| `resources` | default: requests `cpu: 100m`, `memory: 384Mi`; limit `memory: 2Gi`, sized for the default `config.maxConcurrency` of 4 at about 400Mi per slot plus the server and its transcript cache. Scale the limit with `config.maxConcurrency`; a `tmp.medium: Memory` tmpfs counts against it too |
| `replicaCount`, `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`, `podLabels`, `terminationGracePeriodSeconds` (default 30) | as named |
| `podSecurityContext`, `securityContext` | default: non-root 65532, read-only root, all capabilities dropped, no privilege escalation, `RuntimeDefault` seccomp |
| `tmp.sizeLimit` (default `256Mi`), `tmp.medium` | the `/tmp` emptyDir |
| `automountServiceAccountToken` (default `false`) | the pod's service-account token |
| `livenessProbe`, `readinessProbe` | default `GET /healthz` on `http` |
| `service.type` (default `ClusterIP`), `service.port` (default 8080) | the Service |
| `networkPolicy.enabled` (default `false`) | a NetworkPolicy with `policyTypes: [Ingress, Egress]` |
| `networkPolicy.ingressFrom` | `from` peers for the `http` port; empty allows any source on that port |
| `networkPolicy.egress.allowDNS`, `networkPolicy.egress.dnsTo` | UDP and TCP 53 to these peers (default `kube-system` pods labelled `k8s-app: kube-dns`) |
| `networkPolicy.egress.internet.enabled`, `.exceptCIDRs`, `.ports` | an `ipBlock` of `0.0.0.0/0` minus `exceptCIDRs`, by default `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `169.254.0.0/16` (a set list replaces the default; `[]` removes the carve-out); `ports: []` allows every port. The DNS rule is a separate peer, so the carve-out never blocks DNS |
| `extraManifests` | extra objects, rendered through `tpl` |

Rendering with defaults, and with the policy, existing-secret auth and a host override
enabled, is checked by the CI `Helm chart` job, which also requires the policy rendered
with no `exceptCIDRs` override to except all four default ranges; `helm lint --strict`
must pass.

## §11 Versioning

- Releases are `vX.Y.Z` git tags. The image tag and the chart `version` and
  `appVersion` are the same `X.Y.Z`, so a chart deploys the image it was released with
  unless `image.tag` overrides it. The release mechanics are in CONTRIBUTING.md.
- Any change that removes or renames something in §2 to §10, narrows what is accepted,
  or changes a default so that the server accepts more (hosts, auth, egress) is a
  breaking change and updates this document in the same PR.
