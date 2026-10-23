// Command yt-dlp-mcp is a read-only MCP server that searches online video
// sites and returns trimmed metadata, subtitle listings and clean transcripts
// by running yt-dlp. Configuration is read from YTDLP_MCP_* environment
// variables (see docs/CONTRACT.md); run with -h for the flag equivalents.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jedwards1230/yt-dlp-mcp/internal/config"
	"github.com/jedwards1230/yt-dlp-mcp/internal/mcpserver"
	"github.com/jedwards1230/yt-dlp-mcp/internal/version"
	"github.com/jedwards1230/yt-dlp-mcp/internal/ytdlp"
)

// shutdownGrace is how long in-flight tool calls get after SIGTERM. It stays
// under Kubernetes' default 30s termination grace period.
const shutdownGrace = 25 * time.Second

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "yt-dlp-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.LookupEnv, os.Stderr)
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		fmt.Println(version.Value)
		return nil
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	if _, err := exec.LookPath(cfg.YTDLPPath); err != nil {
		return fmt.Errorf("yt-dlp not found (%s=%q): %w", config.EnvYTDLPPath, cfg.YTDLPPath, err)
	}
	runner, err := ytdlp.New(ytdlp.Config{
		Path:           cfg.YTDLPPath,
		Timeout:        cfg.Timeout,
		MaxOutputBytes: cfg.MaxOutputBytes,
		MaxConcurrency: cfg.MaxConcurrency,
		Logger:         log,
	})
	if err != nil {
		return err
	}

	// Before any yt-dlp run: sweep scratch directories a killed predecessor
	// left in the temp dir (best effort; a failure only costs disk space).
	if n, err := runner.RemoveStaleScratch(); err != nil {
		log.Warn("removing stale scratch directories failed", "removed", n, "error", err)
	} else if n > 0 {
		log.Info("removed stale scratch directories", "count", n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ytdlpVersion, err := runner.Version(ctx)
	if err != nil {
		log.Warn("could not read the yt-dlp version", "error", err)
	}

	srv := mcpserver.New(mcpserver.Options{
		Backend:   runner,
		Allowlist: cfg.AllowedHosts,
		CacheTTL:  cfg.CacheTTL,
		Logger:    log,
	})
	handler := mcpserver.NewHandler(srv, cfg.AuthToken, log)

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.Addr, err)
	}
	auth := "disabled (restrict access with a gateway or network policy)"
	if cfg.AuthToken != "" {
		auth = "bearer token"
	}
	log.Info("yt-dlp-mcp listening",
		"version", version.Value,
		"yt_dlp_version", ytdlpVersion,
		"addr", ln.Addr().String(),
		"mcp_path", mcpserver.MCPPath,
		"auth", auth,
		"allowed_hosts", strings.Join(cfg.AllowedHosts.Hosts(), ","),
		"timeout", cfg.Timeout.String(),
		"max_concurrency", cfg.MaxConcurrency,
		"max_output_bytes", cfg.MaxOutputBytes,
		"cache_ttl", cfg.CacheTTL.String(),
	)
	return mcpserver.Serve(ctx, ln, handler, shutdownGrace, log)
}
