package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/yt-dlp-mcp/internal/transcript"
)

func TestTranscriptCache(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newTranscriptCache(time.Minute, 2, 1<<20, func() time.Time { return now })
	a, b, d := &cachedTranscript{title: "a"}, &cachedTranscript{title: "b"}, &cachedTranscript{title: "d"}

	c.put("a", a)
	c.put("b", b)
	if got, ok := c.get("a"); !ok || got != a {
		t.Fatal("a missing")
	}
	c.put("d", d) // evicts b, the least recently used
	if _, ok := c.get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("a should have survived (recently used)")
	}

	now = now.Add(time.Minute)
	if _, ok := c.get("a"); ok {
		t.Fatal("a should have expired")
	}
	if len(c.entries) != 1 || c.order.Len() != 1 || c.bytes != transcriptSize(d) {
		t.Fatalf("expired entry not removed: %d entries, %d in order, %d bytes", len(c.entries), c.order.Len(), c.bytes)
	}
}

// TestTranscriptCacheByteBudget checks the size bound: entries are evicted
// least recently used first until the total fits, a transcript bigger than
// the whole budget is never stored, and replacing a key does not count it
// twice.
func TestTranscriptCacheByteBudget(t *testing.T) {
	text := func(n int) *cachedTranscript {
		return &cachedTranscript{segments: []transcript.Segment{{Text: strings.Repeat("x", n)}}}
	}
	size := transcriptSize(text(1000))
	if size != 1000+segmentOverhead {
		t.Fatalf("transcriptSize = %d, want %d", size, 1000+segmentOverhead)
	}
	c := newTranscriptCache(time.Minute, 10, 3*size, time.Now)

	c.put("a", text(1000))
	c.put("b", text(1000))
	c.put("c", text(1000))
	if c.bytes != 3*size || len(c.entries) != 3 {
		t.Fatalf("after three puts: %d bytes, %d entries", c.bytes, len(c.entries))
	}
	c.put("a", text(1000)) // replace: still three entries, a now most recent
	if c.bytes != 3*size || len(c.entries) != 3 {
		t.Fatalf("after replacing a: %d bytes, %d entries", c.bytes, len(c.entries))
	}
	c.put("d", text(1000)) // over budget: evicts b, the least recently used
	if _, ok := c.get("b"); ok {
		t.Fatal("b should have been evicted to fit the byte budget")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.get(k); !ok {
			t.Fatalf("%s should have survived", k)
		}
	}
	if c.bytes != 3*size {
		t.Fatalf("bytes = %d, want %d", c.bytes, 3*size)
	}

	c.put("huge", text(5000)) // larger than the whole budget: not cached
	if _, ok := c.get("huge"); ok {
		t.Fatal("a transcript larger than the budget was cached")
	}
	if len(c.entries) != 3 || c.bytes != 3*size {
		t.Fatalf("oversized put disturbed the cache: %d entries, %d bytes", len(c.entries), c.bytes)
	}

	c.put("a", text(5000)) // an existing key grown past the budget is dropped
	if _, ok := c.get("a"); ok || c.bytes != 2*size {
		t.Fatalf("oversized replacement kept: ok=%v bytes=%d", ok, c.bytes)
	}
}

func TestTranscriptCacheDisabled(t *testing.T) {
	c := newTranscriptCache(0, 2, 1<<20, time.Now)
	c.put("a", &cachedTranscript{})
	if _, ok := c.get("a"); ok {
		t.Fatal("disabled cache returned an entry")
	}
}
