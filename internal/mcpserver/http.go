package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTP paths.
const (
	MCPPath    = "/mcp"
	HealthPath = "/healthz"
)

// NewHandler mounts srv as a streamable-HTTP MCP endpoint at /mcp and a
// liveness probe at GET /healthz.
//
// When authToken is non-empty, /mcp requires "Authorization: Bearer <token>"
// and answers 401 otherwise. /healthz is always unauthenticated: it reveals
// nothing and must work for kubelet probes. With an empty token /mcp is open,
// and the deployment is expected to restrict who can reach it (an
// authenticating gateway in front, a NetworkPolicy, or a loopback bind).
func NewHandler(srv *mcp.Server, authToken string, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// Stateless: every request is self-contained, so any replica can answer
	// it and no session state accumulates.
	//
	// PropagateRequestCancellation ties a tool call's context to its HTTP
	// request, so a client that disconnects also kills the yt-dlp process
	// working for it, but go-sdk applies it only to clients speaking
	// protocol 2026-07-28 or later. For older clients a disconnect does not
	// cancel the call: yt-dlp runs on until it finishes or YTDLP_MCP_TIMEOUT
	// expires, which still bounds it.
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true},
	)
	if authToken != "" {
		mcpHandler = bearerAuth(authToken, mcpHandler, log)
	}

	mux := http.NewServeMux()
	mux.Handle(MCPPath, mcpHandler)
	mux.HandleFunc("GET "+HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	return mux
}

// bearerAuth compares the whole Authorization header in constant time
// against the expected value. The token is never logged or echoed.
func bearerAuth(token string, next http.Handler, log *slog.Logger) http.Handler {
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), expected) != 1 {
			log.Debug("rejected unauthenticated MCP request", "remote", r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="yt-dlp-mcp", error="invalid_token"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewHTTPServer returns the http.Server for h. WriteTimeout is deliberately
// unset: a streamable-HTTP response lives as long as the tool call, which is
// bounded by the yt-dlp timeout instead.
func NewHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Serve serves h on ln until ctx is canceled, then shuts down gracefully:
// it stops accepting connections and waits up to shutdownTimeout for
// in-flight tool calls before closing their connections. Closing a
// connection cancels the tool call, and with it the yt-dlp process, only for
// clients the SDK propagates request cancellation for (see NewHandler); an
// older client's call is left to its YTDLP_MCP_TIMEOUT deadline.
func Serve(ctx context.Context, ln net.Listener, h http.Handler, shutdownTimeout time.Duration, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	srv := NewHTTPServer(h)
	errCh := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	log.Info("shutting down", "grace", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if errors.Is(shutdownErr, context.DeadlineExceeded) {
		log.Warn("grace period over; closing remaining connections")
		shutdownErr = srv.Close()
	}
	if err := <-errCh; err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	if shutdownErr != nil {
		return fmt.Errorf("http shutdown: %w", shutdownErr)
	}
	return nil
}
