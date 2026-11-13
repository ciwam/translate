package pipeline

import (
	"container/list"
	"fmt"
	"hash/fnv"
	"sync"
)

// fnv1a64Str hashes a string with FNV-1a 64-bit.
func fnv1a64Str(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s)) //nolint:errcheck
	return h.Sum64()
}

// CacheKey is a hash of (source, target, provider, glossary). Identical
// text that is translated again — a log pasted twice, an auto-translate
// re-fire after reverting an edit — returns the cached result instantly
// and, more importantly, deterministically, so the same input never yields
// a different translation across runs (the model is stochastic).
func CacheKey(provider, target int, src, glossary string) string {
	key := fmt.Sprintf("%s\x01%d\x01%d\x01%s", src, provider, target, glossary)
	return fmt.Sprintf("%016x", fnv1a64Str(key))
}

// resultCache is an in-process LRU (port of ResultCache, translator.cpp:699).
type resultCache struct {
	mu    sync.Mutex
	cap   int
	order *list.List               // most-recently-used at the front
	m     map[string]*list.Element // element value = [key, value]
}

type cacheEntry struct {
	key, val string
}

func newResultCache(capacity int) *resultCache {
	return &resultCache{cap: capacity, order: list.New(), m: make(map[string]*list.Element)}
}

func (c *resultCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(cacheEntry).val, true
	}
	return "", false
}

func (c *resultCache) put(key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.order.MoveToFront(el)
		el.Value = cacheEntry{key, val}
		return
	}
	c.m[key] = c.order.PushFront(cacheEntry{key, val})
	if len(c.m) > c.cap { // evict LRU
		if back := c.order.Back(); back != nil {
			delete(c.m, back.Value.(cacheEntry).key)
			c.order.Remove(back)
		}
	}
}

// Process-lifetime cache shared across Translate calls (64 entries, as C++).
var resultCacheDefault = newResultCache(64)

// ClearCache drops every cached translation. Used when the app is
// minimized: entries are reproducible (re-translate on demand), so they are
// the first thing to give back when memory matters.
func ClearCache() { resultCacheDefault = newResultCache(64) }
