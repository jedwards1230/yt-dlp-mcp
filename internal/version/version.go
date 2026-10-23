// Package version holds the build version reported in the MCP initialize
// handshake and the startup log line.
package version

// Value is the server version. Release builds overwrite it with
// -ldflags "-X github.com/jedwards1230/yt-dlp-mcp/internal/version.Value=<version>".
var Value = "dev"
