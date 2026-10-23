// Package live holds the live smoke test: it starts the built container
// image (or uses an already-running server), completes a real MCP handshake
// over streamable HTTP and calls every tool against public YouTube. It is
// compiled only with the "live" build tag, so the default `go test ./...`
// never touches the network or Docker. The image must be built with BuildKit
// (`docker buildx build --load -t yt-dlp-mcp:smoke .`); the legacy builder
// cannot run the Dockerfile. See live_test.go for the variables that
// configure it.
package live
