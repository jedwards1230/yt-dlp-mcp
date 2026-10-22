// Package live holds the live smoke test: a real MCP client that talks to a
// running yt-dlp-mcp server over HTTP and exercises every tool against
// public YouTube. It is compiled only with the "live" build tag, so the
// default `go test ./...` never touches the network. See docs/TESTING.md.
package live
