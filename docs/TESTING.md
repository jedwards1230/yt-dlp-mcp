# Testing

The tests prove the security boundaries first (URL acceptance, argv construction,
process limits), then the conversions an agent reads (transcript text), then the wire
contract (MCP over HTTP). The default `go test ./...` touches no network and needs no
yt-dlp: a fake `yt-dlp` shell script stands in for the real binary. One live smoke
test, behind the `live` build tag, runs the built container image against real
YouTube. The exact commands CI runs are in [CONTRIBUTING.md](../CONTRIBUTING.md).

## Layers

| Layer | Where | Runs | What it proves |
|---|---|---|---|
| URL guard | `internal/urlguard/urlguard_test.go` | CI `Test` | `TestResolveRejects` is the SSRF table: schemes, pseudo-URLs, userinfo, IP literals and shorthands, ports, trailing dots, percent-encoded, IDN and full-width hosts, backslashes, whitespace, playlists and channels. `TestResolveAccepts` pins each canonical form; `TestResolveCustomAllowlist` and `TestNewAllowlist` cover operator lists. |
| yt-dlp runner, against the fake | `internal/ytdlp/runner_test.go`, `runner_unix_test.go` | CI `Test` | Argv shape and flags for every operation, no shell, the sanitized child environment, every limit (output, stderr, subtitle size, deadline, concurrency, process-group kill on abort and after every run in `TestRunKillsLeftoverChildren`), the stale scratch sweep (`TestRemoveStaleScratch`), track choice (`TestTranscriptNamedTracks`, plus the property test `TestYouTubeKeysAccepted` over generated YouTube track keys), unavailable videos (`TestUnavailableVideo`), and the error messages built from yt-dlp's output. |
| Transcript conversion, golden files | `internal/transcript/transcript_test.go` + `testdata/` | CI `Test` | `TestGolden` renders every fixture (`manual` and `auto` json3 and VTT, plus `manual.srt`) with and without timestamps and compares it byte for byte with `testdata/*.golden`. `TestRollingAutoCaptionsDeduplicated`, `TestCleanRules` and `TestPaginate` pin the rules the goldens depend on; `TestParseCueSeparators`, `TestStartTimesClamped` and `TestPaginateCountsBytes` pin cue splitting, start-time clamping and the byte page budget. |
| Configuration | `internal/config/config_test.go` | CI `Test` | Defaults (a `60s` timeout, a `10m` transcript cache), flag over variable precedence, malformed and out-of-range values (`TestInvalid`), a set but blank token refused while an unset one means no auth (`TestAuthToken`), all errors reported at once, no token flag. |
| MCP over HTTP, in process | `internal/mcpserver/*_test.go` | CI `Test` | A go-sdk MCP client against the real handler on `httptest`, backed by the real runner driving the fake: `TestToolList` (names, read-only annotations, output schemas, defaults, enums), round trips for each tool, `TestSSRFRejectedBeforeExec`, `TestUnavailableVideoTools`, `TestNamedTracksEndToEnd`, the property test `TestListSubtitlesCodesRequestable`, auth (`TestBearerAuth`, `TestValidBearer`), `/healthz`, graceful shutdown, the transcript cache and its byte budget. |
| Helm chart | `.github/workflows/ci.yml`, job `Helm chart` | CI | `helm lint --strict`; `helm template` with defaults, and with the NetworkPolicy, existing-secret auth and a host override enabled. |
| Image | `.github/workflows/ci.yml`, job `Docker build` | CI | The image builds, runs as UID 65532 with a read-only root, a tmpfs `/tmp` and no capabilities, has the pinned yt-dlp and a Deno, and serves `/healthz`. |
| Live smoke, via the container image | `test/live/live_test.go`, `TestLiveSmoke` (`//go:build live`) | manually | See [Live smoke test](#live-smoke-test). |

## Running each layer

```bash
go test -race ./internal/urlguard/
go test -race ./internal/ytdlp/
go test -race ./internal/transcript/
go test -race ./internal/config/
go test -race ./internal/mcpserver/
go test -race ./internal/ytdlp/ -run 'TestTimeout|TestConcurrencyLimit' -count=1 -v
```

The runner and MCP layers start real processes (the fake), so a few tests wait on
deliberate sleeps and timeouts of up to a couple of seconds. The chart and image layers
run in CI only; their commands are in CONTRIBUTING.md, as is the live smoke command
with its variables.

## The fake yt-dlp

`internal/ytdlp/testdata/fake-yt-dlp` is a POSIX shell script that understands only the
part of yt-dlp's command line the server uses. It answers each `-O` template from files
under `testdata/videos/<id>/` and copies a fixture subtitle into the `-P` directory.
The `internal/mcpserver` tests use the same fake through the real runner, so a fixture
change moves both suites.

- **Fixture videos.** A directory per 11-character id (the id must pass the bare-id
  rule in `urlguard`): `info.json` (the field-selection line), `subtitles.keys` and
  `auto.keys` (the `, `-joined language codes yt-dlp prints for `%(…)l`),
  `subtitles.names.json` and `auto.names.json` (the `{name,ext}` lists),
  `subs/<manual|auto>.<lang>.<ext>`, and an optional `stderr` copied to stderr (for
  warnings such as a bot check). `videos/custom/` answers any non-YouTube URL, and
  `videos/search.jsonl` holds search results.
- **Template dispatch.** The fake picks the fixture file by the template text, so a new
  or renamed `-O` template in `internal/ytdlp/ops.go` needs a matching `case` in the
  script or it prints `NA`.
- **`--sub-langs` patterns.** An element with a `[` is matched like yt-dlp does, whole
  and case-insensitively, against the fixture's track codes, so named-track selection
  runs through the same pattern the server sends.
- **Unavailable videos.** An id with no fixture directory behaves like the real binary
  under `--ignore-no-formats-error`: extractor warnings on stderr, a placeholder record
  with an empty `fulltitle`, and exit status 0.
- **Failure ids.** `fakeSlow000` hangs, `fakeOrphan0` hangs with a child process,
  `fakeFlood00` floods stdout, `fakeError00` exits 1 with an `ERROR:` line, `fakeNoise00`
  floods stderr and then fails, and `fakeCount00` records how many runs overlap.
  `fakeChildOk`, `fakeChildEr` and `fakeChildPp` exit (0, 1, 1) leaving a child behind,
  detached from stdio or holding the pipes, standing in for a JavaScript runtime that
  outlives yt-dlp.
- **Steering it.** The runner gives the child a sanitized environment, so `t.Setenv`
  never reaches the fake. Tests set `Runner.extraEnv` instead: `FAKE_YTDLP_ARGV_FILE`
  (record argv), `FAKE_YTDLP_ENV_FILE` (record the environment), `FAKE_YTDLP_PID_FILE`
  (record the orphan's pid), `FAKE_YTDLP_RUNNING_DIR` and `FAKE_YTDLP_COUNT_LOG`
  (measure concurrency).
- **Keep it executable.** The script is committed with mode `100755`; a checkout that
  loses the bit fails every runner and MCP test with a start error.

## Golden files

`internal/transcript/testdata/*.golden` are the exact expected output for each fixture,
one file without and one with timestamps. Fixtures and goldens are byte-exact: the
fixtures keep real-world quirks such as CRLF line endings (`manual.srt`) and trailing
spaces (`auto.vtt`), so pre-commit's whitespace hooks exclude `testdata/`, and nothing
should reformat them.

After an intended change to conversion:

```bash
go test ./internal/transcript -run TestGolden -update
git diff internal/transcript/testdata/
```

Read every changed line, then explain the diff in the PR description: a golden change is
a behavior change. To add a fixture, put the subtitle file in `testdata/`, add it to the
`fixtures` table in `TestGolden` with its format and `rolling` set for an automatic
track (by convention those are named `auto.*`), and run the command above to create its
goldens.

## Live smoke test

`TestLiveSmoke` starts the image the way a hardened deployment runs it (read-only root,
tmpfs `/tmp`, all capabilities dropped, no new privileges, a random bearer token, debug
logging), or uses an already-running server when `YTDLP_MCP_LIVE_URL` is set. It then
completes a real MCP handshake over streamable HTTP and runs these subtests, most of them
against a public video with manual English subtitles and automatic captions:

| Subtest | Checks |
|---|---|
| `image` | UID 65532, `yt-dlp --version` equals the `requirements.txt` pin (and the server's startup log agrees), Deno present |
| `healthz`, `auth_required` | `/healthz` is open; `/mcp` without the token answers `401` |
| `tools_list` | exactly the four tools, all read-only |
| `search`, `get_info`, `list_subtitles` | real results with the documented shape; a `youtu.be` link resolves to the same video; only the original automatic track is listed by default |
| `get_transcript_manual` | two pages via `next_offset`, the timestamped page matches the plain one line for line, defaults resolve to the manual English track |
| `get_transcript_auto` | the original automatic track has no consecutive duplicate lines and no markup |
| `named_manual_track` | on a second video, whose only manual English track is a named track (`en-<track id>`): every listed code, translations included, passes `ValidateLang` and none is `unrequestable`; the hint names that track; requesting it by its full code and the default `lang=en` call both return it |
| `unavailable_video` | `get_info`, `list_subtitles` and `get_transcript` on an id that does not exist each fail with `video "<id>" is unavailable: …` |
| `ssrf_rejected_before_exec` | disallowed hosts, an IP address, a non-default port, an IPv6 literal and a trailing dot are rejected, and yt-dlp never ran for them |
| `argv_invariants` | every yt-dlp command line in the debug log ends with `--` and the target and carries the read-only flags |

It logs each call's latency and response size in a summary table. YouTube's latency
varies a lot between runs, so the test asserts stable facts only, never timings or
exact titles.

## Invariants and the tests that enforce them

The numbers match the invariants in [AGENTS.md](../AGENTS.md). A change that breaks one
of these tests breaks an invariant, not just a test.

| # | Invariant | Tests | Would catch |
|---|---|---|---|
| 1 | Read-only | `TestReadOnlyFlags`, `TestArgvSafety` (`internal/ytdlp/runner_test.go`); `TestToolList` (`internal/mcpserver/server_test.go`); `argv_invariants`, `tools_list` in `TestLiveSmoke` | a dropped or weakened read-only flag in any operation, a banned flag such as `--exec` or `--cookies`, a fifth tool, a tool not annotated read-only |
| 2 | Host allowlist before exec | `TestSSRFRejectedBeforeExec`, `TestSearchNeedsYouTubeAllowed` (`internal/mcpserver/server_test.go`); `TestResolveRejects`, `TestResolveCustomAllowlist` (`internal/urlguard/urlguard_test.go`); `ssrf_rejected_before_exec` in `TestLiveSmoke` | a tool path that reaches the backend before `Resolve`, a parser differential that lets a bad host through |
| 3 | Never a shell | `TestNoShell`, `TestArgvSafety`, `TestValidateLang` (`internal/ytdlp/runner_test.go`); `argv_invariants` in `TestLiveSmoke` | shell interpretation of a query or URL, a target that is not the single argument after a final `--`, a `--sub-langs` value that is a regex or `all` |
| 4 | Bounded output | `TestOutputCapStopsProcess`, `TestStderrTailBounded`, `TestSubtitleFileCap`, `TestTimeoutCoversSlotWait`, `TestConcurrencyLimit`, `TestBusy` (`internal/ytdlp/runner_test.go`); `TestTimeoutKillsProcessGroup`, `TestRunKillsLeftoverChildren` (`internal/ytdlp/runner_unix_test.go`); `TestPaginate`, `TestPaginateCountsBytes` (`internal/transcript/transcript_test.go`); `TestGetTranscriptTool`, `TestSearchTool`, `TestDescriptionTruncation`, `TestListSubtitlesTool` (`internal/mcpserver/server_test.go`); `TestTranscriptCacheByteBudget` (`internal/mcpserver/cache_test.go`) | an uncapped buffer, a timeout that restarts after the slot wait, a JavaScript child that outlives its call (on a timeout or after yt-dlp exits), more concurrent runs than slots, an unpaginated or unclamped response, a page budget counted in characters, an unbounded transcript cache |
| 5 | The token stays put | `TestChildEnvSanitized` (`internal/ytdlp/runner_test.go`); `TestBearerAuth`, `TestHealthz` (`internal/mcpserver/http_test.go`); `TestInvalid` (`internal/config/config_test.go`) | the token in yt-dlp's environment or a `401` body, `/healthz` behind auth, a token flag |
| 6 | Every listed code is requestable | `TestListSubtitlesCodesRequestable`, `TestNamedTracksEndToEnd` (`internal/mcpserver/server_test.go`); `TestYouTubeKeysAccepted` (`internal/ytdlp/runner_test.go`); `named_manual_track` in `TestLiveSmoke` | a `list_subtitles` code or hint that `get_transcript` rejects, a YouTube track key `ValidateLang` refuses, a named track the `--sub-langs` pattern misses |

## Explicitly avoid

- **Network in unit tests.** Anything that needs real yt-dlp output becomes a fixture
  for the fake (synthetic words, real structure) or a check in the live test.
- **Copying real transcripts into fixtures.** Keep the structure (rolling cues,
  `aAppend` events, inline word timestamps) and write your own text.
- **Regenerating goldens to turn a test green** without reading the diff.
- **Testing a URL rule only in `urlguard`.** A new URL-taking tool also needs a case in
  `TestSSRFRejectedBeforeExec`, which proves the guard runs before any exec.
- **Faking a failure the real binary does not produce.** An unavailable video is exit 0
  with a placeholder record under `--ignore-no-formats-error`, not an `ERROR:` exit;
  test it through the fake's missing-fixture path, as `TestUnavailableVideo` does.
- **`t.Setenv` to pass state to the fake.** The sanitized child environment drops it,
  which is exactly what `TestChildEnvSanitized` protects; use `Runner.extraEnv`.
- **Asserting live latency or exact live content.** Titles, counts and timings change;
  assert only stable facts such as "the video has manual English subtitles".
- **Putting the live test in the default run.** It stays behind `//go:build live` so
  `go test ./...` never needs Docker or the network.
