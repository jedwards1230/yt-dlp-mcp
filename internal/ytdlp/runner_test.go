package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakePath is the testdata/fake-yt-dlp script (see its header for the
// fixture ids it understands).
func fakePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "fake-yt-dlp"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newRunner(t *testing.T, mutate ...func(*Config)) *Runner {
	t.Helper()
	cfg := Config{
		Path:           fakePath(t),
		Timeout:        10 * time.Second,
		MaxOutputBytes: 1 << 20,
		MaxConcurrency: 2,
		TempDir:        t.TempDir(),
	}
	for _, m := range mutate {
		m(&cfg)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// recordArgv makes the fake write its argv to a file and returns a reader.
func recordArgv(t *testing.T, r *Runner) func() []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "argv")
	r.extraEnv = []string{"FAKE_YTDLP_ARGV_FILE=" + path}
	return func() []string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading recorded argv: %v", err)
		}
		return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
}

func wantKind(t *testing.T, err error, kind ErrorKind) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v (%T) is not an *Error", err, err)
	}
	if e.Kind != kind {
		t.Fatalf("error kind = %d (%q), want %d", e.Kind, e.Msg, kind)
	}
	return e
}

const manualURL = "https://www.youtube.com/watch?v=vidManual01"

// TestArgvSafety pins the invocation contract: BaseArgs first, the target
// last and directly after "--", and none of the flags that would widen what
// yt-dlp may do.
func TestArgvSafety(t *testing.T) {
	r := newRunner(t)
	ops := map[string]func() error{
		"search": func() error { _, err := r.Search(t.Context(), "-x --exec id", 2); return err },
		"info":   func() error { _, _, err := r.Info(t.Context(), manualURL); return err },
		"subs":   func() error { _, _, err := r.Subtitles(t.Context(), manualURL); return err },
		"transcript": func() error {
			_, err := r.Transcript(t.Context(), manualURL, "en", SourceAny)
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			argv := recordArgv(t, r)
			if err := op(); err != nil {
				t.Fatalf("op: %v", err)
			}
			args := argv()
			if !slices.Equal(args[:len(BaseArgs)], BaseArgs) {
				t.Fatalf("argv does not start with BaseArgs:\n%q", args)
			}
			n := len(args)
			if args[n-2] != "--" {
				t.Fatalf("second-to-last arg = %q, want \"--\" (argv %q)", args[n-2], args)
			}
			if slices.Index(args, "--") != n-2 {
				t.Fatalf("\"--\" appears before the end: %q", args)
			}
			for _, banned := range []string{"--remote-components", "--cookies", "--cookies-from-browser", "--exec", "--plugin-dirs", "--config-locations", "--load-info-json", "--batch-file", "-a"} {
				if slices.Contains(args[:n-1], banned) {
					t.Errorf("argv contains %s: %q", banned, args)
				}
			}
			if i := slices.Index(args, "--use-extractors"); i < 0 || args[i+1] != "default,-generic" {
				t.Errorf("generic extractor not disabled: %q", args)
			}
			if name != "search" && !slices.Contains(args, "--no-playlist") {
				t.Errorf("single-video op without --no-playlist: %q", args)
			}
			if !slices.Contains(args, "--skip-download") {
				t.Errorf("missing --skip-download: %q", args)
			}
		})
	}
}

func TestSearch(t *testing.T) {
	r := newRunner(t)
	argv := recordArgv(t, r)
	got, err := r.Search(t.Context(), "bookshelf build", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if args := argv(); args[len(args)-1] != "ytsearch2:bookshelf build" || !slices.Contains(args, "--flat-playlist") {
		t.Fatalf("search argv = %q", args)
	}
	if len(got) != 2 || got[0].ID != "vidManual01" || got[0].Channel != "Example Workshop" || *got[0].Duration != 754 || got[1].Title != "Estantería en una tarde" {
		t.Fatalf("Search = %+v", got)
	}
	if _, err := r.Search(t.Context(), "fake-error", 1); err == nil {
		t.Fatal("failing search returned no error")
	} else if e := wantKind(t, err, KindFailed); e.Msg != "yt-dlp failed: [youtube:search] search failed" {
		t.Fatalf("message = %q", e.Msg)
	}
}

func TestInfo(t *testing.T) {
	r := newRunner(t)
	info, tracks, err := r.Info(t.Context(), manualURL)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.ID != "vidManual01" || info.UploadDate != "20240315" || len(info.Chapters) != 2 || info.Chapters[1].Title != "Assembly" || *info.ViewCount != 12345 || info.CommentCount != nil {
		t.Fatalf("info = %+v", info)
	}
	if got := codes(tracks.Manual); !slices.Equal(got, []string{"en", "de"}) {
		t.Fatalf("manual codes = %q", got)
	}
	if tracks.OriginalAutomatic() != "en-orig" {
		t.Fatalf("original automatic = %q", tracks.OriginalAutomatic())
	}

	_, tracks, err = r.Info(t.Context(), "https://www.youtube.com/watch?v=vidNoSubs00")
	if err != nil || tracks.Manual != nil || tracks.Automatic != nil {
		t.Fatalf("no-subs video: tracks %+v err %v", tracks, err)
	}
}

func TestSubtitlesNames(t *testing.T) {
	r := newRunner(t)
	_, tracks, err := r.Subtitles(t.Context(), manualURL)
	if err != nil {
		t.Fatalf("Subtitles: %v", err)
	}
	wantManual := []Track{{"en", "English"}, {"de", "German"}}
	wantAuto := []Track{{"en", ""}, {"de", ""}, {"en-orig", "English (Original)"}, {"fr", "French"}}
	if !slices.Equal(tracks.Manual, wantManual) || !slices.Equal(tracks.Automatic, wantAuto) {
		t.Fatalf("tracks = %+v", tracks)
	}
}

func TestZipTracksMisaligned(t *testing.T) {
	got := zipTracks("en, fr", `[{"name": "English", "ext": "vtt"}]`)
	if !slices.Equal(got, []Track{{"en", ""}, {"fr", ""}}) {
		t.Fatalf("misaligned names were applied: %+v", got)
	}
	if zipTracks("NA", "") != nil || zipTracks("", "[]") != nil {
		t.Fatal("empty key lines should yield no tracks")
	}
}

func TestTranscript(t *testing.T) {
	r := newRunner(t)
	tests := []struct {
		name, url, lang string
		src             Source
		wantFormat      string
		wantAuto        bool
		wantWrite       []string
		wantNoWrite     string
	}{
		{"manual preferred", manualURL, "en", SourceAny, "json3", false, []string{"--write-subs", "--write-auto-subs"}, ""},
		{"manual vtt", manualURL, "de", SourceManual, "vtt", false, []string{"--write-subs"}, "--write-auto-subs"},
		{"forced auto", manualURL, "en", SourceAutomatic, "json3", true, []string{"--write-auto-subs"}, "--write-subs"},
		{"auto fallback", "https://www.youtube.com/watch?v=vidAutoOnly", "es-orig", SourceAny, "vtt", true, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv := recordArgv(t, r)
			got, err := r.Transcript(t.Context(), tt.url, tt.lang, tt.src)
			if err != nil {
				t.Fatalf("Transcript: %v", err)
			}
			if string(got.Format) != tt.wantFormat || got.Automatic != tt.wantAuto || got.Lang != tt.lang || len(got.Data) == 0 {
				t.Fatalf("got format %q auto %v lang %q (%d bytes)", got.Format, got.Automatic, got.Lang, len(got.Data))
			}
			args := argv()
			for _, w := range tt.wantWrite {
				if !slices.Contains(args, w) {
					t.Errorf("argv lacks %s: %q", w, args)
				}
			}
			if tt.wantNoWrite != "" && slices.Contains(args, tt.wantNoWrite) {
				t.Errorf("argv has %s: %q", tt.wantNoWrite, args)
			}
			for _, pair := range [][2]string{{"--sub-langs", tt.lang}, {"--sub-format", "json3/vtt/srt"}, {"-o", "transcript"}} {
				if i := slices.Index(args, pair[0]); i < 0 || args[i+1] != pair[1] {
					t.Errorf("argv %s = ?, want %q: %q", pair[0], pair[1], args)
				}
			}
			for _, f := range []string{"--no-simulate", "--skip-download"} {
				if !slices.Contains(args, f) {
					t.Errorf("argv lacks %s", f)
				}
			}
			// The per-call scratch directory (-P) is gone once the call returns.
			dir := args[slices.Index(args, "-P")+1]
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("scratch dir %s still exists (stat err %v)", dir, err)
			}
		})
	}
}

func TestTranscriptErrors(t *testing.T) {
	r := newRunner(t)
	tests := []struct {
		name, url, lang string
		src             Source
		kind            ErrorKind
		wantMsg         string
	}{
		{"missing language", manualURL, "xx", SourceAny, KindNoSubtitles, `no subtitles in language "xx"; available manual: en, de; auto: en-orig, en, de, fr (call list_subtitles for names)`},
		{"missing manual", manualURL, "fr", SourceManual, KindNoSubtitles, `no manual subtitles in language "fr"; available manual: en, de;`},
		{"no tracks at all", "https://www.youtube.com/watch?v=vidNoSubs00", "en", SourceAny, KindNoSubtitles, "available manual: none; auto: none"},
		{"unsupported format", "https://www.youtube.com/watch?v=vidTtmlOnly", "en", SourceAny, KindParse, `only available as "ttml"`},
		{"invalid lang regex", manualURL, "en.*", SourceAny, KindInvalid, "is not a subtitle language code"},
		{"invalid lang list", manualURL, "en,all", SourceAny, KindInvalid, "is not a subtitle language code"},
		{"invalid source", manualURL, "en", Source("both"), KindInvalid, "source \"both\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.Transcript(t.Context(), tt.url, tt.lang, tt.src)
			e := wantKind(t, err, tt.kind)
			if !strings.Contains(e.Msg, tt.wantMsg) {
				t.Fatalf("message = %q, want containing %q", e.Msg, tt.wantMsg)
			}
		})
	}
}

func TestSummarizeAutomaticCaps(t *testing.T) {
	var tr Tracks
	for i := range 20 {
		tr.Automatic = append(tr.Automatic, Track{Code: "l" + strconv.Itoa(i)})
	}
	tr.Automatic = append(tr.Automatic, Track{Code: "en-orig"})
	got := summarizeAutomatic(tr)
	if !strings.HasPrefix(got, "en-orig, l0, l1") || !strings.HasSuffix(got, "(9 more)") {
		t.Fatalf("summary = %q", got)
	}
}

func TestValidateLang(t *testing.T) {
	for _, ok := range []string{"en", "pt-BR", "zh-Hans", "en-orig", "es-419", "fil", "sr-Latn-RS"} {
		if err := ValidateLang(ok); err != nil {
			t.Errorf("ValidateLang(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "e", "-en", "en,fr", "all", "en.*", ".*", "en$", "en orig", "en_US", "1en", "en-", "a-b-c-d-e"} {
		if err := ValidateLang(bad); err == nil {
			t.Errorf("ValidateLang(%q) accepted", bad)
		}
	}
}

// TestChildEnvSanitized proves server secrets never reach yt-dlp and that
// HOME and the caches point into the per-call scratch directory.
func TestChildEnvSanitized(t *testing.T) {
	t.Setenv("YTDLP_MCP_AUTH_TOKEN", "super-secret")
	t.Setenv("SOME_OTHER_SECRET", "also-secret")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	envFile := filepath.Join(t.TempDir(), "env")
	r := newRunner(t)
	r.extraEnv = []string{"FAKE_YTDLP_ENV_FILE=" + envFile}
	if _, _, err := r.Info(t.Context(), manualURL); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	for _, leaked := range []string{"super-secret", "also-secret", "YTDLP_MCP_AUTH_TOKEN"} {
		if strings.Contains(env, leaked) {
			t.Errorf("child environment contains %q", leaked)
		}
	}
	for _, want := range []string{"HTTPS_PROXY=http://proxy.example:3128", "PYTHONUTF8=1", "HOME=" + r.cfg.TempDir, "DENO_DIR=" + r.cfg.TempDir, "XDG_CACHE_HOME=" + r.cfg.TempDir} {
		if !strings.Contains(env, want) {
			t.Errorf("child environment lacks %q", want)
		}
	}
}

func TestOutputCapStopsProcess(t *testing.T) {
	r := newRunner(t, func(c *Config) { c.MaxOutputBytes = 64 << 10 })
	start := time.Now()
	_, _, err := r.Info(t.Context(), "https://www.youtube.com/watch?v=fakeFlood00")
	e := wantKind(t, err, KindOutputLimit)
	if !strings.Contains(e.Msg, "65536-byte limit") {
		t.Fatalf("message = %q", e.Msg)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("output cap took %s to stop the process", elapsed)
	}
}

func TestExitErrorMessage(t *testing.T) {
	r := newRunner(t)
	_, _, err := r.Info(t.Context(), "https://www.youtube.com/watch?v=fakeError00")
	e := wantKind(t, err, KindFailed)
	if e.Msg != "yt-dlp failed: [youtube] fakeError00: Video unavailable. This video is private" {
		t.Fatalf("message = %q", e.Msg)
	}
}

func TestCanceled(t *testing.T) {
	r := newRunner(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(300*time.Millisecond, cancel)
	_, _, err := r.Info(ctx, "https://www.youtube.com/watch?v=fakeSlow000")
	_ = wantKind(t, err, KindCanceled)
}

func TestBusy(t *testing.T) {
	r := newRunner(t, func(c *Config) { c.MaxConcurrency = 1; c.Timeout = 200 * time.Millisecond })
	r.sem <- struct{}{} // occupy the only slot
	defer func() { <-r.sem }()
	_, _, err := r.Info(t.Context(), manualURL)
	e := wantKind(t, err, KindBusy)
	if !strings.Contains(e.Msg, "all 1 yt-dlp slots") {
		t.Fatalf("message = %q", e.Msg)
	}
}

// TestTimeoutCoversSlotWait proves YTDLP_MCP_TIMEOUT bounds a whole call, not
// each phase: a call that queues for a slot and then runs a hanging yt-dlp
// still returns at the one deadline (not wait + timeout), and says how much
// of it went to waiting.
func TestTimeoutCoversSlotWait(t *testing.T) {
	const timeout, held = 1500 * time.Millisecond, 1100 * time.Millisecond
	r := newRunner(t, func(c *Config) { c.MaxConcurrency = 1; c.Timeout = timeout })
	r.sem <- struct{}{}                      // another call holds the only slot...
	time.AfterFunc(held, func() { <-r.sem }) // ...and finishes after held

	start := time.Now()
	_, _, err := r.Info(t.Context(), "https://www.youtube.com/watch?v=fakeSlow000")
	elapsed := time.Since(start)

	e := wantKind(t, err, KindTimeout)
	if want := "yt-dlp timed out after 1.5s (YTDLP_MCP_TIMEOUT), 1s of it waiting for a free yt-dlp slot (YTDLP_MCP_MAX_CONCURRENCY)"; !strings.HasPrefix(e.Msg, want) {
		t.Fatalf("message = %q, want prefix %q", e.Msg, want)
	}
	// The old behavior (wait, then a fresh timeout) would take held+timeout.
	if elapsed < timeout || elapsed > timeout+500*time.Millisecond {
		t.Fatalf("call took %s; the deadline is %s for slot wait and run together", elapsed, timeout)
	}
}

func TestStartFailure(t *testing.T) {
	r := newRunner(t, func(c *Config) { c.Path = filepath.Join(t.TempDir(), "no-such-yt-dlp") })
	_, _, err := r.Info(t.Context(), manualURL)
	e := wantKind(t, err, KindStart)
	if !strings.Contains(e.Msg, "could not start yt-dlp") {
		t.Fatalf("message = %q", e.Msg)
	}
}

func TestVersion(t *testing.T) {
	v, err := newRunner(t).Version(t.Context())
	if err != nil || v != "2026.10.21-fake" {
		t.Fatalf("Version = %q, %v", v, err)
	}
}

func TestNewValidates(t *testing.T) {
	base := Config{Path: "yt-dlp", Timeout: time.Second, MaxOutputBytes: 1, MaxConcurrency: 1}
	for name, mutate := range map[string]func(*Config){
		"path":        func(c *Config) { c.Path = "" },
		"timeout":     func(c *Config) { c.Timeout = 0 },
		"output":      func(c *Config) { c.MaxOutputBytes = 0 },
		"concurrency": func(c *Config) { c.MaxConcurrency = 0 },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted %+v", name, cfg)
		}
	}
}

func TestBuffers(t *testing.T) {
	var fired int
	c := &capBuffer{limit: 5, onOverflow: func() { fired++ }}
	_, _ = c.Write([]byte("abc"))
	_, _ = c.Write([]byte("defg"))
	_, _ = c.Write([]byte("hij"))
	if c.buf.String() != "abcde" || !c.overflowed() || fired != 1 {
		t.Fatalf("capBuffer = %q overflowed=%v fired=%d", c.buf.String(), c.overflowed(), fired)
	}

	tb := &tailBuffer{limit: 4}
	_, _ = tb.Write([]byte("abc"))
	_, _ = tb.Write([]byte("defg"))
	if tb.String() != "defg" {
		t.Fatalf("tailBuffer = %q", tb.String())
	}
}

func TestStderrSummary(t *testing.T) {
	tests := map[string]string{
		"WARNING: a\nERROR: first\nERROR: second\n": "second",
		"WARNING: only a warning\n":                 "WARNING: only a warning",
		"":                                          "exited with status 2 and no error message",
		"ERROR: " + strings.Repeat("x", 700):        strings.Repeat("x", 600) + "…",
	}
	for in, want := range tests {
		if got := stderrSummary(in, 2); got != want {
			t.Errorf("stderrSummary(%q) = %q, want %q", in, got, want)
		}
	}
}
