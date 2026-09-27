package cache

import (
	"fmt"
	"sync"
	"time"
)

type entry struct {
	value     any
	expiresAt time.Time
}

type AggregatesCache struct {
	mu      sync.RWMutex
	entries map[string]entry
	maxSize int
	ttl     time.Duration
}

func NewAggregatesCache() *AggregatesCache {
	return &AggregatesCache{
		entries: make(map[string]entry),
		maxSize: 50,
		ttl:     10 * time.Minute,
	}
}

func Key(from, to string, topN int) string {
	return fmt.Sprintf("%s|%s|%d", from, to, topN)
}

func (c *AggregatesCache) Get(key string) (any, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.value, true
}

func (c *AggregatesCache) Put(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxSize {
		var oldest string
		var oldestExp time.Time
		for k, v := range c.entries {
			if oldest == "" || v.expiresAt.Before(oldestExp) {
				oldest = k
				oldestExp = v.expiresAt
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = entry{value: value, expiresAt: time.Now().Add(c.ttl)}
}

func (c *AggregatesCache) InvalidateAll() {
	c.mu.Lock()
	c.entries = make(map[string]entry)
	c.mu.Unlock()
}
