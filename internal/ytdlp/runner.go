// Package ytdlp runs the yt-dlp binary for the MCP tools.
//
// Every invocation goes through [Runner.run], which owns the safety
// properties of the subprocess:
//
//   - argv only: exec.CommandContext with no shell, and the user-derived
//     target is always the last argument, after a "--" end-of-options marker,
//     so it can never be parsed as a flag;
//   - a fixed set of base flags ([BaseArgs]) that keep yt-dlp read-only and
//     self-contained: no config files, plugins, cookies, cache, remote
//     components or generic extractor;
//   - a per-call scratch directory (HOME, caches and any subtitle file live
//     there) that is removed when the call returns;
//   - a sanitized environment that passes through only PATH, locale, TLS and
//     proxy variables, so server secrets never reach the child;
//   - a per-call deadline and a stdout byte cap, both enforced by killing the
//     whole process group (yt-dlp and any JavaScript runtime it spawned); the
//     deadline covers the wait for a concurrency slot as well as the run, so
//     no call outlives the configured timeout;
//   - a global concurrency limit shared by all tools.
package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BaseArgs are passed to every yt-dlp invocation, before the operation's own
// flags. Each one closes a door the tools never need open.
var BaseArgs = []string{
	"--ignore-config",                      // no system/user/portable config files
	"--no-plugin-dirs",                     // no extractor/postprocessor plugins
	"--no-remote-components",               // never fetch JS components; yt-dlp-ejs is installed locally
	"--use-extractors", "default,-generic", // no generic "fetch any page" extractor
	"--no-cache-dir",            // no persistent cache shared between calls
	"--no-cookies",              // never read or write a cookie jar
	"--no-cookies-from-browser", // never read browser profiles
	"--color", "never",
	"--no-progress",
}

// stderrTailBytes is how much of yt-dlp's stderr is kept (the tail, where the
// final ERROR line lands) for error messages.
const stderrTailBytes = 64 << 10

// waitDelay bounds how long Wait lingers for output pipes held open by an
// orphaned grandchild after yt-dlp itself has exited.
const waitDelay = 2 * time.Second

// passthroughEnv is the complete list of parent environment variables a
// yt-dlp child inherits.
var passthroughEnv = []string{
	"PATH", "LANG", "LC_ALL", "TZ",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
}

// Config configures a Runner.
type Config struct {
	// Path is the yt-dlp executable (a name looked up on PATH, or a path).
	Path string
	// Timeout bounds each call end to end: the wait for a free concurrency
	// slot plus the yt-dlp run share one deadline.
	Timeout time.Duration
	// MaxOutputBytes caps yt-dlp's stdout and the size of a subtitle file
	// read back from disk.
	MaxOutputBytes int64
	// MaxConcurrency is the number of yt-dlp processes allowed at once.
	MaxConcurrency int
	// TempDir is the parent of the per-call scratch directories; empty means
	// os.TempDir().
	TempDir string
	// Logger receives per-invocation debug/info records; nil discards them.
	Logger *slog.Logger
}

// Runner executes yt-dlp. It is safe for concurrent use.
type Runner struct {
	cfg Config
	sem chan struct{}
	log *slog.Logger
	// extraEnv is appended to every child environment. Only tests set it,
	// to steer the fake yt-dlp past the sanitized environment.
	extraEnv []string
}

// New validates cfg and returns a Runner.
func New(cfg Config) (*Runner, error) {
	switch {
	case cfg.Path == "":
		return nil, errors.New("yt-dlp path is empty")
	case cfg.Timeout <= 0:
		return nil, fmt.Errorf("timeout must be positive, got %s", cfg.Timeout)
	case cfg.MaxOutputBytes <= 0:
		return nil, fmt.Errorf("max output bytes must be positive, got %d", cfg.MaxOutputBytes)
	case cfg.MaxConcurrency <= 0:
		return nil, fmt.Errorf("max concurrency must be positive, got %d", cfg.MaxConcurrency)
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Runner{cfg: cfg, sem: make(chan struct{}, cfg.MaxConcurrency), log: log}, nil
}

// ErrorKind classifies a failed invocation.
type ErrorKind int

// Error kinds. The zero value, KindFailed, is a non-zero yt-dlp exit.
const (
	KindFailed ErrorKind = iota
	KindTimeout
	KindCanceled
	KindOutputLimit
	KindBusy
	KindStart
	KindParse
	KindNoSubtitles
	KindInvalid
)

// Error is a failed yt-dlp invocation. Msg is written for the calling agent.
type Error struct {
	Kind ErrorKind
	Msg  string
	Err  error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

var (
	errTimeout     = errors.New("yt-dlp timed out")
	errOutputLimit = errors.New("yt-dlp output limit exceeded")
)

// Version runs "yt-dlp --version". It is used for the startup log line and
// does not take a concurrency slot.
func (r *Runner) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp(r.cfg.TempDir, "yt-dlp-mcp-*")
	if err != nil {
		return "", fmt.Errorf("creating scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cmd := exec.CommandContext(ctx, r.cfg.Path, "--version") // #nosec G204 -- the binary path is operator configuration
	cmd.Dir = dir
	cmd.Env = r.childEnv(dir)
	setProcessGroup(cmd)
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("running %s --version: %w", r.cfg.Path, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// run executes yt-dlp with BaseArgs, the operation's flags from build, "--"
// and target, inside a fresh scratch directory. On a zero exit it calls
// collect with that directory and the captured stdout before the directory
// is removed.
//
// One deadline of cfg.Timeout covers the whole call: waiting for a
// concurrency slot, running yt-dlp and collecting its output. A call that
// queued for a slot therefore has correspondingly less time to run.
func (r *Runner) run(ctx context.Context, op string, build func(dir string) []string, target string, collect func(dir string, stdout []byte) error) error {
	timeoutCtx, cancelTimeout := context.WithTimeoutCause(ctx, r.cfg.Timeout, errTimeout)
	defer cancelTimeout()

	queued := time.Now()
	if err := r.acquire(ctx, timeoutCtx); err != nil {
		return err
	}
	defer func() { <-r.sem }()
	waited := time.Since(queued)

	dir, err := os.MkdirTemp(r.cfg.TempDir, "yt-dlp-mcp-*")
	if err != nil {
		return &Error{Kind: KindStart, Msg: fmt.Sprintf("could not create a scratch directory: %v", err), Err: err}
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			r.log.Warn("removing scratch directory failed", "dir", dir, "error", err)
		}
	}()

	runCtx, cancel := context.WithCancelCause(timeoutCtx)
	defer cancel(nil)

	args := make([]string, 0, len(BaseArgs)+16)
	args = append(args, BaseArgs...)
	args = append(args, build(dir)...)
	args = append(args, "--", target)

	stdout := &capBuffer{limit: r.cfg.MaxOutputBytes, onOverflow: func() { cancel(errOutputLimit) }}
	stderr := &tailBuffer{limit: stderrTailBytes}
	// #nosec G204 -- the binary path is operator configuration and argv is
	// fixed flags plus a validated target after "--"; no shell is involved.
	cmd := exec.CommandContext(runCtx, r.cfg.Path, args...)
	cmd.Dir = dir
	cmd.Env = r.childEnv(dir)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	cmd.WaitDelay = waitDelay

	r.log.Debug("running yt-dlp", "op", op, "args", args, "slot_wait_ms", waited.Milliseconds())
	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	if errors.Is(runErr, exec.ErrWaitDelay) {
		// yt-dlp exited but a grandchild kept the pipes open: reap the group.
		_ = killGroup(cmd)
		runErr = nil
	}
	if err := r.classify(ctx, runCtx, runErr, waited, stdout, stderr); err != nil {
		r.log.Info("yt-dlp failed", "op", op, "duration_ms", elapsed.Milliseconds(), "error", err.Error())
		return err
	}
	r.log.Info("yt-dlp finished", "op", op, "duration_ms", elapsed.Milliseconds(), "stdout_bytes", stdout.buf.Len())
	return collect(dir, stdout.buf.Bytes())
}

// classify turns the outcome of cmd.Run into an *Error, or nil on success.
// Context causes are checked before the exit status: a killed process exits
// non-zero, but the useful message is why it was killed. waited is how long
// the call queued for a slot, which a timeout message reports when it ate a
// noticeable part of the deadline.
func (r *Runner) classify(parent, runCtx context.Context, runErr error, waited time.Duration, stdout *capBuffer, stderr *tailBuffer) error {
	if stdout.overflowed() || errors.Is(context.Cause(runCtx), errOutputLimit) {
		return &Error{Kind: KindOutputLimit, Msg: fmt.Sprintf("yt-dlp output exceeded the %d-byte limit (YTDLP_MCP_MAX_OUTPUT_BYTES) and was stopped", r.cfg.MaxOutputBytes)}
	}
	if parent.Err() != nil {
		return &Error{Kind: KindCanceled, Msg: "the request was canceled before yt-dlp finished", Err: parent.Err()}
	}
	if errors.Is(context.Cause(runCtx), errTimeout) {
		if waited >= time.Second {
			return &Error{Kind: KindTimeout, Msg: fmt.Sprintf("yt-dlp timed out after %s (YTDLP_MCP_TIMEOUT), %s of it waiting for a free yt-dlp slot (YTDLP_MCP_MAX_CONCURRENCY); the server is busy — retry shortly", r.cfg.Timeout, waited.Round(time.Second)), Err: errTimeout}
		}
		return &Error{Kind: KindTimeout, Msg: fmt.Sprintf("yt-dlp timed out after %s (YTDLP_MCP_TIMEOUT); the site may be slow or blocking requests — retry later", r.cfg.Timeout), Err: errTimeout}
	}
	if runErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return &Error{Kind: KindFailed, Msg: "yt-dlp failed: " + stderrSummary(stderr.String(), exitErr.ExitCode()), Err: runErr}
	}
	var pathErr *fs.PathError
	if errors.Is(runErr, exec.ErrNotFound) || errors.As(runErr, &pathErr) {
		return &Error{Kind: KindStart, Msg: fmt.Sprintf("could not start yt-dlp at %q (YTDLP_MCP_YTDLP_PATH): %v", r.cfg.Path, runErr), Err: runErr}
	}
	return &Error{Kind: KindFailed, Msg: fmt.Sprintf("running yt-dlp: %v", runErr), Err: runErr}
}

// acquire takes a concurrency slot, waiting until deadline (the call's
// timeout context, derived from parent) is done.
func (r *Runner) acquire(parent, deadline context.Context) error {
	select {
	case r.sem <- struct{}{}:
		return nil
	default:
	}
	select {
	case r.sem <- struct{}{}:
		return nil
	case <-deadline.Done():
		if err := parent.Err(); err != nil {
			return &Error{Kind: KindCanceled, Msg: "the request was canceled while waiting for a free yt-dlp slot", Err: err}
		}
		return &Error{Kind: KindBusy, Msg: fmt.Sprintf("server busy: all %d yt-dlp slots (YTDLP_MCP_MAX_CONCURRENCY) stayed in use for %s; retry shortly", cap(r.sem), r.cfg.Timeout)}
	}
}

// childEnv builds the complete environment for a yt-dlp child: scratch-dir
// HOME and caches, deterministic Python I/O, and the passthrough variables.
func (r *Runner) childEnv(dir string) []string {
	env := []string{
		"HOME=" + dir,
		"TMPDIR=" + dir,
		"XDG_CACHE_HOME=" + filepath.Join(dir, ".cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(dir, ".config"),
		"DENO_DIR=" + filepath.Join(dir, ".deno"),
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
		"NO_COLOR=1",
	}
	for _, k := range passthroughEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, r.extraEnv...)
}

// stderrSummary picks the most useful line of yt-dlp's stderr: the last
// "ERROR:" line, else the last non-empty line.
func stderrSummary(stderr string, code int) string {
	var last, lastErr string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		last = l
		if strings.HasPrefix(l, "ERROR:") {
			lastErr = l
		}
	}
	msg := lastErr
	if msg == "" {
		msg = last
	}
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "ERROR:"))
	if msg == "" {
		return fmt.Sprintf("exited with status %d and no error message", code)
	}
	const maxLen = 600
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}

// capBuffer keeps at most limit bytes. On the first write past the limit it
// calls onOverflow (which kills the process) and silently discards the rest,
// so the child never blocks on a full pipe while it is being killed.
type capBuffer struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	limit      int64
	over       bool
	onOverflow func()
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.over {
		return len(p), nil
	}
	if room := c.limit - int64(c.buf.Len()); int64(len(p)) > room {
		c.buf.Write(p[:max(room, 0)])
		c.over = true
		if c.onOverflow != nil {
			c.onOverflow()
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capBuffer) overflowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
