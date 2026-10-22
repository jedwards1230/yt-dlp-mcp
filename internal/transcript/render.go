package transcript

import (
	"fmt"
	"strings"
	"time"
)

// Clean turns parsed cues into single-line segments and drops duplicates.
//
// With rolling set (automatic captions) every cue line becomes its own
// segment at the cue's start time; a line equal to the one before it is
// dropped, and a line that extends the one before it ("so" then "so we")
// replaces its text while keeping the earlier start time. Without rolling
// (manual captions) a cue's lines are joined with spaces and only a repeat of
// the same text at the same timestamp is dropped.
func Clean(segs []Segment, rolling bool) []Segment {
	lines := make([]Segment, 0, len(segs))
	for _, s := range segs {
		if rolling {
			for _, l := range strings.Split(s.Text, "\n") {
				lines = append(lines, Segment{Start: s.Start, Text: strings.TrimSpace(l)})
			}
			continue
		}
		lines = append(lines, Segment{Start: s.Start, Text: strings.TrimSpace(strings.ReplaceAll(s.Text, "\n", " "))})
	}

	out := make([]Segment, 0, len(lines))
	for _, s := range lines {
		if s.Text == "" {
			continue
		}
		if n := len(out); n > 0 {
			last := &out[n-1]
			if rolling {
				if s.Text == last.Text {
					continue
				}
				if strings.HasPrefix(s.Text, last.Text+" ") {
					last.Text = s.Text
					continue
				}
			} else if s.Text == last.Text && s.Start == last.Start {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// Page is one bounded slice of a transcript.
type Page struct {
	// Text is the page's segments, one per line, optionally prefixed with a
	// "[mm:ss]" or "[hh:mm:ss]" start timestamp.
	Text string
	// Offset is the index of the first segment on the page.
	Offset int
	// Returned is the number of segments on the page.
	Returned int
	// Total is the number of segments in the whole transcript.
	Total int
	// NextOffset is the offset to request for the following page; it equals
	// Total once the transcript is exhausted.
	NextOffset int
	// HasMore reports whether segments remain after this page.
	HasMore bool
}

// Paginate renders segments[offset:] until limit segments or maxChars
// characters are reached, whichever comes first. A page always holds at least
// one segment when any remain, so a single oversized line cannot stall
// pagination. An offset past the end is an error that names the valid range.
func Paginate(segs []Segment, offset, limit, maxChars int, timestamps bool) (Page, error) {
	total := len(segs)
	if offset < 0 {
		return Page{}, fmt.Errorf("offset %d is negative; start at 0", offset)
	}
	if offset > total || (offset == total && total > 0) {
		return Page{}, fmt.Errorf("offset %d is past the end of the transcript (%d segments; valid offsets are 0-%d)", offset, total, max(total-1, 0))
	}
	if limit < 1 {
		limit = 1
	}

	var b strings.Builder
	i := offset
	for ; i < total && i-offset < limit; i++ {
		line := segs[i].Text
		if timestamps {
			line = "[" + FormatTimestamp(segs[i].Start) + "] " + line
		}
		extra := len(line)
		if b.Len() > 0 {
			extra++ // newline separator
		}
		if b.Len() > 0 && b.Len()+extra > maxChars {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return Page{
		Text:       b.String(),
		Offset:     offset,
		Returned:   i - offset,
		Total:      total,
		NextOffset: i,
		HasMore:    i < total,
	}, nil
}

// FormatTimestamp renders d as "mm:ss", or "hh:mm:ss" from one hour on.
func FormatTimestamp(d time.Duration) string {
	sec := int64(d / time.Second)
	h, m, s := sec/3600, (sec/60)%60, sec%60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// FormatDuration renders whole seconds the way yt-dlp's duration_string does:
// "m:ss" or "h:mm:ss".
func FormatDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	h, m, s := seconds/3600, (seconds/60)%60, seconds%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
