// Package mcpserver exposes the four read-only yt-dlp tools (search,
// get_info, list_subtitles, get_transcript) as an MCP server and serves it
// over streamable HTTP.
//
// Every URL argument is resolved through urlguard before ytdlp is called, and
// every result is bounded: search is capped at 25 hits, descriptions are
// truncated unless asked for, the automatic-caption list is compact by
// default, and transcripts are paginated by segment with a per-page byte
// budget. A fetched transcript is kept for [Options.CacheTTL] in a small LRU
// bounded by entry count and bytes, so paging through it costs one yt-dlp
// run. Tool errors are returned as MCP tool errors (isError results) whose
// text tells the agent what to do next.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jedwards1230/yt-dlp-mcp/internal/urlguard"
	"github.com/jedwards1230/yt-dlp-mcp/internal/version"
	"github.com/jedwards1230/yt-dlp-mcp/internal/ytdlp"
)

// Limits on what a single tool call returns.
const (
	defaultSearchLimit    = 10
	maxSearchLimit        = 25
	maxQueryLength        = 200
	descriptionLimit      = 1500
	defaultTranscriptPage = 500
	maxTranscriptPage     = 2000
	maxPageBytes          = 40000 // UTF-8 bytes of page text, not characters
	defaultLang           = "en"
)

// Transcript cache bounds: at most cacheEntries transcripts and about
// cacheBytes of memory, whichever binds first.
const (
	cacheEntries = 32
	cacheBytes   = 64 << 20
)

// Backend is the subset of *ytdlp.Runner the tools use.
type Backend interface {
	Search(ctx context.Context, query string, limit int) ([]ytdlp.SearchEntry, error)
	Info(ctx context.Context, target string) (ytdlp.VideoInfo, ytdlp.Tracks, error)
	Subtitles(ctx context.Context, target string) (ytdlp.VideoInfo, ytdlp.Tracks, error)
	Transcript(ctx context.Context, target, lang string, src ytdlp.Source) (ytdlp.SubtitleFile, error)
}

// Options configures New.
type Options struct {
	Backend   Backend
	Allowlist *urlguard.Allowlist
	// CacheTTL keeps fetched transcripts for pagination; 0 disables caching.
	CacheTTL time.Duration
	Logger   *slog.Logger
}

type server struct {
	backend Backend
	allow   *urlguard.Allowlist
	cache   *transcriptCache
	log     *slog.Logger
}

const instructions = "Read-only access to online video metadata and transcripts through yt-dlp. " +
	"Typical flow: search → get_info or list_subtitles → get_transcript, paginating with next_offset. " +
	"Nothing is ever downloaded. URL arguments accept a video URL or a bare 11-character YouTube video id; " +
	"only http(s) URLs on the server's host allowlist are accepted."

// New builds the MCP server with all tools registered.
func New(opts Options) *mcp.Server {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{
		backend: opts.Backend,
		allow:   opts.Allowlist,
		cache:   newTranscriptCache(opts.CacheTTL, cacheEntries, cacheBytes, time.Now),
		log:     log,
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "yt-dlp-mcp", Version: version.Value}, &mcp.ServerOptions{
		Instructions: instructions,
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "search",
		Description: "Search YouTube and return up to `limit` videos with id, title, url, and channel/duration when the listing includes them. " +
			"Fast flat listing: view counts and dates may be missing, so call get_info on a result for full metadata.",
		Annotations: readOnly("Search YouTube"),
		InputSchema: inputSchema[searchInput](map[string]any{"limit": defaultSearchLimit}, nil),
	}, s.search)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_info",
		Description: "Get trimmed metadata for one video: title, channel, duration, upload date, description " +
			fmt.Sprintf("(first %d characters unless full_description), chapters, view/like counts, ", descriptionLimit) +
			"and whether manual subtitles or automatic captions exist. Nothing is downloaded.",
		Annotations: readOnly("Get video info"),
		InputSchema: inputSchema[getInfoInput](nil, nil),
	}, s.getInfo)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_subtitles",
		Description: "List a video's subtitle languages, split into manual (uploaded) tracks and automatic (speech-recognition) captions. " +
			"The original-language automatic track is flagged original=true (YouTube codes it \"<lang>-orig\"); machine-translated automatic " +
			"tracks are only counted unless include_translations is true. Use a code from here as get_transcript's lang.",
		Annotations: readOnly("List subtitles"),
		InputSchema: inputSchema[listSubtitlesInput](nil, nil),
	}, s.listSubtitles)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_transcript",
		Description: "Fetch a video's subtitles as clean plain text: one caption line per output line, markup and inline timestamps stripped, " +
			"automatic-caption rolling duplicates removed. Track choice for `lang`: the manual track coded exactly `lang`; else a named manual " +
			"track in that language, which YouTube codes `<lang>-<track id>` (e.g. en-eEY6OEpapPo; a region code such as en-GB does not count); " +
			"else the automatic captions in `lang`. `source` limits the choice to manual or automatic tracks, and the result's lang is the code " +
			"actually used. Paginated by segment (caption line): check has_more and pass next_offset as offset " +
			fmt.Sprintf("to continue. A page holds at most `limit` segments and at most %d bytes of text; a single longer line comes back alone.", maxPageBytes),
		Annotations: readOnly("Get transcript"),
		InputSchema: inputSchema[getTranscriptInput](
			map[string]any{"lang": defaultLang, "source": "any", "offset": 0, "limit": defaultTranscriptPage},
			map[string][]any{"source": {"any", "manual", "auto"}},
		),
	}, s.getTranscript)

	return srv
}

func boolPtr(b bool) *bool { return &b }

// readOnly annotates a tool as a side-effect-free reader of the open web.
func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		DestructiveHint: boolPtr(false),
		IdempotentHint:  true,
		OpenWorldHint:   boolPtr(true),
	}
}

// inputSchema reflects T's schema (descriptions come from jsonschema struct
// tags) and adds defaults and enums, which struct tags cannot express. The
// SDK applies schema defaults to incoming arguments before the handler runs.
func inputSchema[T any](defaults map[string]any, enums map[string][]any) *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("input schema for %T: %v", *new(T), err))
	}
	for name, v := range defaults {
		raw, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		mustProp(s, name).Default = raw
	}
	for name, vals := range enums {
		mustProp(s, name).Enum = vals
	}
	return s
}

func mustProp(s *jsonschema.Schema, name string) *jsonschema.Schema {
	p, ok := s.Properties[name]
	if !ok {
		panic(fmt.Sprintf("schema has no property %q", name))
	}
	return p
}

// logCall records one tool call's outcome.
func (s *server) logCall(tool string, start time.Time, err error, attrs ...any) {
	attrs = append(attrs, "tool", tool, "duration_ms", time.Since(start).Milliseconds())
	if err != nil {
		s.log.Info("tool call failed", append(attrs, "error", err.Error())...)
		return
	}
	s.log.Info("tool call", attrs...)
}
