//go:build live

package live

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Configuration, all optional:
//
//	YTDLP_MCP_LIVE_IMAGE  image to start (default yt-dlp-mcp:smoke); build it
//	                      first with `docker build -t yt-dlp-mcp:smoke .`
//	YTDLP_MCP_LIVE_URL    MCP endpoint of an already-running server, e.g.
//	                      http://localhost:8080/mcp; no container is started
//	YTDLP_MCP_LIVE_TOKEN  that server's bearer token, if it has one (a started
//	                      container always gets a random token)
//	YTDLP_MCP_LIVE_VIDEO  public video id with manual English subtitles and
//	                      automatic captions
//	YTDLP_MCP_LIVE_QUERY  search query
//
// Run: go test -tags live -count=1 -v ./test/live/
const (
	defaultImage = "yt-dlp-mcp:smoke"
	// A long-lived public talk with uploaded subtitles in dozens of
	// languages (English included) and automatic captions.
	defaultVideo = "iG9CE55wbtY"
	defaultQuery = "ted talk creativity schools"
	callTimeout  = 2 * time.Minute
	pageLimit    = 40
)

var timestampRe = regexp.MustCompile(`^\[(\d{2}:)?\d{2}:\d{2}\] `)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// container is a yt-dlp-mcp container started for the test.
type container struct {
	id string
}

func docker(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// startContainer runs image the way a hardened deployment does: read-only
// root filesystem, a tmpfs /tmp, no capabilities, no privilege escalation,
// published on a random loopback port. It sets a random bearer token (passed
// through the environment, not argv) and debug logging, so the test can read
// every yt-dlp argv from the container log.
func startContainer(t *testing.T, image, token string) (*container, string) {
	t.Helper()
	ctx := t.Context()
	id, err := docker(ctx, []string{"YTDLP_MCP_AUTH_TOKEN=" + token},
		"run", "-d",
		"--read-only", "--tmpfs", "/tmp:rw,size=256m",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-p", "127.0.0.1::8080",
		"-e", "YTDLP_MCP_AUTH_TOKEN", "-e", "YTDLP_MCP_LOG_LEVEL=debug",
		image)
	if err != nil {
		t.Fatalf("docker run %s: %v\n%s\n(build the image first: docker build -t %s .)", image, err, id, image)
	}
	c := &container{id: id}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("container log:\n%s", c.logs(t))
		}
		_, _ = docker(context.Background(), nil, "rm", "-f", c.id)
	})
	hostPort, err := docker(ctx, nil, "port", id, "8080/tcp")
	if err != nil {
		t.Fatalf("docker port: %v: %s", err, hostPort)
	}
	hostPort = strings.SplitN(hostPort, "\n", 2)[0]
	base := "http://" + hostPort

	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server in %s never became healthy: %v\n%s", id[:12], err, c.logs(t))
		}
		time.Sleep(250 * time.Millisecond)
	}
	return c, base + "/mcp"
}

func (c *container) exec(t *testing.T, args ...string) string {
	t.Helper()
	out, err := docker(t.Context(), nil, append([]string{"exec", c.id}, args...)...)
	if err != nil {
		t.Fatalf("docker exec %v: %v: %s", args, err, out)
	}
	return out
}

func (c *container) logs(t *testing.T) string {
	out, err := docker(context.Background(), nil, "logs", c.id)
	if err != nil {
		t.Logf("docker logs: %v", err)
	}
	return out
}

// logRecord is one JSON log line from the server.
type logRecord struct {
	Msg          string   `json:"msg"`
	Op           string   `json:"op"`
	Args         []string `json:"args"`
	YTDLPVersion string   `json:"yt_dlp_version"`
}

func (c *container) records(t *testing.T) []logRecord {
	t.Helper()
	var recs []logRecord
	sc := bufio.NewScanner(strings.NewReader(c.logs(t)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r logRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Msg != "" {
			recs = append(recs, r)
		}
	}
	return recs
}

// ytdlpRuns returns the argv of every yt-dlp process the server started.
func (c *container) ytdlpRuns(t *testing.T) [][]string {
	t.Helper()
	var runs [][]string
	for _, r := range c.records(t) {
		if r.Msg == "running yt-dlp" {
			runs = append(runs, r.Args)
		}
	}
	return runs
}

// result is one measured tool call, for the summary table.
type result struct {
	tool, note string
	latency    time.Duration
	bytes      int
}

type session struct {
	cs      *mcp.ClientSession
	results *[]result
}

func (s session) callRaw(t *testing.T, name, note string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), callTimeout)
	defer cancel()
	start := time.Now()
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	size := len(text(res))
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		size = len(raw)
	}
	*s.results = append(*s.results, result{tool: name, note: note, latency: elapsed, bytes: size})
	t.Logf("%s (%s) took %s, %d bytes", name, note, elapsed.Round(time.Millisecond), size)
	return res
}

// call runs a tool that must succeed and decodes its structured output.
func (s session) call(t *testing.T, name, note string, args map[string]any, out any) {
	t.Helper()
	res := s.callRaw(t, name, note, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decoding %s output: %v\n%s", name, err, raw)
	}
}

// callErr runs a tool that must fail and returns its error text.
func (s session) callErr(t *testing.T, name, note string, args map[string]any) string {
	t.Helper()
	res := s.callRaw(t, name, note, args)
	if !res.IsError {
		t.Fatalf("%s %v succeeded; want a tool error", name, args)
	}
	return text(res)
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// containsSeq reports whether seq appears in args as consecutive elements.
func containsSeq(args, seq []string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

func excerpt(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// pinnedYTDLP reads the yt-dlp version pinned in the repository's
// requirements.txt.
func pinnedYTDLP(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "requirements.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "yt-dlp=="); ok {
			return v
		}
	}
	t.Fatal("requirements.txt pins no yt-dlp version")
	return ""
}

type transcriptPage struct {
	VideoID       string `json:"video_id"`
	Lang          string `json:"lang"`
	Source        string `json:"source"`
	Format        string `json:"format"`
	Offset        int    `json:"offset"`
	Returned      int    `json:"returned"`
	TotalSegments int    `json:"total_segments"`
	NextOffset    int    `json:"next_offset"`
	HasMore       bool   `json:"has_more"`
	Text          string `json:"text"`
}

func (p transcriptPage) lines() []string { return strings.Split(p.Text, "\n") }

// checkTimestamped asserts ts is plain rendered with "[mm:ss] " prefixes.
func checkTimestamped(t *testing.T, plain, ts transcriptPage) {
	t.Helper()
	pl, tl := plain.lines(), ts.lines()
	if len(pl) != len(tl) || ts.Returned != plain.Returned || ts.TotalSegments != plain.TotalSegments {
		t.Fatalf("timestamped page has %d lines (%d segments of %d), plain has %d (%d of %d)",
			len(tl), ts.Returned, ts.TotalSegments, len(pl), plain.Returned, plain.TotalSegments)
	}
	for i := range tl {
		prefix := timestampRe.FindString(tl[i])
		if prefix == "" || strings.TrimPrefix(tl[i], prefix) != pl[i] {
			t.Fatalf("line %d: timestamped %q does not match plain %q", i, tl[i], pl[i])
		}
	}
}

func TestLiveSmoke(t *testing.T) {
	video := env("YTDLP_MCP_LIVE_VIDEO", defaultVideo)
	query := env("YTDLP_MCP_LIVE_QUERY", defaultQuery)

	var ctr *container
	endpoint, token := os.Getenv("YTDLP_MCP_LIVE_URL"), os.Getenv("YTDLP_MCP_LIVE_TOKEN")
	if endpoint == "" {
		image := env("YTDLP_MCP_LIVE_IMAGE", defaultImage)
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			t.Fatal(err)
		}
		token = hex.EncodeToString(buf)
		ctr, endpoint = startContainer(t, image, token)
		t.Logf("started %s as %s, MCP endpoint %s", image, ctr.id[:12], endpoint)
	} else {
		t.Logf("using the running server at %s", endpoint)
	}

	var results []result
	t.Cleanup(func() {
		var b strings.Builder
		fmt.Fprintf(&b, "video %s, query %q\n%-15s %-58s %10s %8s\n", video, query, "tool", "call", "latency", "bytes")
		for _, r := range results {
			fmt.Fprintf(&b, "%-15s %-58s %10s %8d\n", r.tool, r.note, r.latency.Round(time.Millisecond), r.bytes)
		}
		t.Logf("summary:\n%s", b.String())
	})

	t.Run("image", func(t *testing.T) {
		if ctr == nil {
			t.Skip("YTDLP_MCP_LIVE_URL is set; no container to inspect")
		}
		if uid := ctr.exec(t, "id", "-u"); uid != "65532" {
			t.Fatalf("server runs as uid %s, want 65532", uid)
		}
		want := pinnedYTDLP(t)
		if got := ctr.exec(t, "yt-dlp", "--version"); got != want {
			t.Fatalf("yt-dlp --version = %q, requirements.txt pins %q", got, want)
		}
		for _, r := range ctr.records(t) {
			if r.Msg == "yt-dlp-mcp listening" && r.YTDLPVersion != want {
				t.Fatalf("server reports yt-dlp %q, want %q", r.YTDLPVersion, want)
			}
		}
		if out := ctr.exec(t, "deno", "--version"); !strings.HasPrefix(out, "deno ") {
			t.Fatalf("deno --version = %q", out)
		}
		t.Logf("uid 65532, yt-dlp %s, %s", want, strings.SplitN(ctr.exec(t, "deno", "--version"), "\n", 2)[0])
	})

	t.Run("healthz", func(t *testing.T) {
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/healthz"
		resp, err := http.Get(u.String()) // never authenticated
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("GET %s = %d %q", u, resp.StatusCode, body)
		}
	})

	t.Run("auth_required", func(t *testing.T) {
		if token == "" {
			t.Skip("the server has no bearer token")
		}
		resp, err := http.Post(endpoint, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated POST /mcp = %d, want 401", resp.StatusCode)
		}
	})

	start := time.Now()
	client := mcp.NewClient(&mcp.Implementation{Name: "yt-dlp-mcp-live-smoke", Version: "0.0.0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearer{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("MCP initialize against %s: %v", endpoint, err)
	}
	defer func() { _ = cs.Close() }()
	init := cs.InitializeResult()
	t.Logf("initialize: server %s %s, protocol %s, took %s", init.ServerInfo.Name, init.ServerInfo.Version, init.ProtocolVersion, time.Since(start).Round(time.Millisecond))
	if init.ServerInfo.Name != "yt-dlp-mcp" {
		t.Fatalf("server name = %q", init.ServerInfo.Name)
	}
	s := session{cs: cs, results: &results}

	t.Run("tools_list", func(t *testing.T) {
		var names []string
		for tool, err := range cs.Tools(t.Context(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, tool.Name)
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Errorf("%s is not annotated read-only", tool.Name)
			}
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"get_info", "get_transcript", "list_subtitles", "search"}) {
			t.Fatalf("tools = %v, want exactly get_info, get_transcript, list_subtitles, search", names)
		}
	})

	t.Run("search", func(t *testing.T) {
		var out struct {
			Limit   int `json:"limit"`
			Count   int `json:"count"`
			Results []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				URL      string `json:"url"`
				Duration string `json:"duration"`
			} `json:"results"`
		}
		s.call(t, "search", "limit 3", map[string]any{"query": query, "limit": 3}, &out)
		if out.Limit != 3 || out.Count < 1 || out.Count > 3 || len(out.Results) != out.Count {
			t.Fatalf("search = %+v", out)
		}
		for _, r := range out.Results {
			if len(r.ID) != 11 || r.Title == "" || r.URL == "" {
				t.Fatalf("incomplete result %+v", r)
			}
		}
		t.Logf("search: %d results; first %q (%s) %s", out.Count, out.Results[0].Title, out.Results[0].Duration, out.Results[0].URL)
	})

	t.Run("get_info", func(t *testing.T) {
		var out struct {
			ID                   string   `json:"id"`
			Title                string   `json:"title"`
			URL                  string   `json:"url"`
			Channel              string   `json:"channel"`
			DurationSeconds      int64    `json:"duration_seconds"`
			Duration             string   `json:"duration"`
			UploadDate           string   `json:"upload_date"`
			HasSubtitles         bool     `json:"has_subtitles"`
			SubtitleLanguages    []string `json:"subtitle_languages"`
			HasAutomaticCaptions bool     `json:"has_automatic_captions"`
			AutomaticOriginal    string   `json:"automatic_captions_original"`
			DescriptionLength    int      `json:"description_length"`
			DescriptionTrunc     bool     `json:"description_truncated"`
		}
		s.call(t, "get_info", "bare id", map[string]any{"url": video}, &out)
		if (len(video) == 11 && out.ID != video) || out.Title == "" || out.DurationSeconds <= 0 || out.Duration == "" {
			t.Fatalf("get_info = %+v", out)
		}
		if !out.HasSubtitles || !slices.Contains(out.SubtitleLanguages, "en") || !out.HasAutomaticCaptions {
			t.Fatalf("video %s needs manual en subtitles and automatic captions: %+v", video, out)
		}
		if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(out.UploadDate) {
			t.Fatalf("upload_date %q is not YYYY-MM-DD", out.UploadDate)
		}
		t.Logf("get_info: %s %q by %q, %s (%ds), uploaded %s, %d manual languages, auto original %q, description %d chars (truncated %v)",
			out.ID, out.Title, out.Channel, out.Duration, out.DurationSeconds, out.UploadDate, len(out.SubtitleLanguages), out.AutomaticOriginal, out.DescriptionLength, out.DescriptionTrunc)

		var byURL struct {
			ID string `json:"id"`
		}
		s.call(t, "get_info", "youtu.be URL", map[string]any{"url": "https://youtu.be/" + out.ID + "?si=live"}, &byURL)
		if byURL.ID != out.ID {
			t.Fatalf("youtu.be URL resolved to %q, want %q", byURL.ID, out.ID)
		}
	})

	type track struct {
		Code     string `json:"code"`
		Name     string `json:"name"`
		Original bool   `json:"original"`
	}
	var autoOriginal string
	t.Run("list_subtitles", func(t *testing.T) {
		var out struct {
			VideoID          string  `json:"video_id"`
			Manual           []track `json:"manual"`
			Automatic        []track `json:"automatic"`
			AutomaticTotal   int     `json:"automatic_total"`
			AutomaticOmitted int     `json:"automatic_omitted"`
			Hint             string  `json:"hint"`
		}
		s.call(t, "list_subtitles", "default", map[string]any{"url": video}, &out)
		if !slices.ContainsFunc(out.Manual, func(m track) bool { return m.Code == "en" && m.Name != "" }) {
			t.Fatalf("no named manual en track: %+v", out.Manual)
		}
		if slices.ContainsFunc(out.Manual, func(m track) bool { return m.Original }) {
			t.Fatalf("a manual track is flagged as the original automatic track: %+v", out.Manual)
		}
		// Compact by default: only the original-language automatic track.
		if len(out.Automatic) != 1 || !out.Automatic[0].Original || !strings.HasSuffix(out.Automatic[0].Code, "-orig") {
			t.Fatalf("automatic = %+v, want just the original-language track", out.Automatic)
		}
		if out.AutomaticTotal <= 1 || out.AutomaticOmitted != out.AutomaticTotal-1 {
			t.Fatalf("automatic_total %d, automatic_omitted %d", out.AutomaticTotal, out.AutomaticOmitted)
		}
		autoOriginal = out.Automatic[0].Code
		t.Logf("list_subtitles: %d manual, automatic original %q, %d automatic total (%d omitted); hint: %s",
			len(out.Manual), autoOriginal, out.AutomaticTotal, out.AutomaticOmitted, out.Hint)

		var all struct {
			Automatic        []track `json:"automatic"`
			AutomaticTotal   int     `json:"automatic_total"`
			AutomaticOmitted int     `json:"automatic_omitted"`
		}
		s.call(t, "list_subtitles", "include_translations", map[string]any{"url": video, "include_translations": true}, &all)
		if len(all.Automatic) != all.AutomaticTotal || all.AutomaticOmitted != 0 {
			t.Fatalf("include_translations: %d listed of %d, %d omitted", len(all.Automatic), all.AutomaticTotal, all.AutomaticOmitted)
		}
	})

	t.Run("get_transcript_manual", func(t *testing.T) {
		var p1, p1ts, p2 transcriptPage
		s.call(t, "get_transcript", "manual en, page 1", map[string]any{"url": video, "lang": "en", "source": "manual", "limit": pageLimit}, &p1)
		if p1.Source != "manual" || p1.Lang != "en" || p1.Offset != 0 || p1.Returned != pageLimit || !p1.HasMore ||
			p1.NextOffset != pageLimit || p1.TotalSegments <= pageLimit {
			t.Fatalf("page 1 = %+v", p1)
		}
		if timestampRe.MatchString(p1.Text) {
			t.Fatalf("page 1 has timestamps without timestamps=true: %q", excerpt(p1.Text, 80))
		}
		t.Logf("manual page 1: %s/%s, %d of %d segments, %d chars; first line %q", p1.Source, p1.Format, p1.Returned, p1.TotalSegments, len(p1.Text), excerpt(p1.lines()[0], 80))

		s.call(t, "get_transcript", "manual en, page 1, timestamps", map[string]any{"url": video, "lang": "en", "source": "manual", "limit": pageLimit, "timestamps": true}, &p1ts)
		checkTimestamped(t, p1, p1ts)
		t.Logf("manual page 1 with timestamps: first line %q", excerpt(p1ts.lines()[0], 80))

		s.call(t, "get_transcript", "manual en, page 2 (next_offset)", map[string]any{"url": video, "lang": "en", "source": "manual", "limit": pageLimit, "offset": p1.NextOffset}, &p2)
		if p2.Offset != p1.NextOffset || p2.Returned == 0 || p2.TotalSegments != p1.TotalSegments || p2.NextOffset != p2.Offset+p2.Returned ||
			p2.HasMore != (p2.NextOffset < p2.TotalSegments) {
			t.Fatalf("page 2 = %+v", p2)
		}
		t.Logf("manual page 2: offset %d, %d segments, next_offset %d, has_more %v; first line %q", p2.Offset, p2.Returned, p2.NextOffset, p2.HasMore, excerpt(p2.lines()[0], 80))

		var def transcriptPage
		s.call(t, "get_transcript", "default source", map[string]any{"url": video, "limit": 5}, &def)
		if def.Source != "manual" || def.Lang != "en" {
			t.Fatalf("default source/lang = %s/%s, want manual/en", def.Source, def.Lang)
		}
	})

	t.Run("get_transcript_auto", func(t *testing.T) {
		if autoOriginal == "" {
			t.Fatal("list_subtitles found no original-language automatic track")
		}
		var p, pts transcriptPage
		s.call(t, "get_transcript", "auto "+autoOriginal, map[string]any{"url": video, "lang": autoOriginal, "source": "auto", "limit": pageLimit}, &p)
		if p.Source != "auto" || p.Returned == 0 || p.TotalSegments == 0 {
			t.Fatalf("auto page = %+v", p)
		}
		lines := p.lines()
		for i := 1; i < len(lines); i++ {
			if lines[i] == lines[i-1] {
				t.Fatalf("rolling duplicate survived at line %d: %q", i, lines[i])
			}
			if strings.ContainsAny(lines[i], "<>") {
				t.Fatalf("markup survived at line %d: %q", i, lines[i])
			}
		}
		t.Logf("auto: %s/%s, %d segments total, %d chars on the page; first line %q", p.Source, p.Format, p.TotalSegments, len(p.Text), excerpt(lines[0], 80))

		s.call(t, "get_transcript", "auto "+autoOriginal+", timestamps", map[string]any{"url": video, "lang": autoOriginal, "source": "auto", "limit": pageLimit, "timestamps": true}, &pts)
		checkTimestamped(t, p, pts)
		t.Logf("auto with timestamps: first line %q", excerpt(pts.lines()[0], 80))
	})

	t.Run("ssrf_rejected_before_exec", func(t *testing.T) {
		var before int
		if ctr != nil {
			before = len(ctr.ytdlpRuns(t))
		}
		cases := []struct{ tool, url, want string }{
			{"get_info", "https://example.com/x", `host "example.com" is not allowed; allowed hosts: `},
			{"list_subtitles", "https://example.com/x", `host "example.com" is not allowed; allowed hosts: `},
			{"get_transcript", "https://example.com/x", `host "example.com" is not allowed; allowed hosts: `},
			{"get_info", "http://169.254.169.254/latest/meta-data/", `host "169.254.169.254" is an IP address; only allowed hostnames are accepted`},
			{"get_transcript", "http://127.0.0.1:8080/healthz", "port 8080 is not allowed"},
			{"get_info", "http://[::1]/", "IP literals, encoded or non-ASCII hosts are not allowed"},
			{"list_subtitles", "https://www.youtube.com./watch?v=" + defaultVideo, "ends with a dot"},
		}
		for _, c := range cases {
			msg := s.callErr(t, c.tool, "rejected "+c.url, map[string]any{"url": c.url})
			if !strings.Contains(msg, c.want) {
				t.Errorf("%s(%s) error = %q, want containing %q", c.tool, c.url, msg, c.want)
			}
			t.Logf("%s(%s) rejected: %s", c.tool, c.url, msg)
		}
		if ctr == nil {
			return
		}
		if after := len(ctr.ytdlpRuns(t)); after != before {
			t.Fatalf("yt-dlp ran %d time(s) for rejected URLs", after-before)
		}
	})

	t.Run("argv_invariants", func(t *testing.T) {
		if ctr == nil {
			t.Skip("YTDLP_MCP_LIVE_URL is set; no container log to read")
		}
		runs := ctr.ytdlpRuns(t)
		if len(runs) == 0 {
			t.Fatal("no yt-dlp invocations in the debug log")
		}
		for _, args := range runs {
			n := len(args)
			if n < 2 || args[n-2] != "--" || slices.Index(args, "--") != n-2 {
				t.Fatalf("target is not the single argument after a final \"--\": %q", args)
			}
			for _, want := range [][]string{{"--ignore-config"}, {"--no-remote-components"}, {"--use-extractors", "default,-generic"}, {"--no-cookies"}, {"--skip-download"}} {
				if !containsSeq(args[:n-2], want) {
					t.Fatalf("argv lacks %q: %q", want, args)
				}
			}
		}
		t.Logf("%d yt-dlp invocations, every one read-only with the target after \"--\"", len(runs))
	})
}
