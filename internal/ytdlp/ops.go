package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jedwards1230/yt-dlp-mcp/internal/transcript"
)

// Output templates. Each -O prints exactly one line per video: a "j"
// conversion is single-line JSON, and an "l" conversion of a dict prints its
// keys joined by ", " (an empty dict prints an empty line, a missing field
// "NA"). The %(.{a,b})j form selects only the named fields, which keeps
// stdout to a few KB instead of the ~500 KB a full -J dump of a YouTube video
// weighs (mostly per-language caption URLs).
const (
	searchTemplate = "%(.{id,title,url,channel,channel_id,duration,view_count,live_status})j"
	infoTemplate   = "%(.{id,title,fulltitle,channel,channel_id,channel_url,uploader,uploader_id,duration,upload_date,release_date,timestamp,description,chapters,view_count,like_count,comment_count,tags,categories,webpage_url,live_status,availability,language,age_limit})j"
	briefTemplate  = "%(.{id,title,fulltitle,channel,uploader,webpage_url,language,duration})j"
	manualKeys     = "%(subtitles)l"
	autoKeys       = "%(automatic_captions)l"
	// The {name,ext} pair (not {name} alone) keeps every element non-empty:
	// yt-dlp's traversal drops empty dicts, which would misalign names with
	// keys for tracks that have no display name.
	manualNames = "%(subtitles.:.0.{name,ext})j"
	autoNames   = "%(automatic_captions.:.0.{name,ext})j"

	// transcriptBase is the -o template for subtitle files; yt-dlp names
	// them transcriptBase.<lang>.<ext>.
	transcriptBase = "transcript"
)

// singleVideoArgs restrict an operation to one video: a URL that also names
// a playlist resolves to the video, a playlist-only URL on a non-YouTube host
// yields at most its first entry, nothing is downloaded, and a video with no
// downloadable formats still yields its metadata. That last flag also turns
// "this video is unavailable" into a warning with a placeholder record and a
// zero exit; decodeInfo turns such a record back into an error.
var singleVideoArgs = []string{
	"--no-playlist",
	"--playlist-items", "1",
	"--skip-download",
	"--ignore-no-formats-error",
}

// SearchEntry is one search hit. Search runs with --flat-playlist for speed,
// so only fields present in the result listing are filled.
type SearchEntry struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Channel    string   `json:"channel"`
	ChannelID  string   `json:"channel_id"`
	Duration   *float64 `json:"duration"`
	ViewCount  *float64 `json:"view_count"`
	LiveStatus string   `json:"live_status"`
}

// Search runs a YouTube search ("ytsearch<limit>:<query>") and returns up
// to limit entries.
func (r *Runner) Search(ctx context.Context, query string, limit int) ([]SearchEntry, error) {
	var entries []SearchEntry
	build := func(string) []string {
		return []string{"--flat-playlist", "--skip-download", "-O", searchTemplate}
	}
	target := fmt.Sprintf("ytsearch%d:%s", limit, query)
	err := r.run(ctx, "search", build, target, func(_ string, stdout []byte, _ string) error {
		for _, line := range outputLines(stdout) {
			if line == "" || line == "NA" {
				continue
			}
			var e SearchEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				return &Error{Kind: KindParse, Msg: fmt.Sprintf("could not parse a yt-dlp search result: %v", err), Err: err}
			}
			if e.ID != "" && len(entries) < limit {
				entries = append(entries, e)
			}
		}
		return nil
	})
	return entries, err
}

// Chapter is a video chapter.
type Chapter struct {
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Title     string  `json:"title"`
}

// VideoInfo is the selected metadata fields of one video. Numeric fields are
// pointers because extractors omit them freely and zero is a real value.
type VideoInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// FullTitle is the title the extractor found, before yt-dlp substitutes
	// a generic "<site> video #<id>" for a missing one.
	FullTitle    string    `json:"fulltitle"`
	Channel      string    `json:"channel"`
	ChannelID    string    `json:"channel_id"`
	ChannelURL   string    `json:"channel_url"`
	Uploader     string    `json:"uploader"`
	UploaderID   string    `json:"uploader_id"`
	Duration     *float64  `json:"duration"`
	UploadDate   string    `json:"upload_date"`
	ReleaseDate  string    `json:"release_date"`
	Timestamp    *float64  `json:"timestamp"`
	Description  string    `json:"description"`
	Chapters     []Chapter `json:"chapters"`
	ViewCount    *float64  `json:"view_count"`
	LikeCount    *float64  `json:"like_count"`
	CommentCount *float64  `json:"comment_count"`
	Tags         []string  `json:"tags"`
	Categories   []string  `json:"categories"`
	WebpageURL   string    `json:"webpage_url"`
	LiveStatus   string    `json:"live_status"`
	Availability string    `json:"availability"`
	Language     string    `json:"language"`
	AgeLimit     *float64  `json:"age_limit"`
}

// Track is one subtitle language.
type Track struct {
	Code string
	Name string
}

// Tracks lists a video's subtitle languages in yt-dlp's order.
type Tracks struct {
	// Manual are uploaded subtitles.
	Manual []Track
	// Automatic are speech-recognition captions and their machine
	// translations. On YouTube the untranslated track is "<lang>-orig".
	Automatic []Track
}

// OriginalAutomatic returns the code of the untranslated automatic track
// ("en-orig"), or "" when the extractor does not mark one.
func (t Tracks) OriginalAutomatic() string {
	for _, tr := range t.Automatic {
		if strings.HasSuffix(tr.Code, "-orig") {
			return tr.Code
		}
	}
	return ""
}

func codes(ts []Track) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Code
	}
	return out
}

// Info returns a video's metadata and its subtitle language codes (no names).
func (r *Runner) Info(ctx context.Context, target string) (VideoInfo, Tracks, error) {
	var info VideoInfo
	var tracks Tracks
	templates := []string{infoTemplate, manualKeys, autoKeys}
	err := r.run(ctx, "info", printArgs(templates), target, func(_ string, stdout []byte, stderr string) error {
		lines, err := expectLines(stdout, len(templates))
		if err != nil {
			return err
		}
		if err := decodeInfo(lines[0], &info, stderr); err != nil {
			return err
		}
		tracks.Manual = zipTracks(lines[1], "")
		tracks.Automatic = zipTracks(lines[2], "")
		return nil
	})
	return info, tracks, err
}

// Subtitles returns a video's identity and its subtitle tracks with names.
func (r *Runner) Subtitles(ctx context.Context, target string) (VideoInfo, Tracks, error) {
	var info VideoInfo
	var tracks Tracks
	templates := []string{briefTemplate, manualKeys, manualNames, autoKeys, autoNames}
	err := r.run(ctx, "list_subtitles", printArgs(templates), target, func(_ string, stdout []byte, stderr string) error {
		lines, err := expectLines(stdout, len(templates))
		if err != nil {
			return err
		}
		if err := decodeInfo(lines[0], &info, stderr); err != nil {
			return err
		}
		tracks.Manual = zipTracks(lines[1], lines[2])
		tracks.Automatic = zipTracks(lines[3], lines[4])
		return nil
	})
	return info, tracks, err
}

// Source selects which kind of subtitle track a transcript comes from.
type Source string

// Transcript sources.
const (
	// SourceAny prefers a manual track and falls back to automatic captions.
	SourceAny       Source = ""
	SourceManual    Source = "manual"
	SourceAutomatic Source = "auto"
)

// langRe is a BCP-47-ish language tag as yt-dlp names subtitle tracks ("en",
// "pt-BR", "zh-Hans", "en-orig"). It is deliberately narrow: --sub-langs
// treats its value as a comma-separated list of regular expressions, so a
// comma, a leading "-" or any regex metacharacter must never reach it.
var langRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8}){0,3}$`)

// ValidateLang rejects anything that is not a plain language code, including
// "all", which --sub-langs treats as every language.
func ValidateLang(lang string) error {
	if !langRe.MatchString(lang) || strings.EqualFold(lang, "all") {
		return &Error{Kind: KindInvalid, Msg: fmt.Sprintf("language %q is not a subtitle language code like \"en\", \"pt-BR\" or \"en-orig\"; call list_subtitles for the codes this video has", lang)}
	}
	return nil
}

// SubtitleFile is a downloaded subtitle track.
type SubtitleFile struct {
	Info   VideoInfo
	Tracks Tracks
	// Lang is the downloaded track's language code.
	Lang string
	// Automatic reports whether the track is automatic captions.
	Automatic bool
	Format    transcript.Format
	Data      []byte
}

// Transcript downloads the subtitle track lang into the call's scratch
// directory (never the video) and returns its bytes. With SourceAny both
// manual and automatic tracks are requested and yt-dlp keeps the manual one
// when both exist in that language.
func (r *Runner) Transcript(ctx context.Context, target, lang string, src Source) (SubtitleFile, error) {
	var out SubtitleFile
	if err := ValidateLang(lang); err != nil {
		return out, err
	}
	if src != SourceAny && src != SourceManual && src != SourceAutomatic {
		return out, &Error{Kind: KindInvalid, Msg: fmt.Sprintf("source %q is not one of \"manual\", \"auto\" or empty", src)}
	}
	templates := []string{briefTemplate, manualKeys, autoKeys}
	build := func(dir string) []string {
		args := slices.Clone(singleVideoArgs)
		args = append(args,
			// -O implies --simulate, which would skip writing subtitles;
			// --skip-download still guarantees no media is fetched.
			"--no-simulate",
			// Translations of manual subtitles ("fr-en") are slow to build
			// and are never offered by list_subtitles.
			"--extractor-args", "youtube:skip=translated_subs",
		)
		if src != SourceAutomatic {
			args = append(args, "--write-subs")
		}
		if src != SourceManual {
			args = append(args, "--write-auto-subs")
		}
		formats := make([]string, len(transcript.Formats))
		for i, f := range transcript.Formats {
			formats[i] = string(f)
		}
		args = append(args,
			"--sub-langs", lang,
			"--sub-format", strings.Join(formats, "/"),
			"-P", dir,
			"-o", transcriptBase,
		)
		for _, t := range templates {
			args = append(args, "-O", t)
		}
		return args
	}
	err := r.run(ctx, "get_transcript", build, target, func(dir string, stdout []byte, stderr string) error {
		lines, err := expectLines(stdout, len(templates))
		if err != nil {
			return err
		}
		if err := decodeInfo(lines[0], &out.Info, stderr); err != nil {
			return err
		}
		out.Tracks.Manual = zipTracks(lines[1], "")
		out.Tracks.Automatic = zipTracks(lines[2], "")

		path, format, unsupported, err := findSubtitleFile(dir, lang)
		if err != nil {
			return err
		}
		if unsupported != "" {
			return &Error{Kind: KindParse, Msg: unsupportedMessage(lang, unsupported, src, out.Tracks)}
		}
		if path == "" {
			return &Error{Kind: KindNoSubtitles, Msg: noSubtitlesMessage(lang, src, out.Tracks)}
		}
		st, err := os.Stat(path)
		if err != nil {
			return &Error{Kind: KindParse, Msg: fmt.Sprintf("reading the subtitle file: %v", err), Err: err}
		}
		if st.Size() > r.cfg.MaxOutputBytes {
			return &Error{Kind: KindOutputLimit, Msg: fmt.Sprintf("the %s subtitle track is %d bytes, larger than the %d-byte limit (YTDLP_MCP_MAX_OUTPUT_BYTES)", lang, st.Size(), r.cfg.MaxOutputBytes)}
		}
		data, err := os.ReadFile(path) // #nosec G304 -- path is inside the scratch dir this call created
		if err != nil {
			return &Error{Kind: KindParse, Msg: fmt.Sprintf("reading the subtitle file: %v", err), Err: err}
		}
		out.Lang = lang
		out.Format = format
		out.Data = data
		switch src {
		case SourceManual:
			out.Automatic = false
		case SourceAutomatic:
			out.Automatic = true
		default:
			out.Automatic = !slices.Contains(codes(out.Tracks.Manual), lang)
		}
		return nil
	})
	return out, err
}

// findSubtitleFile locates transcriptBase.<lang>.<ext> in dir for a
// supported ext. Failing that it returns the extension of an unsupported
// file yt-dlp wrote instead (yt-dlp falls back to the track's best format
// when none of --sub-format is offered), or nothing at all when yt-dlp wrote
// no file (the language does not exist for that source).
func findSubtitleFile(dir, lang string) (path string, format transcript.Format, unsupported string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", "", &Error{Kind: KindParse, Msg: fmt.Sprintf("listing the scratch directory: %v", err), Err: err}
	}
	prefix := transcriptBase + "." + lang + "."
	var found []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), prefix) {
			found = append(found, e.Name())
		}
	}
	for _, f := range transcript.Formats {
		name := prefix + string(f)
		if slices.Contains(found, name) {
			return filepath.Join(dir, name), f, "", nil
		}
	}
	if len(found) > 0 {
		return "", "", strings.TrimPrefix(found[0], prefix), nil
	}
	return "", "", "", nil
}

// unsupportedMessage explains that the chosen track came only in a format
// the server cannot convert. A manual track is preferred even then, since
// yt-dlp picks the manual track before looking at formats; when automatic
// captions exist in the same language the message points at source="auto"
// instead of the server silently re-running yt-dlp for a machine track.
func unsupportedMessage(lang, ext string, src Source, t Tracks) string {
	msg := fmt.Sprintf("the %s track is only available as %q, which this server cannot convert (supported: json3, vtt, srt)", lang, ext)
	if src != SourceAutomatic && slices.Contains(codes(t.Manual), lang) && slices.Contains(codes(t.Automatic), lang) {
		msg += fmt.Sprintf("; automatic captions exist in %s, so retry with source=\"auto\"", lang)
	}
	return msg
}

// noSubtitlesMessage tells the agent what does exist.
func noSubtitlesMessage(lang string, src Source, t Tracks) string {
	kind := ""
	switch src {
	case SourceManual:
		kind = "manual "
	case SourceAutomatic:
		kind = "automatic "
	}
	manual := "none"
	if len(t.Manual) > 0 {
		manual = strings.Join(codes(t.Manual), ", ")
	}
	auto := "none"
	if len(t.Automatic) > 0 {
		auto = summarizeAutomatic(t)
	}
	return fmt.Sprintf("no %ssubtitles in language %q; available manual: %s; auto: %s (call list_subtitles for names)", kind, lang, manual, auto)
}

// summarizeAutomatic lists the original-language track first and caps the
// list, since YouTube offers ~150 machine-translated languages.
func summarizeAutomatic(t Tracks) string {
	const show = 12
	all := codes(t.Automatic)
	if orig := t.OriginalAutomatic(); orig != "" {
		all = slices.DeleteFunc(all, func(c string) bool { return c == orig })
		all = append([]string{orig}, all...)
	}
	if len(all) <= show {
		return strings.Join(all, ", ")
	}
	return fmt.Sprintf("%s, … (%d more)", strings.Join(all[:show], ", "), len(all)-show)
}

func printArgs(templates []string) func(string) []string {
	return func(string) []string {
		args := slices.Clone(singleVideoArgs)
		for _, t := range templates {
			args = append(args, "-O", t)
		}
		return args
	}
}

// outputLines splits stdout into lines without dropping empty ones (an empty
// subtitle dict prints an empty line that still counts).
func outputLines(stdout []byte) []string {
	s := strings.ReplaceAll(string(stdout), "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

func expectLines(stdout []byte, n int) ([]string, error) {
	lines := outputLines(stdout)
	if len(lines) < n {
		return nil, &Error{Kind: KindParse, Msg: fmt.Sprintf("yt-dlp printed %d line(s), expected %d; the URL may not point at a single video", len(lines), n)}
	}
	return lines[:n], nil
}

// decodeInfo parses the metadata line. A record with no id, or with neither
// a title of the extractor's own nor a channel or uploader, is yt-dlp's
// placeholder for a video it could not load (removed, private, never
// existed): that becomes a KindUnavailable error carrying yt-dlp's reason
// from stderr. A real video keeps its metadata even when yt-dlp warned about
// it (a bot check, an upcoming premiere).
func decodeInfo(line string, info *VideoInfo, stderr string) error {
	if line == "" || line == "NA" {
		return &Error{Kind: KindParse, Msg: "yt-dlp returned no metadata for this URL"}
	}
	if err := json.Unmarshal([]byte(line), info); err != nil {
		return &Error{Kind: KindParse, Msg: fmt.Sprintf("could not parse yt-dlp metadata: %v", err), Err: err}
	}
	if info.ID == "" || (info.FullTitle == "" && info.Channel == "" && info.Uploader == "") {
		video := "the video"
		if info.ID != "" {
			video = fmt.Sprintf("video %q", info.ID)
		}
		return &Error{Kind: KindUnavailable, Msg: fmt.Sprintf("%s is unavailable: %s", video, unavailableReason(stderr, info.ID))}
	}
	return nil
}

// unavailableReason picks yt-dlp's explanation for a video it could not
// load: the last ERROR line, else the last extractor warning ("WARNING:
// [youtube] This video is unavailable"), with the level, the extractor tag
// and a leading "<id>: " removed.
func unavailableReason(stderr, id string) string {
	var lastErr, lastWarn string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if rest, ok := strings.CutPrefix(l, "ERROR:"); ok {
			lastErr = rest
		} else if rest, ok := strings.CutPrefix(l, "WARNING: ["); ok && strings.Contains(rest, "] ") {
			lastWarn = "[" + rest
		}
	}
	msg := lastErr
	if msg == "" {
		msg = lastWarn
	}
	msg = strings.TrimSpace(msg)
	if strings.HasPrefix(msg, "[") {
		if _, rest, ok := strings.Cut(msg, "] "); ok {
			msg = rest
		}
	}
	if id != "" {
		msg = strings.TrimPrefix(msg, id+": ")
	}
	if msg = strings.TrimSpace(msg); msg == "" {
		return "yt-dlp found no title, channel or other metadata for it; it may be private, removed or never have existed"
	}
	const maxLen = 600
	if len(msg) > maxLen {
		msg = strings.ToValidUTF8(msg[:maxLen], "") + "…"
	}
	return msg
}

// zipTracks pairs a ", "-joined key line with an optional JSON list of
// {name, ext} objects in the same order. Names are dropped (codes kept) if
// the two lists disagree in length.
func zipTracks(keysLine, namesLine string) []Track {
	if keysLine == "" || keysLine == "NA" {
		return nil
	}
	keys := strings.Split(keysLine, ", ")
	var names []struct {
		Name string `json:"name"`
	}
	if namesLine != "" && namesLine != "NA" {
		if err := json.Unmarshal([]byte(namesLine), &names); err != nil || len(names) != len(keys) {
			names = nil
		}
	}
	out := make([]Track, 0, len(keys))
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		t := Track{Code: k}
		if names != nil {
			t.Name = names[i].Name
		}
		out = append(out, t)
	}
	return out
}
