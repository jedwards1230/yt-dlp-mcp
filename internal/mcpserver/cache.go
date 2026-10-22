package mcpserver

import (
	"container/list"
	"sync"
	"time"
)

// transcriptCache keeps recently fetched transcripts so paging through one
// costs a single yt-dlp run. It is a small LRU with a fixed TTL; a zero TTL
// disables it. Entries hold only public subtitle text, never credentials.
type transcriptCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	now     func() time.Time
	order   *list.List // front = most recently used
	entries map[string]*list.Element
}

type cacheEntry struct {
	key     string
	value   *cachedTranscript
	expires time.Time
}

func newTranscriptCache(ttl time.Duration, maxEntries int, now func() time.Time) *transcriptCache {
	return &transcriptCache{ttl: ttl, max: maxEntries, now: now, order: list.New(), entries: map[string]*list.Element{}}
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
		c.order.Remove(el)
		delete(c.entries, key)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.value, true
}

func (c *transcriptCache) put(key string, v *cachedTranscript) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.Remove(el)
	}
	c.entries[key] = c.order.PushFront(&cacheEntry{key: key, value: v, expires: c.now().Add(c.ttl)})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.entries, last.Value.(*cacheEntry).key)
	}
}
