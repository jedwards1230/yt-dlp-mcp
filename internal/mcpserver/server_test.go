package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jedwards1230/yt-dlp-mcp/internal/urlguard"
	"github.com/jedwards1230/yt-dlp-mcp/internal/ytdlp"
)

// countingBackend wraps the real runner and counts calls, so tests can prove
// a rejected URL never reaches yt-dlp and that pagination hits the cache.
type countingBackend struct {
	Backend
	calls, transcripts atomic.Int32
}

func (c *countingBackend) Search(ctx context.Context, q string, n int) ([]ytdlp.SearchEntry, error) {
	c.calls.Add(1)
	return c.Backend.Search(ctx, q, n)
}

func (c *countingBackend) Info(ctx context.Context, target string) (ytdlp.VideoInfo, ytdlp.Tracks, error) {
	c.calls.Add(1)
	return c.Backend.Info(ctx, target)
}

func (c *countingBackend) Subtitles(ctx context.Context, target string) (ytdlp.VideoInfo, ytdlp.Tracks, error) {
	c.calls.Add(1)
	return c.Backend.Subtitles(ctx, target)
}

func (c *countingBackend) Transcript(ctx context.Context, target, lang string, src ytdlp.Source) (ytdlp.SubtitleFile, error) {
	c.calls.Add(1)
	c.transcripts.Add(1)
	return c.Backend.Transcript(ctx, target, lang, src)
}

type fixture struct {
	backend *countingBackend
	session *mcp.ClientSession
	url     string
}

// newFixture runs the real HTTP handler over httptest, backed by the real
// ytdlp.Runner driving testdata's fake yt-dlp, and connects an SDK client.
func newFixture(t *testing.T, token string, hosts ...string) *fixture {
	t.Helper()
	fake, err := filepath.Abs(filepath.Join("..", "ytdlp", "testdata", "fake-yt-dlp"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := ytdlp.New(ytdlp.Config{
		Path: fake, Timeout: 10 * time.Second, MaxOutputBytes: 1 << 20, MaxConcurrency: 2, TempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) == 0 {
		hosts = urlguard.DefaultHosts
	}
	allow, err := urlguard.NewAllowlist(hosts)
	if err != nil {
		t.Fatal(err)
	}
	backend := &countingBackend{Backend: runner}
	srv := New(Options{Backend: backend, Allowlist: allow, CacheTTL: time.Minute})
	ts := httptest.NewServer(NewHandler(srv, token, nil))
	t.Cleanup(ts.Close)

	f := &fixture{backend: backend, url: ts.URL}
	httpClient := ts.Client()
	if token != "" {
		httpClient = &http.Client{Transport: headerTransport{base: http.DefaultTransport, header: "Bearer " + token}}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	f.session, err = client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: ts.URL + MCPPath, HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = f.session.Close() })
	return f
}

type headerTransport struct {
	base   http.RoundTripper
	header string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", h.header)
	return h.base.RoundTrip(r)
}

// call invokes a tool that must succeed and decodes its structured output.
func call[T any](t *testing.T, f *fixture, name string, args map[string]any) T {
	t.Helper()
	res, err := f.session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, resultText(t, res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding %s output %s: %v", name, raw, err)
	}
	return out
}

// callErr invokes a tool that must fail and returns the error text.
func callErr(t *testing.T, f *fixture, name string, args map[string]any) string {
	t.Helper()
	res, err := f.session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("%s succeeded, want a tool error: %s", name, resultText(t, res))
	}
	return resultText(t, res)
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestToolList(t *testing.T) {
	f := newFixture(t, "")
	var names []string
	byName := map[string]*mcp.Tool{}
	for tool, err := range f.session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
		byName[tool.Name] = tool
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"get_info", "get_transcript", "list_subtitles", "search"}) {
		t.Fatalf("tools = %v", names)
	}
	for name, tool := range byName {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Errorf("%s is not annotated read-only/non-destructive: %+v", name, tool.Annotations)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s has no output schema", name)
		}
	}
	schema, err := json.Marshal(byName["get_transcript"].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"enum":["any","manual","auto"]`, `"default":500`, `"default":"en"`, `"required":["url"]`} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("get_transcript input schema lacks %s: %s", want, schema)
		}
	}
}

func TestSearchTool(t *testing.T) {
	f := newFixture(t, "")
	out := call[searchOutput](t, f, "search", map[string]any{"query": "bookshelf", "limit": 50})
	if out.Limit != 25 || out.Count != 3 || len(out.Results) != 3 {
		t.Fatalf("search = %+v", out)
	}
	first := out.Results[0]
	if first.ID != "vidManual01" || first.Duration != "12:34" || *first.DurationSeconds != 754 || *first.ViewCount != 12345 || first.Channel != "Example Workshop" {
		t.Fatalf("first result = %+v", first)
	}
	if out.Results[1].Duration != "5:13" || out.Results[1].ViewCount != nil {
		t.Fatalf("second result = %+v", out.Results[1])
	}

	if msg := callErr(t, f, "search", map[string]any{"query": "  "}); !strings.Contains(msg, "query is required") {
		t.Fatalf("empty query error = %q", msg)
	}
	if msg := callErr(t, f, "search", map[string]any{"query": "a\nb"}); !strings.Contains(msg, "control characters") {
		t.Fatalf("newline query error = %q", msg)
	}
}

func TestSearchNeedsYouTubeAllowed(t *testing.T) {
	f := newFixture(t, "", "videos.example.org")
	if msg := callErr(t, f, "search", map[string]any{"query": "x"}); !strings.Contains(msg, "www.youtube.com is not on this server's allowed host list") {
		t.Fatalf("error = %q", msg)
	}
	if n := f.backend.calls.Load(); n != 0 {
		t.Fatalf("backend called %d times", n)
	}
}

func TestGetInfoTool(t *testing.T) {
	f := newFixture(t, "")
	out := call[getInfoOutput](t, f, "get_info", map[string]any{"url": "vidManual01"})
	if out.ID != "vidManual01" || out.UploadDate != "2024-03-15" || out.Duration != "12:34" || out.ChannelHandle != "@exampleworkshop" ||
		*out.LikeCount != 678 || out.CommentCount != nil || out.DescriptionTruncated || out.Tags != nil {
		t.Fatalf("get_info = %+v", out)
	}
	if len(out.Chapters) != 2 || out.Chapters[1] != (chapterOut{Start: "01:35", StartSeconds: 95, EndSeconds: 754, Title: "Assembly"}) {
		t.Fatalf("chapters = %+v", out.Chapters)
	}
	if !out.HasSubtitles || !slices.Equal(out.SubtitleLanguages, []string{"en", "de"}) || !out.HasAutomaticCaptions ||
		out.AutomaticOriginal != "en-orig" || out.AutomaticCount != 4 {
		t.Fatalf("subtitle summary = %+v", out)
	}

	withTags := call[getInfoOutput](t, f, "get_info", map[string]any{"url": "https://youtu.be/vidManual01", "include_tags": true})
	if !slices.Equal(withTags.Tags, []string{"woodworking", "diy"}) {
		t.Fatalf("tags = %v", withTags.Tags)
	}

	none := call[getInfoOutput](t, f, "get_info", map[string]any{"url": "vidNoSubs00"})
	if none.HasSubtitles || none.HasAutomaticCaptions || none.AutomaticCount != 0 {
		t.Fatalf("no-subs video = %+v", none)
	}
}

func TestGetInfoErrors(t *testing.T) {
	f := newFixture(t, "")
	msg := callErr(t, f, "get_info", map[string]any{"url": "fakeError00"})
	if msg != "yt-dlp failed: [youtube] fakeError00: Video unavailable. This video is private" {
		t.Fatalf("error = %q", msg)
	}
	if msg := callErr(t, f, "get_info", map[string]any{}); !strings.Contains(msg, "url") {
		t.Fatalf("missing url error = %q", msg)
	}
}

// TestSSRFRejectedBeforeExec drives rejected URLs through every URL-taking
// tool and checks yt-dlp was never started.
func TestSSRFRejectedBeforeExec(t *testing.T) {
	f := newFixture(t, "")
	cases := map[string]string{
		"http://169.254.169.254/latest/meta-data/":            "IP address",
		"http://2130706433/":                                  "IP address",
		"http://[::1]/":                                       "not a plain hostname",
		"https://example.com/watch?v=dQw4w9WgXcQ":             `host "example.com" is not allowed; allowed hosts:`,
		"HTTPS://EXAMPLE.COM/watch?v=dQw4w9WgXcQ":             `host "example.com" is not allowed`,
		"https://www.youtube.com.evil.example/watch?v=x":      "is not allowed",
		"https://www.youtube.com./watch?v=dQw4w9WgXcQ":        "ends with a dot",
		"https://www.yоutube.com/watch?v=dQw4w9WgXcQ":         "not a plain hostname", // Cyrillic о
		"https://%77ww.youtube.com/watch?v=dQw4w9WgXcQ":       "not a plain hostname",
		"file:///etc/passwd":                                  `scheme "file" is not allowed`,
		"https://www.youtube.com@10.0.0.1/":                   "username or password",
		"https://www.youtube.com:8080/watch?v=x":              "port 8080 is not allowed",
		"ytsearch1:anything":                                  "not an http(s) URL",
		"--exec=id":                                           "not an http(s) URL",
		"https://www.youtube.com/playlist?list=PL12":          "could not find a video id",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ\n--exec": "whitespace",
	}
	for _, tool := range []string{"get_info", "list_subtitles", "get_transcript"} {
		for in, want := range cases {
			if msg := callErr(t, f, tool, map[string]any{"url": in}); !strings.Contains(msg, want) {
				t.Errorf("%s(%q) error = %q, want containing %q", tool, in, msg, want)
			}
		}
	}
	if n := f.backend.calls.Load(); n != 0 {
		t.Fatalf("yt-dlp backend was called %d times for rejected URLs", n)
	}
}

func TestListSubtitlesTool(t *testing.T) {
	f := newFixture(t, "")
	out := call[listSubtitlesOutput](t, f, "list_subtitles", map[string]any{"url": "vidManual01"})
	if !slices.Equal(out.Manual, []trackOut{{Code: "en", Name: "English"}, {Code: "de", Name: "German"}}) {
		t.Fatalf("manual = %+v", out.Manual)
	}
	if !slices.Equal(out.Automatic, []trackOut{{Code: "en-orig", Name: "English (Original)", Original: true}}) ||
		out.AutomaticTotal != 4 || out.AutomaticOmitted != 3 {
		t.Fatalf("automatic = %+v total %d omitted %d", out.Automatic, out.AutomaticTotal, out.AutomaticOmitted)
	}
	if !strings.Contains(out.Hint, `lang="en" returns the manual track`) || !strings.Contains(out.Hint, `lang="en-orig" source="auto"`) {
		t.Fatalf("hint = %q", out.Hint)
	}

	all := call[listSubtitlesOutput](t, f, "list_subtitles", map[string]any{"url": "vidManual01", "include_translations": true})
	if len(all.Automatic) != 4 || all.AutomaticOmitted != 0 {
		t.Fatalf("include_translations = %+v", all)
	}

	none := call[listSubtitlesOutput](t, f, "list_subtitles", map[string]any{"url": "vidNoSubs00"})
	if none.Manual == nil || none.Automatic == nil || len(none.Manual)+len(none.Automatic) != 0 {
		t.Fatalf("no-subs lists must be empty, not null: %+v", none)
	}
}

func TestGetTranscriptTool(t *testing.T) {
	f := newFixture(t, "")
	out := call[getTranscriptOutput](t, f, "get_transcript", map[string]any{"url": "vidManual01"})
	wantText := "Welcome to the workshop.\nToday we build a bookshelf.\nMeasure twice.\nCut once.\nThanks for watching."
	if out.Source != "manual" || out.Format != "json3" || out.Lang != "en" || out.TotalSegments != 5 || out.HasMore || out.NextOffset != 5 || out.Text != wantText {
		t.Fatalf("get_transcript = %+v", out)
	}

	ts := call[getTranscriptOutput](t, f, "get_transcript", map[string]any{"url": "vidManual01", "timestamps": true, "offset": 3, "limit": 5})
	if ts.Text != "[00:08] Cut once.\n[01:02:05] Thanks for watching." || ts.Returned != 2 || ts.Offset != 3 {
		t.Fatalf("timestamps page = %+v", ts)
	}

	page := call[getTranscriptOutput](t, f, "get_transcript", map[string]any{"url": "vidManual01", "limit": 2})
	if page.Returned != 2 || !page.HasMore || page.NextOffset != 2 || page.Text != "Welcome to the workshop.\nToday we build a bookshelf." {
		t.Fatalf("first page = %+v", page)
	}
	if n := f.backend.transcripts.Load(); n != 1 {
		t.Fatalf("pagination fetched the transcript %d times, want 1 (cache)", n)
	}

	if msg := callErr(t, f, "get_transcript", map[string]any{"url": "vidManual01", "offset": 5}); !strings.Contains(msg, "past the end of the transcript (5 segments") {
		t.Fatalf("offset past end error = %q", msg)
	}
}

func TestGetTranscriptAutomatic(t *testing.T) {
	f := newFixture(t, "")
	out := call[getTranscriptOutput](t, f, "get_transcript", map[string]any{"url": "vidAutoOnly", "lang": "es-orig"})
	if out.Source != "auto" || out.Format != "vtt" || out.AutoTranslated || out.Text != "hola a todos\nhoy hacemos una estantería" {
		t.Fatalf("auto transcript = %+v", out)
	}
	tr := call[getTranscriptOutput](t, f, "get_transcript", map[string]any{"url": "vidAutoOnly", "lang": "en"})
	if tr.Source != "auto" || !tr.AutoTranslated {
		t.Fatalf("translated transcript = %+v", tr)
	}
	forced := call[getTranscriptOutput](t, f, "get_transcript", map[string]any{"url": "vidManual01", "source": "auto"})
	if forced.Source != "auto" || forced.AutoTranslated || forced.Text != "welcome to the workshop\ntoday we build" {
		t.Fatalf("forced auto = %+v", forced)
	}
}

func TestGetTranscriptErrors(t *testing.T) {
	f := newFixture(t, "")
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing language", map[string]any{"url": "vidManual01", "lang": "xx"}, `no subtitles in language "xx"; available manual: en, de; auto: en-orig, en, de, fr`},
		{"no subtitles", map[string]any{"url": "vidNoSubs00"}, "available manual: none; auto: none"},
		{"regex language", map[string]any{"url": "vidManual01", "lang": ".*"}, "is not a subtitle language code"},
		{"all languages", map[string]any{"url": "vidManual01", "lang": "all"}, "is not a subtitle language code"},
		{"bad source", map[string]any{"url": "vidManual01", "source": "both"}, "source"},
		{"negative offset", map[string]any{"url": "vidManual01", "offset": -1}, "negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if msg := callErr(t, f, "get_transcript", tt.args); !strings.Contains(msg, tt.want) {
				t.Fatalf("error = %q, want containing %q", msg, tt.want)
			}
		})
	}
}

func TestPreferredManual(t *testing.T) {
	tracks := []trackOut{{Code: "af"}, {Code: "de"}, {Code: "en-GB"}, {Code: "es"}}
	for _, tt := range []struct{ spoken, want string }{
		{"es", "es"}, {"en", "en-GB"}, {"", "en-GB"}, {"ja", "en-GB"},
	} {
		if got := preferredManual(tracks, tt.spoken); got != tt.want {
			t.Errorf("preferredManual(spoken %q) = %q, want %q", tt.spoken, got, tt.want)
		}
	}
	if got := preferredManual([]trackOut{{Code: "fr"}}, "ja"); got != "fr" {
		t.Errorf("fallback = %q", got)
	}
	if got := preferredManual(nil, "en"); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestClamp(t *testing.T) {
	for _, tt := range []struct{ v, def, lo, hi, want int }{
		{0, 10, 1, 25, 10}, {-3, 10, 1, 25, 1}, {99, 10, 1, 25, 25}, {7, 10, 1, 25, 7},
	} {
		if got := clamp(tt.v, tt.def, tt.lo, tt.hi); got != tt.want {
			t.Errorf("clamp(%d) = %d, want %d", tt.v, got, tt.want)
		}
	}
}

func TestDescriptionTruncation(t *testing.T) {
	b := &stubBackend{info: ytdlp.VideoInfo{ID: "x", Description: strings.Repeat("é", descriptionLimit+10)}}
	allow, _ := urlguard.NewAllowlist(urlguard.DefaultHosts)
	s := &server{backend: b, allow: allow, cache: newTranscriptCache(0, 1, 0, time.Now), log: nil}
	s.log = discardLogger()
	_, out, err := s.getInfo(t.Context(), nil, getInfoInput{URL: "dQw4w9WgXcQ"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.DescriptionTruncated || out.DescriptionLength != descriptionLimit+10 || len([]rune(out.Description)) != descriptionLimit {
		t.Fatalf("truncation: truncated=%v length=%d runes=%d", out.DescriptionTruncated, out.DescriptionLength, len([]rune(out.Description)))
	}
	_, full, err := s.getInfo(t.Context(), nil, getInfoInput{URL: "dQw4w9WgXcQ", FullDescription: true})
	if err != nil || full.DescriptionTruncated || len([]rune(full.Description)) != descriptionLimit+10 {
		t.Fatalf("full description: %+v %v", full.DescriptionTruncated, err)
	}
}
