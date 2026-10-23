# AGENTS.md

Guidance for coding agents working in this repository. Build, test, and lint commands
live in [CONTRIBUTING.md](CONTRIBUTING.md).

**yt-dlp-mcp** is a read-only MCP server: four tools (`search`, `get_info`,
`list_subtitles`, `get_transcript`) over streamable HTTP, each answered by running the
`yt-dlp` executable with a locked-down argv and trimming what it prints or writes. yt-dlp
fetches almost any URL and runs a JavaScript runtime, so the design treats it as
untrusted machinery: a host allowlist in front, a fixed flag set, hard limits behind.

- [docs/CONTRACT.md](docs/CONTRACT.md): the external contract, each clause citing its
  enforcing test. Read it before changing behavior.
- [docs/TESTING.md](docs/TESTING.md): test layers, the fake yt-dlp, golden files, and
  the map from each invariant below to the tests that enforce it.
- Package depth: the doc comment at the top of each package.

## Layout

- `internal/urlguard/`: the SSRF boundary; `Allowlist.Resolve` gates every URL.
- `internal/ytdlp/`: the only code that starts processes; `Runner.run` owns argv, env,
  scratch dir, deadline, caps and the semaphore. `testdata/fake-yt-dlp` is the fake.
- `test/live/`: live smoke test against the built image, `live` build tag only.

## Commands

```bash
# Serve against the fake yt-dlp: no network, no yt-dlp install
YTDLP_MCP_YTDLP_PATH=$PWD/internal/ytdlp/testdata/fake-yt-dlp \
  go run ./cmd/yt-dlp-mcp -addr 127.0.0.1:8080 -log-level debug
# Serve for real (yt-dlp, yt-dlp-ejs and deno on PATH)
go run ./cmd/yt-dlp-mcp -addr 127.0.0.1:8080
# One package, one test
go test -race ./internal/ytdlp -run TestArgvSafety
```

## Invariants (violations are bugs)

Which tests enforce each one, and what a break would look like:
[docs/TESTING.md](docs/TESTING.md#invariants-and-the-tests-that-enforce-them).

1. **Read-only.** No tool downloads media, writes outside its per-call scratch dir, or
   takes a path. Every run carries the fixed read-only flags in `ytdlp.BaseArgs` plus
   `--skip-download`, and that set only ever gets stricter.
2. **Host allowlist before exec.** Every URL argument goes through
   `urlguard.Allowlist.Resolve` before a process starts, and yt-dlp gets the rebuilt
   URL, never the raw string. A new URL-taking tool gets a case in
   `TestSSRFRejectedBeforeExec`.
3. **Never a shell.** `exec.CommandContext` with argv only. The user-derived target is
   the last argument, right after `--`; the one user value in a flag (`--sub-langs`)
   passes `ValidateLang`.
4. **Bounded output.** stdout is capped and stops the run, stderr keeps a 64 KiB tail,
   subtitle files are size-checked before reading, one `YTDLP_MCP_TIMEOUT` deadline
   covers slot wait plus run, the process group is SIGKILLed on abort and again after
   every run, a semaphore caps concurrency, responses are clamped and paginated, and the
   transcript cache is capped by entry count and bytes.
5. **The token stays put.** It never reaches the yt-dlp environment or a response, it
   cannot be passed as a flag, and `/healthz` stays unauthenticated.
6. **Every listed code is requestable.** Each code `list_subtitles` returns in `manual`,
   `automatic` or `hint` passes `ValidateLang`, so `get_transcript` accepts it; any other
   goes to `unrequestable`.

## Gotchas

- **`-O` silently disables subtitle writing.** It implies `--simulate`, so transcripts
  need `--no-simulate`; without it every call reports "no subtitles" and yt-dlp exits 0.
- **A missing language is not a yt-dlp error.** It exits 0 and writes nothing; "no
  subtitles" is the absence of `transcript.<lang>.*` in the scratch dir.
- **Exit 0 does not mean the video exists.** Under `--ignore-no-formats-error` yt-dlp
  answers an unavailable video with exit 0 and a placeholder record (empty `fulltitle`).
  A new single-video operation must decode through `decodeInfo`, or it returns empty
  metadata instead of an error.
- **`--sub-langs` takes comma-split regexes, and `all` means every track.** Keep
  `ValidateLang` narrow: no comma, metacharacter, leading `-` or `all`. The server's own
  named-track pattern must hold no comma either, so `namedSuffix` spells "9 or more" as
  `{9}[…]*`: `{9,}` would be split in two.
- **`en-GB` is not a named track.** YouTube codes those `<lang>-<11-char id>`, and
  `IsNamedTrack` demands more than 8 characters after the hyphen; a plain prefix match
  would hand `lang=en` the British track instead of English captions.
- **Empty stdout lines carry data.** `%(dict)l` prints an empty line for an empty dict,
  so never drop empty lines. Track names use `{name,ext}` because yt-dlp drops empty
  dicts and `{name}` alone would misalign names with codes.
- **The fake dispatches on `-O` template text.** A new or renamed template in
  `internal/ytdlp/ops.go` needs a matching `case` in `testdata/fake-yt-dlp` (every
  `%(.{…})j` reads `info.json`), or it prints `NA`; `mcpserver` tests run it too.
- **The child environment is an allowlist.** A variable yt-dlp or Deno needs goes in
  `passthroughEnv`. `t.Setenv` never reaches the fake; tests use `Runner.extraEnv`.
- **YouTube needs Deno and the matching yt-dlp-ejs.** `--no-remote-components` forbids
  fetching the challenge solver, so it must be installed locally at the version the
  pinned yt-dlp expects: bump both together in `requirements.txt`, never one alone.
- **`/tmp` is the only writable path.** The image runs with a read-only root; each call
  makes a scratch dir under `/tmp` for HOME, caches and `DENO_DIR`. No writable `/tmp`
  means every call fails with "could not create a scratch directory".
- **The server must not be PID 1.** Orphans of a killed process group are reparented
  to PID 1, and the Go server would leave them as zombies; `tini` is PID 1 to reap them.
- **Testdata is byte-exact.** Fixtures keep real quirks (CRLF in `manual.srt`, trailing
  spaces in `auto.vtt`), so pre-commit skips `testdata/`; never let an editor normalize
  them. A golden diff is a behavior change, never a way to turn a test green.
- **The live test hides from `go test ./...`** (`//go:build live`), but Lint compiles
  it via `go vet -tags live` and golangci-lint, so a break there fails Lint, not Test.
- **`www.youtube.com` must stay on any custom allowlist.** Every YouTube input (short
  links and bare ids too) is canonicalized to it and `search` requires it.

## Where knowledge goes

| Destination | Holds |
|---|---|
| Agent-local memory | machine-specific facts: tool paths, local Docker setup |
| This repo (this file, `docs/`, package doc comments) | everything needed to work here: gotchas, invariants, contract, test strategy |
