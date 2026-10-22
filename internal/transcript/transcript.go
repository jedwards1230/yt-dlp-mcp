// Package transcript turns subtitle files written by yt-dlp into clean,
// de-duplicated plain-text segments and renders bounded pages of them.
//
// Supported inputs are YouTube's json3 timed-text format, WebVTT and SubRip
// (SRT). Every parser yields one [Segment] per cue, with markup, inline word
// timestamps and HTML entities removed and whitespace collapsed.
//
// Automatic (speech-recognition) tracks are additionally passed through
// [Clean] with Rolling set: YouTube's auto-caption VTT repeats each line in
// the following cue (the "rolling" two-line display), and some sources grow
// a line word by word across cues. Rolling cleanup drops a line equal to the
// previous one and folds a line that merely extends the previous one into
// it. Manual tracks are left alone apart from dropping exact repeats at the
// same timestamp, so a genuinely repeated line (a song chorus) survives.
package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Segment is one caption cue. Straight out of [Parse], Text may hold several
// lines separated by "\n"; after [Clean] it is always a single line.
type Segment struct {
	Start time.Duration
	Text  string
}

// Format identifies a subtitle file format by its yt-dlp extension.
type Format string

// Supported formats, in the server's preference order.
const (
	FormatJSON3 Format = "json3"
	FormatVTT   Format = "vtt"
	FormatSRT   Format = "srt"
)

// Formats is the preference order passed to yt-dlp's --sub-format.
var Formats = []Format{FormatJSON3, FormatVTT, FormatSRT}

// Parse decodes a subtitle file of the given format into segments.
func Parse(data []byte, f Format) ([]Segment, error) {
	switch f {
	case FormatJSON3:
		return parseJSON3(data)
	case FormatVTT, FormatSRT:
		return parseCues(data), nil
	default:
		return nil, fmt.Errorf("subtitle format %q is not supported (supported: json3, vtt, srt)", f)
	}
}

type json3Doc struct {
	Events []struct {
		TStartMs int64 `json:"tStartMs"`
		AAppend  int   `json:"aAppend"`
		Segs     []struct {
			UTF8 string `json:"utf8"`
		} `json:"segs"`
	} `json:"events"`
}

func parseJSON3(data []byte) ([]Segment, error) {
	var doc json3Doc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decoding json3 subtitles: %w", err)
	}
	segs := make([]Segment, 0, len(doc.Events))
	for _, ev := range doc.Events {
		if len(ev.Segs) == 0 {
			continue // window/pen definitions carry no text
		}
		var b strings.Builder
		for _, s := range ev.Segs {
			b.WriteString(s.UTF8)
		}
		text := normalizeLines(strings.Split(b.String(), "\n"), false)
		if text == "" {
			continue // aAppend line-break events are a lone "\n"
		}
		segs = append(segs, Segment{Start: msToDuration(ev.TStartMs), Text: text})
	}
	return segs, nil
}

// msToDuration converts a json3 start time. A negative value, or one too
// large for a time.Duration, is clamped to 0 instead of wrapping around.
func msToDuration(ms int64) time.Duration {
	if ms < 0 || ms > math.MaxInt64/int64(time.Millisecond) {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

var (
	tagRe        = regexp.MustCompile(`<[^>]*>`)
	assOverrides = regexp.MustCompile(`\{\\[^}]*\}`)
	spaceRe      = regexp.MustCompile(`\s+`)
)

// normalizeLines normalizes each caption line and joins the non-empty ones
// with "\n". [Clean] later either joins them with spaces (manual) or splits
// them apart for de-duplication (automatic).
func normalizeLines(lines []string, markup bool) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if t := normalizeText(l, markup); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

// normalizeText collapses whitespace and drops invisible direction marks.
// With markup set (VTT/SRT payloads) it first strips tags, inline word
// timestamps and ASS override blocks and decodes HTML entities; json3 text is
// already plain, so a literal "<" there is content and is kept.
func normalizeText(s string, markup bool) string {
	if markup {
		s = tagRe.ReplaceAllString(s, "")
		s = assOverrides.ReplaceAllString(s, "")
		s = html.UnescapeString(s)
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\u200e', '\u200f', '\ufeff': // LRM, RLM, BOM
			return -1
		case '\u00a0':
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// parseCues handles WebVTT and SRT. A cue starts at a "-->" timing line;
// its payload lines become one segment, joined with "\n". Lines outside a
// cue (the WEBVTT header, NOTE/STYLE/REGION blocks, cue identifiers, stray
// text) are skipped, and so is a cue whose start time does not parse.
//
// A cue ends at an empty line or at the next timing line. A whitespace-only
// line is payload (YouTube's rolling auto-captions put one in the empty
// "previous line" slot of a cue, and normalizing drops it), except when the
// next timing line follows it directly or after one identifier line: then it
// is a separator with a stray space or tab in it, and the identifier belongs
// to the next cue. So two cues never merge and neither a timing line nor a
// cue number leaks into caption text.
func parseCues(data []byte) []Segment {
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	isTiming := func(i int) bool { return i < len(lines) && strings.Contains(lines[i], "-->") }
	isBlank := func(i int) bool { return i < len(lines) && strings.TrimSpace(lines[i]) == "" }
	var (
		segs    []Segment
		inCue   bool
		start   time.Duration
		payload []string
	)
	flush := func() {
		if inCue {
			if t := normalizeLines(payload, true); t != "" {
				segs = append(segs, Segment{Start: start, Text: t})
			}
		}
		inCue, payload = false, payload[:0]
	}
	for i, line := range lines {
		switch {
		case line == "":
			flush()
		case isTiming(i):
			flush()
			start, inCue = parseTimestamp(strings.TrimSpace(strings.SplitN(line, "-->", 2)[0]))
		case isBlank(i) && (isTiming(i+1) || (i+1 < len(lines) && !isBlank(i+1) && isTiming(i+2))):
			flush()
		case inCue:
			payload = append(payload, line)
		}
	}
	flush()
	return segs
}

// maxHours is the largest hour count a time.Duration holds with room for
// the minutes and seconds that follow it.
const maxHours = math.MaxInt64/int64(time.Hour) - 1

// parseTimestamp accepts "hh:mm:ss.ttt", "mm:ss.ttt" and the SRT comma form.
// An hour count too large for a time.Duration is clamped to 0, like an
// out-of-range json3 start time, rather than wrapping around.
func parseTimestamp(s string) (time.Duration, bool) {
	s = strings.ReplaceAll(s, ",", ".")
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var h, m int
	var err error
	if len(parts) == 3 {
		if h, err = strconv.Atoi(parts[0]); err != nil || h < 0 {
			return 0, false
		}
		parts = parts[1:]
	}
	if m, err = strconv.Atoi(parts[0]); err != nil || m < 0 || m > 59 {
		return 0, false
	}
	sec, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || sec < 0 || sec >= 60 {
		return 0, false
	}
	if int64(h) > maxHours {
		return 0, true
	}
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second))
	return d.Round(time.Millisecond), true
}
