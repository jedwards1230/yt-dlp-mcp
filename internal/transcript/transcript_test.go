package transcript

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden files from the current output")

// TestGolden converts every fixture under testdata/ and compares the full
// rendered transcript, with and without timestamps, to its golden file.
// Fixtures named auto.* are automatic captions and get rolling cleanup.
// Regenerate with: go test ./internal/transcript -run TestGolden -update
func TestGolden(t *testing.T) {
	fixtures := []struct {
		file    string
		format  Format
		rolling bool
	}{
		{"manual.json3", FormatJSON3, false},
		{"auto.json3", FormatJSON3, true},
		{"manual.vtt", FormatVTT, false},
		{"auto.vtt", FormatVTT, true},
		{"manual.srt", FormatSRT, false},
	}
	for _, fx := range fixtures {
		for _, ts := range []bool{false, true} {
			golden := fx.file + ".golden"
			if ts {
				golden = fx.file + ".timestamps.golden"
			}
			t.Run(golden, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join("testdata", fx.file))
				if err != nil {
					t.Fatal(err)
				}
				segs, err := Parse(data, fx.format)
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				segs = Clean(segs, fx.rolling)
				page, err := Paginate(segs, 0, len(segs), 1<<20, ts)
				if err != nil {
					t.Fatalf("Paginate: %v", err)
				}
				if page.HasMore || page.NextOffset != len(segs) {
					t.Fatalf("full render left segments behind: %+v", page)
				}
				got := page.Text + "\n"
				path := filepath.Join("testdata", golden)
				if *update {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("reading golden (run with -update to create it): %v", err)
				}
				if got != string(want) {
					t.Errorf("output differs from %s\n--- got ---\n%s--- want ---\n%s", path, got, want)
				}
			})
		}
	}
}

// TestRollingAutoCaptionsDeduplicated pins the property the goldens exist
// for: no line of a rolling auto-caption track is emitted twice in a row and
// no inline word timestamps or tags survive.
func TestRollingAutoCaptionsDeduplicated(t *testing.T) {
	for _, f := range []struct {
		file   string
		format Format
	}{{"auto.vtt", FormatVTT}, {"auto.json3", FormatJSON3}} {
		t.Run(f.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", f.file))
			if err != nil {
				t.Fatal(err)
			}
			segs, err := Parse(data, f.format)
			if err != nil {
				t.Fatal(err)
			}
			segs = Clean(segs, true)
			want := []string{
				"[Music]",
				"okay so today",
				"we're going to build a bookshelf",
				"um measure twice cut once",
			}
			for i, w := range want {
				if i >= len(segs) || segs[i].Text != w {
					t.Fatalf("segment %d = %+v, want %q (all: %+v)", i, segs, w, segs)
				}
			}
			for i, s := range segs {
				if strings.ContainsAny(s.Text, "<>") && !strings.HasPrefix(s.Text, ">>") {
					t.Errorf("segment %d still has markup: %q", i, s.Text)
				}
				if i > 0 && segs[i-1].Text == s.Text {
					t.Errorf("segment %d duplicates its predecessor: %q", i, s.Text)
				}
			}
		})
	}
}

func TestCleanRules(t *testing.T) {
	s := func(sec int, text string) Segment {
		return Segment{Start: time.Duration(sec) * time.Second, Text: text}
	}
	tests := []struct {
		name    string
		in      []Segment
		rolling bool
		want    []Segment
	}{
		{"rolling drops repeat", []Segment{s(1, "a b"), s(2, "a b"), s(3, "c")}, true, []Segment{s(1, "a b"), s(3, "c")}},
		{"rolling folds growth keeping first start", []Segment{s(1, "so"), s(2, "so we"), s(3, "so we go")}, true, []Segment{s(1, "so we go")}},
		{"rolling needs a word boundary to fold", []Segment{s(1, "go"), s(2, "gone")}, true, []Segment{s(1, "go"), s(2, "gone")}},
		{"manual keeps a chorus repeated later", []Segment{s(1, "la la"), s(2, "la la")}, false, []Segment{s(1, "la la"), s(2, "la la")}},
		{"manual drops same-time duplicate", []Segment{s(1, "la la"), s(1, "la la")}, false, []Segment{s(1, "la la")}},
		{"empty dropped", []Segment{s(1, ""), s(2, "x")}, false, []Segment{s(2, "x")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Clean(tt.in, tt.rolling)
			if len(got) != len(tt.want) {
				t.Fatalf("Clean = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Clean = %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

func TestPaginate(t *testing.T) {
	segs := []Segment{
		{0, "alpha"}, {65 * time.Second, "bravo"}, {2 * time.Hour, "charlie"}, {2*time.Hour + time.Second, "delta"},
	}
	tests := []struct {
		name                    string
		offset, limit, maxChars int
		ts                      bool
		wantText                string
		wantNext, wantReturned  int
		wantMore                bool
		wantErr                 string
	}{
		{name: "first page by limit", offset: 0, limit: 2, maxChars: 1000, wantText: "alpha\nbravo", wantNext: 2, wantReturned: 2, wantMore: true},
		{name: "last page", offset: 2, limit: 10, maxChars: 1000, wantText: "charlie\ndelta", wantNext: 4, wantReturned: 2},
		{name: "char budget stops early", offset: 0, limit: 10, maxChars: 11, wantText: "alpha\nbravo", wantNext: 2, wantReturned: 2, wantMore: true},
		{name: "oversized single line still returned", offset: 0, limit: 10, maxChars: 3, wantText: "alpha", wantNext: 1, wantReturned: 1, wantMore: true},
		{name: "timestamps switch to hours", offset: 1, limit: 2, maxChars: 1000, ts: true, wantText: "[01:05] bravo\n[02:00:00] charlie", wantNext: 3, wantReturned: 2, wantMore: true},
		{name: "offset at end", offset: 4, limit: 1, maxChars: 1000, wantErr: "past the end of the transcript (4 segments; valid offsets are 0-3)"},
		{name: "negative offset", offset: -1, limit: 1, maxChars: 1000, wantErr: "negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Paginate(segs, tt.offset, tt.limit, tt.maxChars, tt.ts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Text != tt.wantText || p.NextOffset != tt.wantNext || p.Returned != tt.wantReturned || p.HasMore != tt.wantMore || p.Total != len(segs) || p.Offset != tt.offset {
				t.Fatalf("Paginate = %+v, want text %q next %d returned %d more %v", p, tt.wantText, tt.wantNext, tt.wantReturned, tt.wantMore)
			}
		})
	}
}

// TestParseCueSeparators pins how VTT and SRT cues are split: any blank-
// looking line (spaces, tabs, CR) ends a cue, as does the next timing line,
// so two cues never merge and a timing line never leaks into caption text.
func TestParseCueSeparators(t *testing.T) {
	seg := func(ms int, text string) Segment {
		return Segment{Start: time.Duration(ms) * time.Millisecond, Text: text}
	}
	two := []Segment{seg(1000, "first cue"), seg(3000, "second cue")}
	tests := []struct {
		name   string
		format Format
		in     string
		want   []Segment
	}{
		{"vtt space-only separator", FormatVTT,
			"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst cue\n \n00:00:03.000 --> 00:00:04.000\nsecond cue\n", two},
		{"vtt tab-only separator", FormatVTT,
			"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst cue\n\t\n00:00:03.000 --> 00:00:04.000\nsecond cue\n", two},
		{"vtt crlf with trailing-space blank lines", FormatVTT,
			"WEBVTT\r\n  \r\n00:00:01.000 --> 00:00:02.000\r\nfirst cue\r\n   \r\n00:00:03.000 --> 00:00:04.000\r\nsecond cue\r\n", two},
		{"vtt no separator before the next timing line", FormatVTT,
			"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst cue\n00:00:03.000 --> 00:00:04.000\nsecond cue\n", two},
		{"vtt cue identifiers and note skipped", FormatVTT,
			"WEBVTT\n\nNOTE a comment\n \nintro\n00:00:01.000 --> 00:00:02.000 align:start\nfirst cue\n\t \nc2\n00:00:03.000 --> 00:00:04.000\nsecond cue", two},
		{"vtt whitespace-only payload line is not a separator", FormatVTT,
			"WEBVTT\n\n00:00:01.000 --> 00:00:02.000 align:start position:0%\n \nfirst cue\n\n00:00:02.500 --> 00:00:02.510\n \n \n\n00:00:03.000 --> 00:00:04.000\nsecond cue\n \n", two},
		{"vtt multi-line cue kept together", FormatVTT,
			"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst\ncue\n \n00:00:03.000 --> 00:00:04.000\nsecond cue\n",
			[]Segment{seg(1000, "first\ncue"), seg(3000, "second cue")}},
		{"vtt unparsable timing skips only its cue", FormatVTT,
			"WEBVTT\n\nxx:yy --> 00:00:02.000\nlost\n \n00:00:03.000 --> 00:00:04.000\nsecond cue\n",
			[]Segment{seg(3000, "second cue")}},
		{"srt space-only separator", FormatSRT,
			"1\n00:00:01,000 --> 00:00:02,000\nfirst cue\n \n2\n00:00:03,000 --> 00:00:04,000\nsecond cue\n", two},
		{"srt crlf with trailing-space blank lines", FormatSRT,
			"1\r\n00:00:01,000 --> 00:00:02,000\r\nfirst cue\r\n \t \r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nsecond cue\r\n \r\n", two},
		{"srt bare cr line endings", FormatSRT,
			"1\r00:00:01,000 --> 00:00:02,000\rfirst cue\r \r2\r00:00:03,000 --> 00:00:04,000\rsecond cue\r", two},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.in), tt.format)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Parse = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte("not json"), FormatJSON3); err == nil {
		t.Error("invalid json3 accepted")
	}
	if _, err := Parse([]byte("x"), Format("ttml")); err == nil || !strings.Contains(err.Error(), `"ttml" is not supported`) {
		t.Errorf("ttml err = %v", err)
	}
	if segs, err := Parse([]byte("WEBVTT\n\nnot a cue\n"), FormatVTT); err != nil || len(segs) != 0 {
		t.Errorf("cue-less vtt = %v, %v", segs, err)
	}
}

func TestFormatTimestamp(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want string
	}{
		{0, "00:00"},
		{59*time.Second + 900*time.Millisecond, "00:59"},
		{65 * time.Second, "01:05"},
		{2 * time.Hour, "02:00:00"},
		{-1500 * time.Millisecond, "00:00"},
		{-2 * time.Hour, "00:00"},
		{math.MinInt64, "00:00"},
		{math.MaxInt64, "2562047:47:16"},
	} {
		if got := FormatTimestamp(tt.in); got != tt.want {
			t.Errorf("FormatTimestamp(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestStartTimesClamped feeds start times that are negative or too large
// for a time.Duration: they become 0 instead of wrapping into nonsense, and
// the cue text survives.
func TestStartTimesClamped(t *testing.T) {
	tests := []struct {
		name   string
		format Format
		in     string
		want   []time.Duration
	}{
		{"json3 negative and overflowing", FormatJSON3,
			fmt.Sprintf(`{"events":[{"tStartMs":-1500,"segs":[{"utf8":"a"}]},{"tStartMs":%d,"segs":[{"utf8":"b"}]},{"tStartMs":%d,"segs":[{"utf8":"c"}]},{"tStartMs":2500,"segs":[{"utf8":"d"}]}]}`,
				int64(math.MaxInt64), math.MaxInt64/int64(time.Millisecond)+1),
			[]time.Duration{0, 0, 0, 2500 * time.Millisecond}},
		{"vtt hour count past a Duration", FormatVTT,
			"WEBVTT\n\n9999999:00:00.000 --> 9999999:00:01.000\na\n\n2562046:00:00.000 --> 2562046:00:01.000\nb\n",
			[]time.Duration{0, 2562046 * time.Hour}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segs, err := Parse([]byte(tt.in), tt.format)
			if err != nil {
				t.Fatal(err)
			}
			if len(segs) != len(tt.want) {
				t.Fatalf("got %d segments, want %d: %+v", len(segs), len(tt.want), segs)
			}
			for i, s := range segs {
				if s.Start != tt.want[i] {
					t.Errorf("segment %d (%q) starts at %d, want %d", i, s.Text, s.Start, tt.want[i])
				}
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	for in, want := range map[int64]string{0: "0:00", 59: "0:59", 213: "3:33", 3600: "1:00:00", 3723: "1:02:03", -5: "0:00"} {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}
