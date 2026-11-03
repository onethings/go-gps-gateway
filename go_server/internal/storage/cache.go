package postgres

import (
	"sync"
	"sync/atomic"
	"time"

	"traccar-server_go/go_server/internal/auth"
)

// tokenCacheTTL is how long a resolved token lookup is trusted before it is
// treated as a miss again. The generation bump on writes already invalidates
// the entries a change affects; the TTL is a second guard so a long-lived
// process never serves an entry whose row was removed without a bump (for
// example an administrator editing the database out of band).
const tokenCacheTTL = 5 * time.Minute

// tokenCacheEntry is one cached token lookup. version ties the entry to the
// store revision it was resolved from, and storedAt the wall clock it landed,
// so the cache has both an invalidation version and a freshness bound.
type tokenCacheEntry struct {
	version    int64
	storedAt   time.Time
	principal  *auth.Principal
}

// tokenCache is the versioned, per-process cache in front of LookupToken. It is
// what the roadmap calls the "帶版本號的快取": every mutation that can change a
// token's authorisation bumps the generation, and a lookup only serves an entry
// whose generation still matches and that is still fresh.
//
// A principal is immutable once built (NewPrincipal copies its slices), so the
// same *auth.Principal can be shared by many goroutines. A cached miss stores a
// nil principal with present=true, so an unknown token does not hammer the
// database.
type tokenCache struct {
	mu         sync.RWMutex
	version    int64
	now        func() time.Time
	entries    map[string]tokenCacheEntry
	// capacity bounds the cache so a long-lived process cannot grow it without
	// bound. Reaching it evicts the whole cache; reads refill it, so this is a
	// size cap, not a performance cliff.
	capacity int
	// hits and misses feed stats only. They are atomics so the read path can
	// update them while holding only the read lock; the map access stays under
	// the mutex.
	hits   atomic.Int64
	misses atomic.Int64
}

// newTokenCache builds an empty cache. capacity <= 0 means no size bound.
func newTokenCache(capacity int) *tokenCache {
	return &tokenCache{
		entries:  make(map[string]tokenCacheEntry),
		capacity: capacity,
		now:      time.Now,
	}
}

// get returns the cached principal for tokenHash and whether the entry is still
// current. present is false for a hash that was never cached; stale is true for
// an entry the cache holds but must not serve (wrong generation or past TTL).
func (c *tokenCache) get(tokenHash string, generation int64) (principal *auth.Principal, present bool, stale bool) {
	c.mu.RLock()
	entry, ok := c.entries[tokenHash]
	if !ok {
		c.misses.Add(1)
		c.mu.RUnlock()
		return nil, false, false
	}
	if entry.version != generation || c.now().After(entry.storedAt.Add(tokenCacheTTL)) {
		c.misses.Add(1)
		c.mu.RUnlock()
		return nil, false, true
	}
	c.hits.Add(1)
	c.mu.RUnlock()
	return entry.principal, true, false
}

// put stores a resolved lookup under the current generation. A nil principal
// caches a miss, which requires present=true so get can tell it apart from
// "never cached".
func (c *tokenCache) put(tokenHash string, generation int64, principal *auth.Principal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capacity > 0 && len(c.entries) >= c.capacity {
		c.entries = make(map[string]tokenCacheEntry)
	}
	c.entries[tokenHash] = tokenCacheEntry{
		version:    generation,
		storedAt:   c.now(),
		principal:  principal,
	}
}

// invalidate advances the generation and drops the entries built against the
// old one. Because mutations are far rarer than lookups, clearing the whole map
// on a bump is cheaper than tracking which key each write affected.
func (c *tokenCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.version++
	c.entries = make(map[string]tokenCacheEntry)
}

// generation reports the current store revision.
func (c *tokenCache) generation() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

// count returns the number of live entries.
func (c *tokenCache) count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// invalidateTokenCache bumps the token cache generation. Every write that can
// change what a token authorises calls it, so the next LookupToken for any
// cached hash reloads from the database instead of serving a stale principal.
func (s *Store) invalidateTokenCache() {
	if s.tokens != nil {
		s.tokens.invalidate()
	}
}
