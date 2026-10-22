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
		segs = append(segs, Segment{Start: time.Duration(ev.TStartMs) * time.Millisecond, Text: text})
	}
	return segs, nil
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

// parseCues handles WebVTT and SRT: blank-line separated blocks, where a
// block containing a "-->" timing line is a cue and everything else (the
// WEBVTT header, NOTE/STYLE/REGION blocks, stray text) is skipped. A cue's
// non-empty payload lines become one segment, joined with "\n".
func parseCues(data []byte) []Segment {
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var segs []Segment
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(block, "\n")
		timing := -1
		for i, l := range lines {
			if strings.Contains(l, "-->") {
				timing = i
				break
			}
		}
		if timing < 0 {
			continue
		}
		start, ok := parseTimestamp(strings.TrimSpace(strings.SplitN(lines[timing], "-->", 2)[0]))
		if !ok {
			continue
		}
		if t := normalizeLines(lines[timing+1:], true); t != "" {
			segs = append(segs, Segment{Start: start, Text: t})
		}
	}
	return segs
}

// parseTimestamp accepts "hh:mm:ss.ttt", "mm:ss.ttt" and the SRT comma form.
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
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second))
	return d.Round(time.Millisecond), true
}
