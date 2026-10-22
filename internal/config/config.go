// Package config loads the server configuration from YTDLP_MCP_* environment
// variables, with command-line flags as optional overrides (a flag wins over
// its variable, which wins over the default).
//
// The bearer token is deliberately environment-only: a flag value would be
// visible to every user on the host through the process list.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jedwards1230/yt-dlp-mcp/internal/urlguard"
)

// Environment variable names. docs/CONTRACT.md lists them with defaults; the
// Helm chart maps config.* values onto them.
const (
	EnvAddr           = "YTDLP_MCP_ADDR"
	EnvYTDLPPath      = "YTDLP_MCP_YTDLP_PATH"
	EnvAllowedHosts   = "YTDLP_MCP_ALLOWED_HOSTS"
	EnvTimeout        = "YTDLP_MCP_TIMEOUT"
	EnvMaxOutputBytes = "YTDLP_MCP_MAX_OUTPUT_BYTES"
	EnvMaxConcurrency = "YTDLP_MCP_MAX_CONCURRENCY"
	EnvAuthToken      = "YTDLP_MCP_AUTH_TOKEN" // #nosec G101 -- an environment variable name, not a credential
	EnvLogLevel       = "YTDLP_MCP_LOG_LEVEL"
	EnvCacheTTL       = "YTDLP_MCP_CACHE_TTL"
)

// Defaults.
const (
	DefaultAddr           = ":8080"
	DefaultYTDLPPath      = "yt-dlp"
	DefaultTimeout        = 60 * time.Second
	DefaultMaxOutputBytes = 16 << 20
	DefaultMaxConcurrency = 4
	DefaultCacheTTL       = 10 * time.Minute
)

// Bounds that keep a misconfiguration from becoming an outage or a footgun.
const (
	minTimeout        = time.Second
	maxTimeout        = 10 * time.Minute
	minMaxOutputBytes = 64 << 10
	maxMaxOutputBytes = 256 << 20
	maxMaxConcurrency = 64
	maxCacheTTL       = 24 * time.Hour
)

// Config is the validated server configuration.
type Config struct {
	Addr           string
	YTDLPPath      string
	AllowedHosts   *urlguard.Allowlist
	Timeout        time.Duration
	MaxOutputBytes int64
	MaxConcurrency int
	// AuthToken guards /mcp when non-empty. It is never logged.
	AuthToken string
	LogLevel  slog.Level
	// CacheTTL is how long a fetched transcript is kept for pagination; 0
	// disables the cache.
	CacheTTL time.Duration
	// ShowVersion asks main to print the version and exit.
	ShowVersion bool
}

// Load parses args (without the program name) over the environment read
// through getenv. Usage and flag errors are written to stderr. A -h/-help
// request returns flag.ErrHelp.
func Load(args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	envOr := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	fs := flag.NewFlagSet("yt-dlp-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", envOr(EnvAddr, DefaultAddr), "listen address ("+EnvAddr+")")
	ytdlpPath := fs.String("yt-dlp-path", envOr(EnvYTDLPPath, DefaultYTDLPPath), "yt-dlp executable ("+EnvYTDLPPath+")")
	hosts := fs.String("allowed-hosts", envOr(EnvAllowedHosts, strings.Join(urlguard.DefaultHosts, ",")), "comma-separated exact hostnames tools may fetch ("+EnvAllowedHosts+")")
	timeout := fs.String("timeout", envOr(EnvTimeout, DefaultTimeout.String()), "per-call yt-dlp timeout, Go duration ("+EnvTimeout+")")
	maxOut := fs.String("max-output-bytes", envOr(EnvMaxOutputBytes, strconv.Itoa(DefaultMaxOutputBytes)), "cap on yt-dlp stdout and subtitle file size, bytes ("+EnvMaxOutputBytes+")")
	maxConc := fs.String("max-concurrency", envOr(EnvMaxConcurrency, strconv.Itoa(DefaultMaxConcurrency)), "concurrent yt-dlp processes ("+EnvMaxConcurrency+")")
	logLevel := fs.String("log-level", envOr(EnvLogLevel, "info"), "debug, info, warn or error ("+EnvLogLevel+")")
	cacheTTL := fs.String("cache-ttl", envOr(EnvCacheTTL, DefaultCacheTTL.String()), "transcript cache lifetime for pagination, Go duration; 0 disables ("+EnvCacheTTL+")")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %q", fs.Args())
	}

	cfg := Config{
		Addr:        strings.TrimSpace(*addr),
		YTDLPPath:   strings.TrimSpace(*ytdlpPath),
		AuthToken:   strings.TrimSpace(getenv(EnvAuthToken)),
		ShowVersion: *showVersion,
	}
	var errs []error
	if cfg.Addr == "" {
		errs = append(errs, fmt.Errorf("%s must not be empty", EnvAddr))
	}
	if cfg.YTDLPPath == "" {
		errs = append(errs, fmt.Errorf("%s must not be empty", EnvYTDLPPath))
	}

	allow, err := urlguard.NewAllowlist(strings.Split(*hosts, ","))
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvAllowedHosts, err))
	}
	cfg.AllowedHosts = allow

	if cfg.Timeout, err = time.ParseDuration(*timeout); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvTimeout, err))
	} else if cfg.Timeout < minTimeout || cfg.Timeout > maxTimeout {
		errs = append(errs, fmt.Errorf("%s=%s is outside %s-%s", EnvTimeout, cfg.Timeout, minTimeout, maxTimeout))
	}

	if cfg.MaxOutputBytes, err = strconv.ParseInt(*maxOut, 10, 64); err != nil {
		errs = append(errs, fmt.Errorf("%s: %q is not a whole number of bytes", EnvMaxOutputBytes, *maxOut))
	} else if cfg.MaxOutputBytes < minMaxOutputBytes || cfg.MaxOutputBytes > maxMaxOutputBytes {
		errs = append(errs, fmt.Errorf("%s=%d is outside %d-%d", EnvMaxOutputBytes, cfg.MaxOutputBytes, minMaxOutputBytes, maxMaxOutputBytes))
	}

	if cfg.MaxConcurrency, err = strconv.Atoi(*maxConc); err != nil {
		errs = append(errs, fmt.Errorf("%s: %q is not a whole number", EnvMaxConcurrency, *maxConc))
	} else if cfg.MaxConcurrency < 1 || cfg.MaxConcurrency > maxMaxConcurrency {
		errs = append(errs, fmt.Errorf("%s=%d is outside 1-%d", EnvMaxConcurrency, cfg.MaxConcurrency, maxMaxConcurrency))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(*logLevel)); err != nil {
		errs = append(errs, fmt.Errorf("%s: %q is not one of debug, info, warn, error", EnvLogLevel, *logLevel))
	}

	if cfg.CacheTTL, err = time.ParseDuration(*cacheTTL); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvCacheTTL, err))
	} else if cfg.CacheTTL < 0 || cfg.CacheTTL > maxCacheTTL {
		errs = append(errs, fmt.Errorf("%s=%s is outside 0-%s", EnvCacheTTL, cfg.CacheTTL, maxCacheTTL))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
