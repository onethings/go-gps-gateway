// Package geo holds the services that need a third party: reverse geocoding,
// speed limits, map matching and cell tower positioning. Every one of them is
// cached, because the ingest path must never wait for a remote server.
package geo

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// DefaultCacheTTL is how long an answer from a remote service is reused.
const DefaultCacheTTL = 24 * time.Hour

// maxCacheEntries bounds the in-memory cache so a long running gateway does not
// grow without limit. The oldest entry is dropped when it is full.
const maxCacheEntries = 10000

type cacheEntry struct {
	Value string `json:"value"`
	// Expires is Unix nanoseconds so a short TTL is honoured: the services are
	// configured in seconds but a test uses milliseconds.
	Expires int64 `json:"expires"`
}

// Cache is a small TTL cache for the answers of the remote services. It can be
// persisted to a file, so a restart does not throw away what was already paid
// for, and it never blocks: a miss returns immediately.
type Cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	path    string
	entries map[string]cacheEntry
	dirty   bool
}

// NewCache creates a cache. path may be empty, which disables persistence.
func NewCache(ttl time.Duration, path string) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{ttl: ttl, path: path, entries: make(map[string]cacheEntry)}
}

// KeyFor rounds a coordinate so nearby requests share one entry. Five decimals
// is about a metre, which is finer than any of the services answer.
func KeyFor(prefix string, latitude, longitude float64) string {
	return fmt.Sprintf("%s:%s:%s", prefix,
		trim(latitude), trim(longitude))
}

func trim(value float64) string {
	return fmt.Sprintf("%.5f", value)
}

// Get returns the cached value when it is still fresh.
func (c *Cache) Get(key string) (string, bool) {
	if c == nil || key == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if time.Now().UnixNano() > entry.Expires {
		delete(c.entries, key)
		c.dirty = true
		return "", false
	}
	return entry.Value, true
}

// Set stores a value and marks the cache dirty so a later Save writes it.
func (c *Cache) Set(key, value string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= maxCacheEntries {
		c.evict()
	}
	c.entries[key] = cacheEntry{Value: value, Expires: time.Now().Add(c.ttl).UnixNano()}
	c.dirty = true
}

// evict drops the entries that expire first. The cache is small enough that a
// sort is cheaper than tracking the insertion order.
func (c *Cache) evict() {
	keys := make([]string, 0, len(c.entries))
	for key := range c.entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return c.entries[keys[i]].Expires < c.entries[keys[j]].Expires
	})
	for index := 0; index < len(keys)/4+1; index++ {
		delete(c.entries, keys[index])
	}
}

// Load reads the persisted cache. A missing file is not an error: the cache
// simply starts empty.
func (c *Cache) Load() error {
	if c == nil || c.path == "" {
		return nil
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("geo cache: %w", err)
	}
	entries := make(map[string]cacheEntry)
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("geo cache: %w", err)
	}

	now := time.Now().UnixNano()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range entries {
		if entry.Expires > now {
			c.entries[key] = entry
		}
	}
	return nil
}

// Save writes the cache, atomically so a crash cannot leave a half written
// file behind.
func (c *Cache) Save() error {
	if c == nil || c.path == "" {
		return nil
	}
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	now := time.Now().UnixNano()
	entries := make(map[string]cacheEntry, len(c.entries))
	for key, entry := range c.entries {
		if entry.Expires > now {
			entries[key] = entry
		}
	}
	c.dirty = false
	c.mu.Unlock()

	raw, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("geo cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("geo cache: %w", err)
	}
	temporary := c.path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return fmt.Errorf("geo cache: %w", err)
	}
	if err := os.Rename(temporary, c.path); err != nil {
		return fmt.Errorf("geo cache: %w", err)
	}
	return nil
}

// Size reports the number of live entries, which is what a test asserts on.
func (c *Cache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// CellKey identifies the request for the position of a cell tower group.
func CellKey(network *model.Network) string {
	if network == nil || len(network.CellTowers) == 0 {
		return ""
	}
	best := network.CellTowers[0]
	return fmt.Sprintf("cell:%d:%d:%d:%d", best.MobileCountryCode,
		best.MobileNetworkCode, best.LocationAreaCode, best.CellID)
}

// kmhToKnots converts a speed limit, because positions carry knots.
func kmhToKnots(kmh float64) float64 {
	return math.Round(kmh/1.852*100) / 100
}
