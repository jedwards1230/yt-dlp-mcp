package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jedwards1230/yt-dlp-mcp/internal/urlguard"
	"github.com/jedwards1230/yt-dlp-mcp/internal/ytdlp"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// stubBackend returns canned data; block, when set, makes Info wait for its
// context (to test shutdown and cancellation).
type stubBackend struct {
	info    ytdlp.VideoInfo
	block   bool
	started chan struct{}
	ended   atomic.Bool
}

func (s *stubBackend) Search(context.Context, string, int) ([]ytdlp.SearchEntry, error) {
	return nil, nil
}

func (s *stubBackend) Info(ctx context.Context, _ string) (ytdlp.VideoInfo, ytdlp.Tracks, error) {
	if s.block {
		close(s.started)
		<-ctx.Done()
		s.ended.Store(true)
		return ytdlp.VideoInfo{}, ytdlp.Tracks{}, ctx.Err()
	}
	return s.info, ytdlp.Tracks{}, nil
}

func (s *stubBackend) Subtitles(context.Context, string) (ytdlp.VideoInfo, ytdlp.Tracks, error) {
	return s.info, ytdlp.Tracks{}, nil
}

func (s *stubBackend) Transcript(context.Context, string, string, ytdlp.Source) (ytdlp.SubtitleFile, error) {
	return ytdlp.SubtitleFile{}, nil
}

func stubServer(t *testing.T, b Backend) *mcp.Server {
	t.Helper()
	allow, err := urlguard.NewAllowlist(urlguard.DefaultHosts)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Backend: b, Allowlist: allow})
}

func TestHealthz(t *testing.T) {
	ts := httptest.NewServer(NewHandler(stubServer(t, &stubBackend{}), "secret-token", nil))
	defer ts.Close()

	resp, err := http.Get(ts.URL + HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("GET /healthz with auth enabled = %d %q, want 200 ok", resp.StatusCode, body)
	}

	post, err := http.Post(ts.URL+HealthPath, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want 405", post.StatusCode)
	}
}

func TestBearerAuth(t *testing.T) {
	const token = "s3cret-token"
	ts := httptest.NewServer(NewHandler(stubServer(t, &stubBackend{}), token, nil))
	defer ts.Close()

	for name, header := range map[string]string{
		"missing":      "",
		"wrong token":  "Bearer nope",
		"wrong scheme": "Basic " + token,
		"bare token":   token,
		"prefix only":  "Bearer " + token[:4],
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+MCPPath, strings.NewReader(`{}`))
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
				t.Fatalf("status %d, WWW-Authenticate %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
			}
			if strings.Contains(string(body), token) {
				t.Fatal("response body echoes the token")
			}
		})
	}

	// The SDK client cannot even initialize without the token...
	client := mcp.NewClient(&mcp.Implementation{Name: "anon", Version: "0"}, nil)
	if s, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: ts.URL + MCPPath, MaxRetries: -1}, nil); err == nil {
		_ = s.Close()
		t.Fatal("unauthenticated client connected")
	}
	// ...and completes a tools/list round trip with it.
	authed := &http.Client{Transport: headerTransport{base: http.DefaultTransport, header: "Bearer " + token}}
	s, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: ts.URL + MCPPath, HTTPClient: authed}, nil)
	if err != nil {
		t.Fatalf("authenticated Connect: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.ListTools(t.Context(), nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
}

func TestNoAuthWhenTokenEmpty(t *testing.T) {
	ts := httptest.NewServer(NewHandler(stubServer(t, &stubBackend{}), "", nil))
	defer ts.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "anon", Version: "0"}, nil)
	s, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: ts.URL + MCPPath}, nil)
	if err != nil {
		t.Fatalf("Connect without auth configured: %v", err)
	}
	_ = s.Close()
}

// TestServeGracefulShutdown starts Serve on a real listener, begins a tool
// call that blocks inside the backend, cancels Serve's context, and checks
// that Serve returns once the grace period ends and the call's context is
// canceled (which is what kills a real yt-dlp process).
func TestServeGracefulShutdown(t *testing.T) {
	b := &stubBackend{block: true, started: make(chan struct{})}
	h := NewHandler(stubServer(t, b), "", nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, h, 300*time.Millisecond, discardLogger()) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: "http://" + ln.Addr().String() + MCPPath, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	go func() {
		_, _ = sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_info", Arguments: map[string]any{"url": "dQw4w9WgXcQ"}})
	}()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool call never reached the backend")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the grace period")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !b.ended.Load() {
		if time.Now().After(deadline) {
			t.Fatal("in-flight tool call context was not canceled on shutdown")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeReturnsListenerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	if err := Serve(t.Context(), ln, http.NotFoundHandler(), time.Second, nil); err == nil {
		t.Fatal("Serve on a closed listener returned nil")
	}
}
