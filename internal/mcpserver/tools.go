package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jedwards1230/yt-dlp-mcp/internal/transcript"
	"github.com/jedwards1230/yt-dlp-mcp/internal/urlguard"
	"github.com/jedwards1230/yt-dlp-mcp/internal/ytdlp"
)

// ── search ──────────────────────────────────────────────────────────────────

type searchInput struct {
	Query string `json:"query" jsonschema:"what to search YouTube for"`
	Limit int    `json:"limit,omitempty" jsonschema:"number of results, 1-25; larger values are clamped to 25"`
}

type searchResult struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	Channel         string `json:"channel,omitempty"`
	ChannelID       string `json:"channel_id,omitempty"`
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
	Duration        string `json:"duration,omitempty" jsonschema:"human-readable duration, m:ss or h:mm:ss"`
	ViewCount       *int64 `json:"view_count,omitempty"`
	LiveStatus      string `json:"live_status,omitempty"`
}

type searchOutput struct {
	Query   string         `json:"query"`
	Limit   int            `json:"limit" jsonschema:"the limit actually applied after clamping"`
	Count   int            `json:"count"`
	Results []searchResult `json:"results"`
}

func (s *server) search(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (_ *mcp.CallToolResult, out searchOutput, err error) {
	start := time.Now()
	defer func() { s.logCall("search", start, err, "limit", out.Limit) }()

	query := strings.TrimSpace(in.Query)
	if err := validateQuery(query); err != nil {
		return nil, out, err
	}
	if !s.allow.Allows(urlguard.CanonicalYouTubeHost) {
		return nil, out, fmt.Errorf("search uses YouTube, but %s is not on this server's allowed host list", urlguard.CanonicalYouTubeHost)
	}
	limit := clamp(in.Limit, defaultSearchLimit, 1, maxSearchLimit)

	entries, err := s.backend.Search(ctx, query, limit)
	if err != nil {
		return nil, out, err
	}
	out = searchOutput{Query: query, Limit: limit, Results: make([]searchResult, 0, len(entries))}
	for _, e := range entries {
		r := searchResult{
			ID: e.ID, Title: e.Title, URL: e.URL, Channel: e.Channel, ChannelID: e.ChannelID,
			ViewCount: intPtr(e.ViewCount), LiveStatus: e.LiveStatus,
		}
		if d := intPtr(e.Duration); d != nil {
			r.DurationSeconds = d
			r.Duration = transcript.FormatDuration(*d)
		}
		out.Results = append(out.Results, r)
	}
	out.Count = len(out.Results)
	return nil, out, nil
}

func validateQuery(q string) error {
	if q == "" {
		return errors.New("query is required")
	}
	if n := utf8.RuneCountInString(q); n > maxQueryLength {
		return fmt.Errorf("query is %d characters; the limit is %d", n, maxQueryLength)
	}
	for _, r := range q {
		if unicode.IsControl(r) {
			return errors.New("query must not contain control characters or newlines")
		}
	}
	return nil
}

// ── get_info ────────────────────────────────────────────────────────────────

type getInfoInput struct {
	URL             string `json:"url" jsonschema:"video URL (http/https on the server's allowed hosts) or a bare 11-character YouTube video id"`
	FullDescription bool   `json:"full_description,omitempty" jsonschema:"return the whole description instead of the first 1500 characters"`
	IncludeTags     bool   `json:"include_tags,omitempty" jsonschema:"include the uploader's keyword tags"`
}

type chapterOut struct {
	Start        string `json:"start" jsonschema:"start time as mm:ss or hh:mm:ss"`
	StartSeconds int64  `json:"start_seconds"`
	EndSeconds   int64  `json:"end_seconds"`
	Title        string `json:"title"`
}

type getInfoOutput struct {
	ID                   string       `json:"id"`
	Title                string       `json:"title"`
	URL                  string       `json:"url"`
	Channel              string       `json:"channel,omitempty"`
	ChannelID            string       `json:"channel_id,omitempty"`
	ChannelURL           string       `json:"channel_url,omitempty"`
	ChannelHandle        string       `json:"channel_handle,omitempty"`
	DurationSeconds      *int64       `json:"duration_seconds,omitempty"`
	Duration             string       `json:"duration,omitempty" jsonschema:"human-readable duration, m:ss or h:mm:ss"`
	UploadDate           string       `json:"upload_date,omitempty" jsonschema:"YYYY-MM-DD"`
	LiveStatus           string       `json:"live_status,omitempty"`
	Availability         string       `json:"availability,omitempty"`
	Language             string       `json:"language,omitempty" jsonschema:"spoken language reported by the site"`
	AgeLimit             *int64       `json:"age_limit,omitempty"`
	ViewCount            *int64       `json:"view_count,omitempty"`
	LikeCount            *int64       `json:"like_count,omitempty"`
	CommentCount         *int64       `json:"comment_count,omitempty"`
	Description          string       `json:"description"`
	DescriptionTruncated bool         `json:"description_truncated"`
	DescriptionLength    int          `json:"description_length" jsonschema:"length of the full description in characters"`
	Chapters             []chapterOut `json:"chapters,omitempty"`
	Categories           []string     `json:"categories,omitempty"`
	Tags                 []string     `json:"tags,omitempty"`
	HasSubtitles         bool         `json:"has_subtitles" jsonschema:"manual (uploaded) subtitles exist"`
	SubtitleLanguages    []string     `json:"subtitle_languages,omitempty" jsonschema:"manual subtitle language codes"`
	HasAutomaticCaptions bool         `json:"has_automatic_captions"`
	AutomaticOriginal    string       `json:"automatic_captions_original,omitempty" jsonschema:"code of the untranslated automatic track, e.g. en-orig"`
	AutomaticCount       int          `json:"automatic_captions_count" jsonschema:"number of automatic tracks, machine translations included"`
}

func (s *server) getInfo(ctx context.Context, _ *mcp.CallToolRequest, in getInfoInput) (_ *mcp.CallToolResult, out getInfoOutput, err error) {
	start := time.Now()
	var target urlguard.Target
	defer func() { s.logCall("get_info", start, err, "target", target.URL) }()

	if target, err = s.allow.Resolve(in.URL); err != nil {
		return nil, out, err
	}
	info, tracks, err := s.backend.Info(ctx, target.URL)
	if err != nil {
		return nil, out, err
	}

	out = getInfoOutput{
		ID: info.ID, Title: info.Title, URL: firstNonEmpty(info.WebpageURL, target.URL),
		Channel: firstNonEmpty(info.Channel, info.Uploader), ChannelID: info.ChannelID, ChannelURL: info.ChannelURL,
		UploadDate:   isoDate(firstNonEmpty(info.UploadDate, info.ReleaseDate)),
		LiveStatus:   info.LiveStatus,
		Availability: info.Availability,
		Language:     info.Language,
		AgeLimit:     intPtr(info.AgeLimit),
		ViewCount:    intPtr(info.ViewCount),
		LikeCount:    intPtr(info.LikeCount),
		CommentCount: intPtr(info.CommentCount),
		Categories:   info.Categories,
	}
	if strings.HasPrefix(info.UploaderID, "@") {
		out.ChannelHandle = info.UploaderID
	}
	if d := intPtr(info.Duration); d != nil {
		out.DurationSeconds = d
		out.Duration = transcript.FormatDuration(*d)
	}
	out.DescriptionLength = utf8.RuneCountInString(info.Description)
	out.Description = info.Description
	if !in.FullDescription && out.DescriptionLength > descriptionLimit {
		out.Description = string([]rune(info.Description)[:descriptionLimit])
		out.DescriptionTruncated = true
	}
	for _, c := range info.Chapters {
		st := int64(math.Round(c.StartTime))
		out.Chapters = append(out.Chapters, chapterOut{
			Start:        transcript.FormatTimestamp(time.Duration(st) * time.Second),
			StartSeconds: st,
			EndSeconds:   int64(math.Round(c.EndTime)),
			Title:        c.Title,
		})
	}
	if in.IncludeTags {
		out.Tags = info.Tags
	}
	for _, t := range tracks.Manual {
		out.SubtitleLanguages = append(out.SubtitleLanguages, t.Code)
	}
	out.HasSubtitles = len(tracks.Manual) > 0
	out.HasAutomaticCaptions = len(tracks.Automatic) > 0
	out.AutomaticOriginal = tracks.OriginalAutomatic()
	out.AutomaticCount = len(tracks.Automatic)
	return nil, out, nil
}

// ── list_subtitles ──────────────────────────────────────────────────────────

type listSubtitlesInput struct {
	URL                 string `json:"url" jsonschema:"video URL (http/https on the server's allowed hosts) or a bare 11-character YouTube video id"`
	IncludeTranslations bool   `json:"include_translations,omitempty" jsonschema:"also list every machine-translated automatic track (YouTube offers about 150)"`
}

type trackOut struct {
	Code     string `json:"code"`
	Name     string `json:"name,omitempty"`
	Original bool   `json:"original,omitempty" jsonschema:"the untranslated automatic track in the video's spoken language"`
}

type listSubtitlesOutput struct {
	VideoID          string     `json:"video_id"`
	Title            string     `json:"title"`
	URL              string     `json:"url"`
	Language         string     `json:"language,omitempty" jsonschema:"spoken language reported by the site"`
	Manual           []trackOut `json:"manual" jsonschema:"uploaded subtitles"`
	Automatic        []trackOut `json:"automatic" jsonschema:"automatic captions; by default only the original-language track"`
	AutomaticTotal   int        `json:"automatic_total" jsonschema:"number of automatic tracks including machine translations"`
	AutomaticOmitted int        `json:"automatic_omitted" jsonschema:"machine-translated automatic tracks left out; set include_translations to list them"`
	Hint             string     `json:"hint,omitempty"`
}

func (s *server) listSubtitles(ctx context.Context, _ *mcp.CallToolRequest, in listSubtitlesInput) (_ *mcp.CallToolResult, out listSubtitlesOutput, err error) {
	start := time.Now()
	var target urlguard.Target
	defer func() { s.logCall("list_subtitles", start, err, "target", target.URL) }()

	if target, err = s.allow.Resolve(in.URL); err != nil {
		return nil, out, err
	}
	info, tracks, err := s.backend.Subtitles(ctx, target.URL)
	if err != nil {
		return nil, out, err
	}
	out = listSubtitlesOutput{
		VideoID: info.ID, Title: info.Title, URL: firstNonEmpty(info.WebpageURL, target.URL), Language: info.Language,
		Manual: make([]trackOut, 0, len(tracks.Manual)), Automatic: []trackOut{},
		AutomaticTotal: len(tracks.Automatic),
	}
	for _, t := range tracks.Manual {
		out.Manual = append(out.Manual, trackOut{Code: t.Code, Name: t.Name})
	}
	orig := tracks.OriginalAutomatic()
	// Without an "-orig" marker (non-YouTube sites) there is no way to tell
	// originals from translations, so everything is listed.
	listAll := in.IncludeTranslations || orig == ""
	for _, t := range tracks.Automatic {
		isOrig := t.Code == orig
		if listAll || isOrig {
			out.Automatic = append(out.Automatic, trackOut{Code: t.Code, Name: t.Name, Original: isOrig})
		}
	}
	out.AutomaticOmitted = out.AutomaticTotal - len(out.Automatic)
	out.Hint = subtitleHint(out.Manual, info.Language, orig)
	return nil, out, nil
}

// subtitleHint suggests get_transcript arguments: the manual track in the
// video's spoken language (else English, else the first one listed) and the
// original automatic track.
func subtitleHint(manual []trackOut, spoken, orig string) string {
	var parts []string
	if code := preferredManual(manual, spoken); code != "" {
		parts = append(parts, fmt.Sprintf("get_transcript lang=%q returns the manual track", code))
	}
	if orig != "" {
		parts = append(parts, fmt.Sprintf("lang=%q source=\"auto\" returns the original automatic captions", orig))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func preferredManual(manual []trackOut, spoken string) string {
	if len(manual) == 0 {
		return ""
	}
	for _, want := range []string{spoken, defaultLang} {
		if want == "" {
			continue
		}
		for _, m := range manual {
			if m.Code == want {
				return m.Code
			}
		}
		for _, m := range manual {
			if strings.HasPrefix(m.Code, want+"-") {
				return m.Code
			}
		}
	}
	return manual[0].Code
}

// ── get_transcript ──────────────────────────────────────────────────────────

type getTranscriptInput struct {
	URL        string `json:"url" jsonschema:"video URL (http/https on the server's allowed hosts) or a bare 11-character YouTube video id"`
	Lang       string `json:"lang,omitempty" jsonschema:"subtitle language code from list_subtitles, e.g. en, pt-BR, en-orig"`
	Source     string `json:"source,omitempty" jsonschema:"any (manual track if present, else automatic captions), manual, or auto"`
	Timestamps bool   `json:"timestamps,omitempty" jsonschema:"prefix each line with its start time as [mm:ss] or [hh:mm:ss]"`
	Offset     int    `json:"offset,omitempty" jsonschema:"index of the first segment to return; pass next_offset from the previous page"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum segments (caption lines) per page, 1-2000; larger values are clamped"`
}

type getTranscriptOutput struct {
	VideoID        string `json:"video_id"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	Lang           string `json:"lang"`
	Source         string `json:"source" jsonschema:"manual or auto, the kind of track actually used"`
	AutoTranslated bool   `json:"auto_translated,omitempty" jsonschema:"the automatic track is a machine translation, not the spoken language"`
	Format         string `json:"format" jsonschema:"subtitle format converted: json3, vtt or srt"`
	Offset         int    `json:"offset"`
	Returned       int    `json:"returned" jsonschema:"segments on this page"`
	TotalSegments  int    `json:"total_segments"`
	NextOffset     int    `json:"next_offset" jsonschema:"offset for the next page; equals total_segments when done"`
	HasMore        bool   `json:"has_more"`
	Text           string `json:"text" jsonschema:"one caption line per line"`
}

// cachedTranscript is a fetched, cleaned transcript plus its metadata.
type cachedTranscript struct {
	videoID, title, url, lang string
	automatic, translated     bool
	format                    transcript.Format
	segments                  []transcript.Segment
}

func (s *server) getTranscript(ctx context.Context, _ *mcp.CallToolRequest, in getTranscriptInput) (_ *mcp.CallToolResult, out getTranscriptOutput, err error) {
	start := time.Now()
	var target urlguard.Target
	cached := false
	defer func() {
		s.logCall("get_transcript", start, err, "target", target.URL, "cached", cached, "offset", in.Offset)
	}()

	if target, err = s.allow.Resolve(in.URL); err != nil {
		return nil, out, err
	}
	lang := strings.TrimSpace(in.Lang)
	if lang == "" {
		lang = defaultLang
	}
	if err := ytdlp.ValidateLang(lang); err != nil {
		return nil, out, err
	}
	var src ytdlp.Source
	switch strings.ToLower(strings.TrimSpace(in.Source)) {
	case "", "any":
		src = ytdlp.SourceAny
	case "manual":
		src = ytdlp.SourceManual
	case "auto":
		src = ytdlp.SourceAutomatic
	default:
		return nil, out, fmt.Errorf("source %q is not one of any, manual, auto", in.Source)
	}
	if in.Offset < 0 {
		return nil, out, fmt.Errorf("offset %d is negative; start at 0", in.Offset)
	}
	limit := clamp(in.Limit, defaultTranscriptPage, 1, maxTranscriptPage)

	key := target.URL + "\x00" + lang + "\x00" + string(src)
	t, ok := s.cache.get(key)
	cached = ok
	if !ok {
		if t, err = s.fetchTranscript(ctx, target, lang, src); err != nil {
			return nil, out, err
		}
		s.cache.put(key, t)
	}

	page, err := transcript.Paginate(t.segments, in.Offset, limit, maxPageChars, in.Timestamps)
	if err != nil {
		return nil, out, err
	}
	source := "manual"
	if t.automatic {
		source = "auto"
	}
	return nil, getTranscriptOutput{
		VideoID: t.videoID, Title: t.title, URL: t.url, Lang: t.lang, Source: source,
		AutoTranslated: t.translated, Format: string(t.format),
		Offset: page.Offset, Returned: page.Returned, TotalSegments: page.Total,
		NextOffset: page.NextOffset, HasMore: page.HasMore, Text: page.Text,
	}, nil
}

func (s *server) fetchTranscript(ctx context.Context, target urlguard.Target, lang string, src ytdlp.Source) (*cachedTranscript, error) {
	f, err := s.backend.Transcript(ctx, target.URL, lang, src)
	if err != nil {
		return nil, err
	}
	segs, err := transcript.Parse(f.Data, f.Format)
	if err != nil {
		return nil, fmt.Errorf("converting the %s %s track: %w", lang, f.Format, err)
	}
	segs = transcript.Clean(segs, f.Automatic)
	if len(segs) == 0 {
		return nil, fmt.Errorf("the %s subtitle track exists but contains no text", lang)
	}
	t := &cachedTranscript{
		videoID: f.Info.ID, title: f.Info.Title, url: firstNonEmpty(f.Info.WebpageURL, target.URL),
		lang: f.Lang, automatic: f.Automatic, format: f.Format, segments: segs,
	}
	if orig := f.Tracks.OriginalAutomatic(); f.Automatic && orig != "" {
		spoken := strings.TrimSuffix(orig, "-orig")
		t.translated = lang != orig && lang != spoken
	}
	return t, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// clamp returns def for 0 and otherwise bounds v to [lo, hi].
func clamp(v, def, lo, hi int) int {
	if v == 0 {
		v = def
	}
	return min(max(v, lo), hi)
}

func intPtr(f *float64) *int64 {
	if f == nil || math.IsNaN(*f) || math.IsInf(*f, 0) {
		return nil
	}
	v := int64(math.Round(*f))
	return &v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// isoDate turns yt-dlp's YYYYMMDD into YYYY-MM-DD; anything else passes through.
func isoDate(d string) string {
	if len(d) == 8 && strings.Trim(d, "0123456789") == "" {
		return d[:4] + "-" + d[4:6] + "-" + d[6:]
	}
	return d
}
