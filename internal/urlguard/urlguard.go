// Package urlguard is the SSRF boundary between tool input and yt-dlp.
//
// yt-dlp will fetch almost any URL it is handed, so every URL-shaped tool
// argument passes through [Allowlist.Resolve] before a process is started.
// Resolve accepts only http(s) URLs whose host is exactly on the configured
// allowlist, and rejects everything else up front: other schemes (including
// yt-dlp pseudo-URLs such as "ytsearch:"), userinfo, IP literals, explicit
// non-default ports, trailing-dot hosts, percent-encoded or non-ASCII hosts,
// and any whitespace, control character or backslash.
//
// Resolve never returns the caller's raw string. It returns a URL rebuilt from
// the parsed components, so the validator and yt-dlp cannot disagree about
// which host is being contacted. YouTube URLs are further reduced to the
// canonical https://www.youtube.com/watch?v=<id> form, which also stops a
// playlist or channel URL from fanning out into many extractions.
package urlguard

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// DefaultHosts is the allowlist used when none is configured: the YouTube
// hostnames a user is likely to paste.
var DefaultHosts = []string{
	"youtube.com",
	"www.youtube.com",
	"m.youtube.com",
	"music.youtube.com",
	"youtu.be",
	"youtube-nocookie.com",
	"www.youtube-nocookie.com",
}

// CanonicalYouTubeHost is the host every YouTube URL is rewritten to. yt-dlp's
// YouTube extractor talks to it regardless of which YouTube hostname the input
// used, so it must be on the allowlist for any YouTube input to be accepted.
const CanonicalYouTubeHost = "www.youtube.com"

// MaxURLLength bounds the accepted input length.
const MaxURLLength = 2048

// youtubeHosts are the hostnames whose URLs are canonicalized to a watch URL.
// This is fixed (not the configured allowlist): it describes what a YouTube
// URL looks like, while the allowlist decides whether one is permitted.
var youtubeHosts = map[string]bool{
	"youtube.com":              true,
	"www.youtube.com":          true,
	"m.youtube.com":            true,
	"music.youtube.com":        true,
	"youtu.be":                 true,
	"youtube-nocookie.com":     true,
	"www.youtube-nocookie.com": true,
}

var (
	videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	// authorityRe is the only authority shape accepted: a plain DNS name with
	// an optional numeric port. It excludes '@' (userinfo), '%' (encoded
	// hosts), '[' / ']' (IPv6 literals), '_' and every non-ASCII byte.
	authorityRe = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)
	labelRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Error is a rejected input. Its message is written for the calling agent:
// it says what was wrong and what would be accepted instead.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func rejectf(format string, args ...any) error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}

// IsRejection reports whether err is an input rejection from this package.
func IsRejection(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Allowlist is an immutable set of exact hostnames.
type Allowlist struct {
	hosts map[string]bool
	list  []string
}

// NewAllowlist validates hosts and builds an allowlist. Entries are matched
// exactly (no wildcards, no implicit subdomains) after lowercasing; an entry
// that is an IP literal, carries a port, a trailing dot, or is otherwise not
// a multi-label DNS name is a configuration error.
func NewAllowlist(hosts []string) (*Allowlist, error) {
	a := &Allowlist{hosts: map[string]bool{}}
	for _, raw := range hosts {
		h := strings.ToLower(strings.TrimSpace(raw))
		if h == "" {
			continue
		}
		if err := validHostname(h); err != nil {
			return nil, fmt.Errorf("allowed host %q: %w", raw, err)
		}
		if !a.hosts[h] {
			a.hosts[h] = true
			a.list = append(a.list, h)
		}
	}
	if len(a.list) == 0 {
		return nil, errors.New("the allowed host list is empty")
	}
	slices.Sort(a.list)
	return a, nil
}

// Hosts returns the allowed hostnames, sorted.
func (a *Allowlist) Hosts() []string { return slices.Clone(a.list) }

// Allows reports whether host (case-insensitive) is on the list.
func (a *Allowlist) Allows(host string) bool { return a.hosts[strings.ToLower(host)] }

// Target is a validated, canonical URL ready to hand to yt-dlp.
type Target struct {
	// URL is the rebuilt URL passed to yt-dlp after its "--" marker.
	URL string
	// Host is the lowercased host of URL.
	Host string
	// VideoID is the YouTube video id, empty for non-YouTube hosts.
	VideoID string
}

// Resolve validates input (a URL or a bare 11-character YouTube video id)
// against the allowlist and returns its canonical form.
func (a *Allowlist) Resolve(input string) (Target, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return Target{}, rejectf("url is required: pass a video URL (e.g. https://www.youtube.com/watch?v={id}) or an 11-character YouTube video id")
	}
	if len(s) > MaxURLLength {
		return Target{}, rejectf("url is %d bytes long; the limit is %d", len(s), MaxURLLength)
	}
	if videoIDRe.MatchString(s) {
		return a.youtubeTarget(s)
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r == '\\' {
			return Target{}, rejectf("url %q contains whitespace, control characters or a backslash", s)
		}
	}

	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return Target{}, rejectf("%q is not an http(s) URL or an 11-character YouTube video id", s)
	}
	scheme = strings.ToLower(scheme)
	if scheme != "http" && scheme != "https" {
		return Target{}, rejectf("url scheme %q is not allowed; only http and https URLs are accepted", scheme)
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	if strings.Contains(authority, "@") {
		return Target{}, rejectf("urls with a username or password are not allowed")
	}
	if !authorityRe.MatchString(authority) {
		return Target{}, rejectf("url host %q is not a plain hostname; IP literals, encoded or non-ASCII hosts are not allowed", authority)
	}

	u, err := url.Parse(s)
	if err != nil {
		return Target{}, rejectf("url %q could not be parsed: %v", s, err)
	}
	if u.Opaque != "" || u.User != nil || u.Host == "" {
		return Target{}, rejectf("url %q is not an absolute http(s) URL with a host", s)
	}

	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !isDefaultPort(scheme, port) {
		return Target{}, rejectf("url port %s is not allowed; use the scheme's default port", port)
	}
	if strings.HasSuffix(host, ".") {
		return Target{}, rejectf("host %q ends with a dot; remove the trailing dot", host)
	}
	if looksLikeIP(host) {
		return Target{}, rejectf("host %q is an IP address; only allowed hostnames are accepted (%s)", host, a.hostsString())
	}
	if err := validHostname(host); err != nil {
		return Target{}, rejectf("host %q is not a valid hostname: %v", host, err)
	}
	if !a.Allows(host) {
		return Target{}, rejectf("host %q is not allowed; allowed hosts: %s", host, a.hostsString())
	}

	if youtubeHosts[host] {
		id, ok := youtubeVideoID(host, u)
		if !ok {
			return Target{}, rejectf("could not find a video id in %q; pass a single-video URL (https://www.youtube.com/watch?v={id}, https://youtu.be/{id}, /shorts/{id}) or the bare 11-character id — playlists and channels are not supported", s)
		}
		return a.youtubeTarget(id)
	}

	clean := url.URL{Scheme: scheme, Host: host, Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}
	return Target{URL: clean.String(), Host: host}, nil
}

func (a *Allowlist) youtubeTarget(id string) (Target, error) {
	if !a.Allows(CanonicalYouTubeHost) {
		return Target{}, rejectf("YouTube videos are fetched from %s, which is not on the allowed host list (%s)", CanonicalYouTubeHost, a.hostsString())
	}
	return Target{
		URL:     "https://" + CanonicalYouTubeHost + "/watch?v=" + id,
		Host:    CanonicalYouTubeHost,
		VideoID: id,
	}, nil
}

func (a *Allowlist) hostsString() string { return strings.Join(a.list, ", ") }

// youtubeVideoID extracts the video id from the URL shapes YouTube uses for a
// single video.
func youtubeVideoID(host string, u *url.URL) (string, bool) {
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if host == "youtu.be" {
		if len(segs) >= 1 && videoIDRe.MatchString(segs[0]) {
			return segs[0], true
		}
		return "", false
	}
	if len(segs) == 1 && segs[0] == "watch" {
		if v := u.Query().Get("v"); videoIDRe.MatchString(v) {
			return v, true
		}
		return "", false
	}
	if len(segs) >= 2 {
		switch segs[0] {
		case "shorts", "embed", "live", "v", "e":
			if videoIDRe.MatchString(segs[1]) {
				return segs[1], true
			}
		}
	}
	return "", false
}

func isDefaultPort(scheme, port string) bool {
	return (scheme == "https" && port == "443") || (scheme == "http" && port == "80")
}

// looksLikeIP catches IP literals and the numeric shorthands some resolvers
// accept ("127.1", "2130706433", "0x7f.0.0.1").
func looksLikeIP(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]
	if last == "" {
		return false
	}
	if strings.HasPrefix(last, "0x") {
		return true
	}
	return strings.Trim(last, "0123456789") == ""
}

// validHostname accepts lowercase multi-label DNS names only.
func validHostname(h string) error {
	switch {
	case len(h) > 253:
		return errors.New("longer than 253 characters")
	case strings.Contains(h, "*"):
		return errors.New("wildcards are not supported; list each hostname exactly")
	case strings.Contains(h, ":"):
		return errors.New("must not include a port or be an IPv6 address")
	case strings.HasSuffix(h, "."):
		return errors.New("must not end with a dot")
	case looksLikeIP(h):
		return errors.New("must be a hostname, not an IP address")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return errors.New("must be a fully qualified name with at least two labels")
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return fmt.Errorf("label %q is not a valid ASCII DNS label", l)
		}
	}
	return nil
}
