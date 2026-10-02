package service

import (
	"fmt"
	"sync"
	"time"
)

type aggCacheEntry struct {
	value     map[string]any
	expiresAt time.Time
}

type AggregatesCache struct {
	mu      sync.RWMutex
	entries map[string]aggCacheEntry
	maxSize int
	ttl     time.Duration
}

func NewAggregatesCache() *AggregatesCache {
	return &AggregatesCache{
		entries: make(map[string]aggCacheEntry),
		maxSize: 50,
		ttl:     10 * time.Minute,
	}
}

func AggregateCacheKey(from, to string, topN int) string {
	return fmt.Sprintf("%s|%s|%d", from, to, topN)
}

func (c *AggregatesCache) Get(key string) (map[string]any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.value, true
}

func (c *AggregatesCache) Put(key string, value map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxSize {
		var oldest string
		var oldestExp time.Time
		for k, e := range c.entries {
			if oldest == "" || e.expiresAt.Before(oldestExp) {
				oldest = k
				oldestExp = e.expiresAt
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = aggCacheEntry{value: value, expiresAt: time.Now().Add(c.ttl)}
}

func (c *AggregatesCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]aggCacheEntry)
}
