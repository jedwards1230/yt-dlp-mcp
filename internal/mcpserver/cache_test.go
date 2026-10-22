package mcpserver

import (
	"testing"
	"time"
)

func TestTranscriptCache(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newTranscriptCache(time.Minute, 2, func() time.Time { return now })
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
	if len(c.entries) != 1 || c.order.Len() != 1 {
		t.Fatalf("expired entry not removed: %d entries, %d in order", len(c.entries), c.order.Len())
	}
}

func TestTranscriptCacheDisabled(t *testing.T) {
	c := newTranscriptCache(0, 2, time.Now)
	c.put("a", &cachedTranscript{})
	if _, ok := c.get("a"); ok {
		t.Fatal("disabled cache returned an entry")
	}
}
