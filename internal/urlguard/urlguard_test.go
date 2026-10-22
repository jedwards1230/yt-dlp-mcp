package urlguard

import (
	"strings"
	"testing"
)

func defaultAllowlist(t *testing.T) *Allowlist {
	t.Helper()
	a, err := NewAllowlist(DefaultHosts)
	if err != nil {
		t.Fatalf("NewAllowlist(DefaultHosts): %v", err)
	}
	return a
}

const watchURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

// TestResolveAccepts covers every accepted input shape and the canonical URL
// it must become.
func TestResolveAccepts(t *testing.T) {
	a := defaultAllowlist(t)
	tests := []struct {
		name, in, wantURL, wantID string
	}{
		{"bare id", "dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"bare id with dash and underscore", "a-b_c-d_e-f", "https://www.youtube.com/watch?v=a-b_c-d_e-f", "a-b_c-d_e-f"},
		{"bare id leading dash is still an id", "-abcdefghij", "https://www.youtube.com/watch?v=-abcdefghij", "-abcdefghij"},
		{"surrounding whitespace trimmed", "  " + watchURL + "\n", watchURL, "dQw4w9WgXcQ"},
		{"watch url", watchURL, watchURL, "dQw4w9WgXcQ"},
		{"watch url extra params dropped", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s&list=PL123", watchURL, "dQw4w9WgXcQ"},
		{"uppercase scheme and host", "HTTPS://WWW.YOUTUBE.COM/watch?v=dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"http scheme", "http://youtube.com/watch?v=dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"explicit default https port", "https://www.youtube.com:443/watch?v=dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"explicit default http port", "http://www.youtube.com:80/watch?v=dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"mobile host", "https://m.youtube.com/watch?v=dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"music host", "https://music.youtube.com/watch?v=dQw4w9WgXcQ&si=x", watchURL, "dQw4w9WgXcQ"},
		{"short link", "https://youtu.be/dQw4w9WgXcQ?si=abc", watchURL, "dQw4w9WgXcQ"},
		{"shorts", "https://www.youtube.com/shorts/dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"embed", "https://www.youtube.com/embed/dQw4w9WgXcQ?start=3", watchURL, "dQw4w9WgXcQ"},
		{"nocookie embed", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"live", "https://www.youtube.com/live/dQw4w9WgXcQ", watchURL, "dQw4w9WgXcQ"},
		{"fragment dropped", "https://www.youtube.com/watch?v=dQw4w9WgXcQ#t=1", watchURL, "dQw4w9WgXcQ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.Resolve(tt.in)
			if err != nil {
				t.Fatalf("Resolve(%q) error: %v", tt.in, err)
			}
			if got.URL != tt.wantURL || got.VideoID != tt.wantID || got.Host != CanonicalYouTubeHost {
				t.Fatalf("Resolve(%q) = %+v, want URL %q id %q", tt.in, got, tt.wantURL, tt.wantID)
			}
		})
	}
}

// TestResolveRejects is the SSRF table: every input here must be refused
// before any process runs, with a message naming the problem.
func TestResolveRejects(t *testing.T) {
	a := defaultAllowlist(t)
	tests := []struct {
		name, in, wantSubstr string
	}{
		{"empty", "", "url is required"},
		{"blank", "   ", "url is required"},
		{"too long", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&x=" + strings.Repeat("a", MaxURLLength), "limit is"},
		{"ten char id", "dQw4w9WgXc", "not an http(s) URL"},
		{"twelve char id", "dQw4w9WgXcQQ", "not an http(s) URL"},
		{"flag-looking input", "--exec=id", "not an http(s) URL"},
		{"ytsearch pseudo url", "ytsearch5:secret", "not an http(s) URL"},
		{"ytsearch with slashes", "ytsearch://www.youtube.com/", "scheme \"ytsearch\" is not allowed"},
		{"file scheme", "file:///etc/passwd", "scheme \"file\" is not allowed"},
		{"ftp scheme", "ftp://www.youtube.com/watch?v=dQw4w9WgXcQ", "scheme \"ftp\" is not allowed"},
		{"javascript scheme", "javascript://www.youtube.com/%0aalert(1)", "scheme \"javascript\" is not allowed"},
		{"data scheme", "data:text/plain,hi", "not an http(s) URL"},
		{"scheme relative", "//www.youtube.com/watch?v=dQw4w9WgXcQ", "not an http(s) URL"},
		{"no scheme", "www.youtube.com/watch?v=dQw4w9WgXcQ", "not an http(s) URL"},
		{"single slash", "https:/www.youtube.com/watch?v=dQw4w9WgXcQ", "not an http(s) URL"},
		{"triple slash empty host", "https:///watch?v=dQw4w9WgXcQ", "not a plain hostname"},
		{"other host", "https://example.com/video", "host \"example.com\" is not allowed; allowed hosts: m.youtube.com"},
		{"suffix trick", "https://www.youtube.com.evil.example/watch?v=dQw4w9WgXcQ", "is not allowed"},
		{"prefix trick", "https://evilyoutube.com/watch?v=dQw4w9WgXcQ", "is not allowed"},
		{"subdomain not implied", "https://gaming.youtube.com/watch?v=dQw4w9WgXcQ", "is not allowed"},
		{"userinfo", "https://www.youtube.com@evil.example/", "username or password"},
		{"userinfo with password", "https://user:pass@www.youtube.com/watch?v=dQw4w9WgXcQ", "username or password"},
		{"userinfo in allowed host", "https://evil.example@www.youtube.com/watch?v=dQw4w9WgXcQ", "username or password"},
		{"fragment hides host", "https://evil.example#@www.youtube.com/", "is not allowed"},
		{"backslash authority", "https://www.youtube.com\\@evil.example/", "backslash"},
		{"embedded space", "https://www.youtube.com/watch?v=dQw4w9WgXcQ &--exec", "whitespace"},
		{"embedded newline", "https://www.youtube.com/watch?v=dQw4w9WgXcQ\nhttps://evil.example", "whitespace"},
		{"nul byte", "https://www.youtube.com/\x00", "control characters"},
		{"non default port", "https://www.youtube.com:8443/watch?v=dQw4w9WgXcQ", "port 8443 is not allowed"},
		{"https on port 80", "https://www.youtube.com:80/watch?v=dQw4w9WgXcQ", "port 80 is not allowed"},
		{"empty port", "https://www.youtube.com:/watch?v=dQw4w9WgXcQ", "not a plain hostname"},
		{"trailing dot", "https://www.youtube.com./watch?v=dQw4w9WgXcQ", "ends with a dot"},
		{"ipv4 literal", "http://127.0.0.1/", "IP address"},
		{"ipv4 metadata", "http://169.254.169.254/latest/meta-data/", "IP address"},
		{"ipv4 shorthand", "http://127.1/", "IP address"},
		{"ipv4 decimal", "http://2130706433/", "IP address"},
		{"ipv4 hex", "http://0x7f.0.0.1/", "IP address"},
		{"ipv6 literal", "http://[::1]/", "not a plain hostname"},
		{"ipv6 mapped", "http://[::ffff:127.0.0.1]:80/", "not a plain hostname"},
		{"percent encoded host", "https://%77ww.youtube.com/watch?v=dQw4w9WgXcQ", "not a plain hostname"},
		{"idn homograph", "https://www.yоutube.com/watch?v=dQw4w9WgXcQ", "not a plain hostname"},
		{"fullwidth dot", "https://www.youtube。com/watch?v=dQw4w9WgXcQ", "not a plain hostname"},
		{"punycode lookalike", "https://www.xn--yutube-wqf.com/watch?v=dQw4w9WgXcQ", "is not allowed"},
		{"underscore host", "https://www_youtube.com/", "not a plain hostname"},
		{"empty label", "https://www..youtube.com/watch?v=dQw4w9WgXcQ", "not a valid hostname"},
		{"localhost", "http://localhost/", "not a valid hostname"},
		{"playlist", "https://www.youtube.com/playlist?list=PL0123456789", "could not find a video id"},
		{"channel", "https://www.youtube.com/@somechannel/videos", "could not find a video id"},
		{"watch without id", "https://www.youtube.com/watch?list=PL0123", "could not find a video id"},
		{"watch bad id", "https://www.youtube.com/watch?v=short", "could not find a video id"},
		{"short link without id", "https://youtu.be/", "could not find a video id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.Resolve(tt.in)
			if err == nil {
				t.Fatalf("Resolve(%q) = %+v, want rejection", tt.in, got)
			}
			if !IsRejection(err) {
				t.Fatalf("Resolve(%q) error %v is not a rejection", tt.in, err)
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("Resolve(%q) error %q, want it to contain %q", tt.in, err, tt.wantSubstr)
			}
		})
	}
}

func TestResolveCustomAllowlist(t *testing.T) {
	a, err := NewAllowlist([]string{"Videos.Example.org", "youtu.be"})
	if err != nil {
		t.Fatalf("NewAllowlist: %v", err)
	}

	got, err := a.Resolve("https://videos.example.org/w/abc?x=1#frag")
	if err != nil {
		t.Fatalf("Resolve custom host: %v", err)
	}
	if got.URL != "https://videos.example.org/w/abc?x=1" || got.VideoID != "" || got.Host != "videos.example.org" {
		t.Fatalf("custom host target = %+v", got)
	}

	// youtu.be is allowed but the canonical www.youtube.com is not: refuse
	// rather than let yt-dlp contact a host the operator did not list.
	if _, err := a.Resolve("https://youtu.be/dQw4w9WgXcQ"); err == nil || !strings.Contains(err.Error(), "www.youtube.com, which is not on the allowed host list") {
		t.Fatalf("youtu.be without canonical host: err = %v", err)
	}
	if _, err := a.Resolve("dQw4w9WgXcQ"); err == nil {
		t.Fatal("bare id accepted without www.youtube.com on the allowlist")
	}
}

func TestNewAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		hosts   []string
		want    []string
		wantErr string
	}{
		{"defaults", DefaultHosts, []string{"m.youtube.com", "music.youtube.com", "www.youtube-nocookie.com", "www.youtube.com", "youtu.be", "youtube-nocookie.com", "youtube.com"}, ""},
		{"normalizes and dedupes", []string{" WWW.YouTube.com ", "www.youtube.com", ""}, []string{"www.youtube.com"}, ""},
		{"empty", []string{"", " "}, nil, "empty"},
		{"ip", []string{"10.0.0.1"}, nil, "not an IP address"},
		{"ipv6", []string{"::1"}, nil, "port or be an IPv6"},
		{"port", []string{"example.com:8080"}, nil, "port"},
		{"wildcard", []string{"*.example.com"}, nil, "wildcards"},
		{"single label", []string{"localhost"}, nil, "two labels"},
		{"trailing dot", []string{"example.com."}, nil, "dot"},
		{"non ascii", []string{"exämple.com"}, nil, "ASCII DNS label"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := NewAllowlist(tt.hosts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NewAllowlist(%q) err = %v, want containing %q", tt.hosts, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewAllowlist(%q): %v", tt.hosts, err)
			}
			if got := a.Hosts(); strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("Hosts() = %v, want %v", got, tt.want)
			}
		})
	}
}
