package dbusserver

import (
	"sync"
	"time"
)

// Only opaque, successful update responses are retained by the coordinator.
// Repository parsing and Flatpak discovery still happen in the unprivileged
// worker. A generation prevents an in-flight old query undoing invalidation.
type queryCache struct {
	mu         sync.Mutex
	generation uint64
	entries    map[queryCacheKey]queryCacheEntry
}
type queryCacheKey struct {
	uid    uint32
	method string
}
type queryCacheEntry struct {
	at    time.Time
	reply queryResponse
}

func (c *queryCache) get(key queryCacheKey) (queryResponse, bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry.reply, ok && time.Since(entry.at) < nativeUpdateCacheTTL, c.generation
}

func (c *queryCache) put(key queryCacheKey, generation uint64, reply queryResponse) {
	if reply.Error != nil {
		return
	}
	size := 0
	for _, value := range reply.Values {
		size += len(value)
	}
	// At most 16 * 2 MiB of retained response data, independent of caller count.
	if size > 2*1024*1024 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return
	}
	if c.entries == nil {
		c.entries = make(map[queryCacheKey]queryCacheEntry)
	}
	if len(c.entries) >= 16 {
		var oldest queryCacheKey
		var oldestAt time.Time
		for k, entry := range c.entries {
			if oldestAt.IsZero() || entry.at.Before(oldestAt) {
				oldest, oldestAt = k, entry.at
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = queryCacheEntry{time.Now(), reply}
}

func (c *queryCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.entries = nil
}

func (s *Server) query(request queryRequest, caller *desktopUser, journal bool) (queryResponse, error) {
	cacheable := request.Interface == "Software" && (request.Method == "ListNativeUpdates" || request.Method == "ListUpdates")
	key := queryCacheKey{method: request.Method}
	if caller != nil {
		key.uid = caller.Uid
	}
	var generation uint64
	if cacheable {
		reply, ok, gen := s.queryCache.get(key)
		if ok {
			return reply, nil
		}
		generation = gen
	}
	reply, err := executeQuery(request, caller, journal, s.profile)
	if cacheable && err == nil {
		s.queryCache.put(key, generation, reply)
	}
	return reply, err
}
