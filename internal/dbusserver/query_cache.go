package dbusserver

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// flatpakMarkers are touched by Flatpak after every install, update and
// removal in an installation, including those vegad did not run: Vega
// updates Flatpaks from the user session, and users may run flatpak in a
// terminal. GNOME Software watches the same files.
var flatpakSystemMarker = "/var/lib/flatpak/.changed"

// flatpakStamp identifies the Flatpak state of the system installation and
// of home's user installation. A cached update list is valid only while the
// stamp stays the same.
func flatpakStamp(home string) string {
	paths := []string{flatpakSystemMarker}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".local/share/flatpak/.changed"))
	}
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil {
			parts = append(parts, info.ModTime().UTC().Format(time.RFC3339Nano))
		} else {
			parts = append(parts, "-")
		}
	}
	return strings.Join(parts, "|")
}

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
	stamp string
	reply queryResponse
}

func (c *queryCache) get(key queryCacheKey, stamp string) (queryResponse, bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry.reply, ok && entry.stamp == stamp && time.Since(entry.at) < nativeUpdateCacheTTL, c.generation
}

func (c *queryCache) put(key queryCacheKey, generation uint64, stamp string, reply queryResponse) {
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
	c.entries[key] = queryCacheEntry{time.Now(), stamp, reply}
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
	stamp := ""
	if request.Method == "ListUpdates" {
		home := ""
		if caller != nil {
			home = caller.HomeDir
		}
		stamp = flatpakStamp(home)
	}
	if cacheable {
		reply, ok, gen := s.queryCache.get(key, stamp)
		if ok {
			return reply, nil
		}
		generation = gen
	}
	reply, err := executeQuery(request, caller, journal, s.profile)
	if cacheable && err == nil {
		s.queryCache.put(key, generation, stamp, reply)
	}
	return reply, err
}
