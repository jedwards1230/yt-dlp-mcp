package mcpserver

import (
	"container/list"
	"sync"
	"time"
)

// transcriptCache keeps recently fetched transcripts so paging through one
// costs a single yt-dlp run. It is a small LRU with a fixed TTL, bounded by
// both an entry count and a total size; a zero TTL disables it. Entries hold
// only public subtitle text, never credentials.
type transcriptCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	max      int
	maxBytes int64
	bytes    int64 // sum of the entries' sizes
	now      func() time.Time
	order    *list.List // front = most recently used
	entries  map[string]*list.Element
}

type cacheEntry struct {
	key     string
	value   *cachedTranscript
	size    int64
	expires time.Time
}

// segmentOverhead approximates the memory a segment costs beyond its text:
// the Segment struct (a duration and a string header) plus slice slack.
const segmentOverhead = 32

// transcriptSize approximates the memory a cached transcript holds, which
// the segments' text dominates.
func transcriptSize(t *cachedTranscript) int64 {
	n := int64(len(t.videoID) + len(t.title) + len(t.url) + len(t.lang))
	for _, s := range t.segments {
		n += int64(len(s.Text)) + segmentOverhead
	}
	return n
}

func newTranscriptCache(ttl time.Duration, maxEntries int, maxBytes int64, now func() time.Time) *transcriptCache {
	return &transcriptCache{ttl: ttl, max: maxEntries, maxBytes: maxBytes, now: now, order: list.New(), entries: map[string]*list.Element{}}
}

func (c *transcriptCache) get(key string) (*cachedTranscript, bool) {
	if c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if !c.now().Before(e.expires) {
		c.remove(el)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

// put stores v under key, evicting least recently used entries until both
// the entry count and the size budget hold. A transcript larger than the
// whole budget is not cached at all; paging through it re-fetches.
func (c *transcriptCache) put(key string, v *cachedTranscript) {
	if c.ttl <= 0 {
		return
	}
	size := transcriptSize(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	if size > c.maxBytes {
		return
	}
	c.entries[key] = c.order.PushFront(&cacheEntry{key: key, value: v, size: size, expires: c.now().Add(c.ttl)})
	c.bytes += size
	for c.order.Len() > c.max || c.bytes > c.maxBytes {
		c.remove(c.order.Back())
	}
}

// remove drops el from the cache. The caller holds c.mu.
func (c *transcriptCache) remove(el *list.Element) {
	e := el.Value.(*cacheEntry)
	c.order.Remove(el)
	delete(c.entries, e.key)
	c.bytes -= e.size
}
