//go:build live

package live

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Configuration, all optional:
//
//	YTDLP_MCP_LIVE_URL    MCP endpoint (default http://localhost:8080/mcp)
//	YTDLP_MCP_LIVE_TOKEN  bearer token, when the server has one configured
//	YTDLP_MCP_LIVE_VIDEO  a public video id or URL with manual English subtitles
//	YTDLP_MCP_LIVE_QUERY  search query
//
// Run: go test -tags live -count=1 -v ./test/live/
const (
	defaultEndpoint = "http://localhost:8080/mcp"
	// A long-lived public talk with uploaded English subtitles and
	// automatic captions.
	defaultVideo = "iG9CE55wbtY"
	defaultQuery = "ted talk creativity schools"
)

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

type session struct {
	t *testing.T
	s *mcp.ClientSession
}

// call runs a tool and decodes its structured output into out, failing the
// test on a tool error. It logs the call's wall time.
func (s session) call(name string, args map[string]any, out any) {
	s.t.Helper()
	start := time.Now()
	res, err := s.s.CallTool(s.t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	s.t.Logf("%s %v took %s", name, args, time.Since(start).Round(time.Millisecond))
	if res.IsError {
		s.t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		s.t.Fatalf("decoding %s output: %v\n%s", name, err, raw)
	}
}

// callErr runs a tool that must fail and returns its error text.
func (s session) callErr(name string, args map[string]any) string {
	s.t.Helper()
	start := time.Now()
	res, err := s.s.CallTool(s.t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	s.t.Logf("%s %v took %s", name, args, time.Since(start).Round(time.Millisecond))
	if !res.IsError {
		s.t.Fatalf("%s succeeded; want a tool error", name)
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

func excerpt(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func TestLiveSmoke(t *testing.T) {
	endpoint := env("YTDLP_MCP_LIVE_URL", defaultEndpoint)
	video := env("YTDLP_MCP_LIVE_VIDEO", defaultVideo)
	query := env("YTDLP_MCP_LIVE_QUERY", defaultQuery)
	httpClient := &http.Client{Transport: bearer{token: os.Getenv("YTDLP_MCP_LIVE_TOKEN")}}

	t.Run("healthz", func(t *testing.T) {
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/healthz"
		resp, err := http.Get(u.String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("GET %s = %d %q", u, resp.StatusCode, body)
		}
	})

	start := time.Now()
	client := mcp.NewClient(&mcp.Implementation{Name: "yt-dlp-mcp-live-smoke", Version: "0.0.0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("MCP initialize against %s: %v", endpoint, err)
	}
	defer func() { _ = cs.Close() }()
	init := cs.InitializeResult()
	t.Logf("initialize: server %s %s, protocol %s, took %s", init.ServerInfo.Name, init.ServerInfo.Version, init.ProtocolVersion, time.Since(start).Round(time.Millisecond))
	if init.ServerInfo.Name != "yt-dlp-mcp" {
		t.Fatalf("server name = %q", init.ServerInfo.Name)
	}

	t.Run("tools/list", func(t *testing.T) {
		var names []string
		for tool, err := range cs.Tools(t.Context(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, tool.Name)
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"get_info", "get_transcript", "list_subtitles", "search"}) {
			t.Fatalf("tools = %v", names)
		}
	})

	t.Run("search", func(t *testing.T) {
		var out struct {
			Count   int `json:"count"`
			Results []struct {
				ID, Title, URL, Duration string
			} `json:"results"`
		}
		session{t, cs}.call("search", map[string]any{"query": query, "limit": 3}, &out)
		if out.Count < 1 || out.Count > 3 || out.Results[0].ID == "" || out.Results[0].URL == "" {
			t.Fatalf("search = %+v", out)
		}
		t.Logf("search: %d results; first %q (%s) %s", out.Count, out.Results[0].Title, out.Results[0].Duration, out.Results[0].URL)
	})

	t.Run("get_info", func(t *testing.T) {
		var out struct {
			ID, Title, Channel, Duration string
			UploadDate                   string   `json:"upload_date"`
			HasSubtitles                 bool     `json:"has_subtitles"`
			SubtitleLanguages            []string `json:"subtitle_languages"`
			AutomaticOriginal            string   `json:"automatic_captions_original"`
			DescriptionLength            int      `json:"description_length"`
		}
		session{t, cs}.call("get_info", map[string]any{"url": video}, &out)
		if out.ID == "" || out.Title == "" || out.Duration == "" || !out.HasSubtitles || !slices.Contains(out.SubtitleLanguages, "en") {
			t.Fatalf("get_info = %+v", out)
		}
		t.Logf("get_info: %q by %q, %s, uploaded %s, %d manual languages, auto original %q", out.Title, out.Channel, out.Duration, out.UploadDate, len(out.SubtitleLanguages), out.AutomaticOriginal)
	})

	var autoOriginal string
	t.Run("list_subtitles", func(t *testing.T) {
		var out struct {
			Manual    []struct{ Code, Name string } `json:"manual"`
			Automatic []struct {
				Code, Name string
				Original   bool
			} `json:"automatic"`
			AutomaticTotal   int    `json:"automatic_total"`
			AutomaticOmitted int    `json:"automatic_omitted"`
			Hint             string `json:"hint"`
		}
		session{t, cs}.call("list_subtitles", map[string]any{"url": video}, &out)
		hasEn := slices.ContainsFunc(out.Manual, func(m struct{ Code, Name string }) bool { return m.Code == "en" })
		if !hasEn {
			t.Fatalf("no manual en track: %+v", out.Manual)
		}
		for _, a := range out.Automatic {
			if a.Original {
				autoOriginal = a.Code
			}
		}
		t.Logf("list_subtitles: %d manual, automatic original %q, %d automatic total (%d omitted); hint: %s", len(out.Manual), autoOriginal, out.AutomaticTotal, out.AutomaticOmitted, out.Hint)
	})

	type page struct {
		Source        string `json:"source"`
		Format        string `json:"format"`
		Offset        int    `json:"offset"`
		Returned      int    `json:"returned"`
		TotalSegments int    `json:"total_segments"`
		NextOffset    int    `json:"next_offset"`
		HasMore       bool   `json:"has_more"`
		Text          string `json:"text"`
	}
	t.Run("get_transcript", func(t *testing.T) {
		s := session{t, cs}
		var p1 page
		s.call("get_transcript", map[string]any{"url": video, "lang": "en", "limit": 40}, &p1)
		if p1.Source != "manual" || p1.Returned != 40 || !p1.HasMore || p1.NextOffset != 40 || p1.TotalSegments <= 40 {
			t.Fatalf("page 1 = %+v", p1)
		}
		t.Logf("get_transcript page 1: %s/%s, %d of %d segments; first line %q", p1.Source, p1.Format, p1.Returned, p1.TotalSegments, excerpt(strings.SplitN(p1.Text, "\n", 2)[0], 80))

		var p2 page
		s.call("get_transcript", map[string]any{"url": video, "lang": "en", "limit": 40, "offset": p1.NextOffset, "timestamps": true}, &p2)
		if p2.Offset != 40 || p2.Returned == 0 || !strings.HasPrefix(p2.Text, "[") {
			t.Fatalf("page 2 = %+v", p2)
		}
		t.Logf("get_transcript page 2 (timestamps, cached): first line %q", excerpt(strings.SplitN(p2.Text, "\n", 2)[0], 80))
	})

	t.Run("get_transcript_auto", func(t *testing.T) {
		if autoOriginal == "" {
			t.Skip("video has no original-language automatic track")
		}
		var p page
		session{t, cs}.call("get_transcript", map[string]any{"url": video, "lang": autoOriginal, "source": "auto", "limit": 20}, &p)
		if p.Source != "auto" || p.Returned == 0 {
			t.Fatalf("auto page = %+v", p)
		}
		lines := strings.Split(p.Text, "\n")
		for i := 1; i < len(lines); i++ {
			if lines[i] == lines[i-1] {
				t.Fatalf("rolling duplicate survived at line %d: %q", i, lines[i])
			}
		}
		t.Logf("get_transcript auto: %s/%s, %d segments total; first line %q", p.Source, p.Format, p.TotalSegments, excerpt(lines[0], 80))
	})

	t.Run("ssrf_rejected", func(t *testing.T) {
		s := session{t, cs}
		msg := s.callErr("get_info", map[string]any{"url": "http://169.254.169.254/latest/meta-data/"})
		if !strings.Contains(msg, "IP address") {
			t.Fatalf("metadata-IP rejection = %q", msg)
		}
		t.Logf("ssrf rejection: %s", msg)
		msg = s.callErr("get_transcript", map[string]any{"url": "https://example.com/video"})
		if !strings.Contains(msg, `host "example.com" is not allowed`) {
			t.Fatalf("host rejection = %q", msg)
		}
		t.Logf("host rejection: %s", msg)
	})
}
