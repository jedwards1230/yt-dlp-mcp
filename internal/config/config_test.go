package config

import (
	"errors"
	"flag"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(nil, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" || cfg.YTDLPPath != "yt-dlp" || cfg.Timeout != 60*time.Second ||
		cfg.MaxOutputBytes != 16<<20 || cfg.MaxConcurrency != 4 || cfg.AuthToken != "" ||
		cfg.LogLevel != slog.LevelInfo || cfg.CacheTTL != 10*time.Minute {
		t.Fatalf("defaults = %+v", cfg)
	}
	if got := strings.Join(cfg.AllowedHosts.Hosts(), ","); got != "m.youtube.com,music.youtube.com,www.youtube-nocookie.com,www.youtube.com,youtu.be,youtube-nocookie.com,youtube.com" {
		t.Fatalf("default hosts = %s", got)
	}
}

func TestEnvAndFlagPrecedence(t *testing.T) {
	e := env(map[string]string{
		EnvAddr:           "127.0.0.1:9000",
		EnvYTDLPPath:      "/opt/yt-dlp",
		EnvAllowedHosts:   " www.youtube.com , videos.example.org ",
		EnvTimeout:        "90s",
		EnvMaxOutputBytes: "1048576",
		EnvMaxConcurrency: "8",
		EnvAuthToken:      "  s3cret  ",
		EnvLogLevel:       "DEBUG",
		EnvCacheTTL:       "0",
	})
	cfg, err := Load([]string{"-timeout", "2m", "-max-concurrency", "2"}, e, io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9000" || cfg.YTDLPPath != "/opt/yt-dlp" || cfg.Timeout != 2*time.Minute ||
		cfg.MaxOutputBytes != 1<<20 || cfg.MaxConcurrency != 2 || cfg.AuthToken != "s3cret" ||
		cfg.LogLevel != slog.LevelDebug || cfg.CacheTTL != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !cfg.AllowedHosts.Allows("videos.example.org") || cfg.AllowedHosts.Allows("youtu.be") {
		t.Fatalf("hosts = %v", cfg.AllowedHosts.Hosts())
	}
}

func TestInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"bad timeout", map[string]string{EnvTimeout: "soon"}, nil, EnvTimeout},
		{"timeout too short", map[string]string{EnvTimeout: "10ms"}, nil, "outside"},
		{"timeout too long", map[string]string{EnvTimeout: "1h"}, nil, "outside"},
		{"output not a number", map[string]string{EnvMaxOutputBytes: "16MiB"}, nil, "whole number of bytes"},
		{"output too small", map[string]string{EnvMaxOutputBytes: "100"}, nil, "outside"},
		{"concurrency zero", map[string]string{EnvMaxConcurrency: "0"}, nil, "outside 1-64"},
		{"log level", map[string]string{EnvLogLevel: "loud"}, nil, EnvLogLevel},
		{"cache negative", map[string]string{EnvCacheTTL: "-1s"}, nil, "outside"},
		{"ip in allowlist", map[string]string{EnvAllowedHosts: "10.0.0.1"}, nil, EnvAllowedHosts},
		{"empty allowlist", map[string]string{EnvAllowedHosts: ","}, nil, "empty"},
		{"stray argument", nil, []string{"serve"}, "unexpected arguments"},
		{"unknown flag", nil, []string{"-auth-token", "x"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.args, env(tt.env), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestMultipleErrorsReported(t *testing.T) {
	_, err := Load(nil, env(map[string]string{EnvTimeout: "x", EnvMaxConcurrency: "y"}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), EnvTimeout) || !strings.Contains(err.Error(), EnvMaxConcurrency) {
		t.Fatalf("err = %v, want both problems listed", err)
	}
}

func TestHelp(t *testing.T) {
	if _, err := Load([]string{"-h"}, env(nil), io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
}
