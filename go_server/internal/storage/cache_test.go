package storage

import (
	"sync"
	"testing"
	"time"

	"traccar-server_go/go_server/internal/auth"
)

// fakeClock lets a test drive the TTL freshness path without sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

func (f *fakeClock) tick(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func testPrincipal() *auth.Principal {
	return auth.NewPrincipal(1, true, nil, []int64{7})
}

// TestTokenCacheRoundTrip verifies a stored principal is served back.
func TestTokenCacheRoundTrip(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := newTokenCache(0)
	c.now = clock.Now

	if p, present, stale := c.get("abc", c.generation()); present || stale || p != nil {
		t.Fatalf("fresh cache served a value: present=%v stale=%v p=%v", present, stale, p)
	}

	c.put("abc", c.generation(), testPrincipal())
	p, present, stale := c.get("abc", c.generation())
	if !present || stale || p == nil || p.UserID != 1 {
		t.Fatalf("round trip failed: present=%v stale=%v p=%v", present, stale, p)
	}
}

// TestTokenCacheNegativeCaching verifies a miss is cached and stays distinct
// from a present entry.
func TestTokenCacheNegativeCaching(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := newTokenCache(0)
	c.now = clock.Now

	c.put("missing", c.generation(), nil)
	p, present, stale := c.get("missing", c.generation())
	if !present || stale || p != nil {
		t.Fatalf("negative cache not served: present=%v stale=%v p=%v", present, stale, p)
	}
}

// TestTokenCacheInvalidation verifies a generation bump drops every entry.
func TestTokenCacheInvalidation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := newTokenCache(0)
	c.now = clock.Now

	c.put("abc", c.generation(), testPrincipal())
	gen := c.generation()
	c.invalidate()

	if c.generation() == gen {
		t.Fatal("generation did not advance")
	}
	// invalidate evicts the whole map, so the entry is gone entirely; get must
	// not serve it.
	if p, present, stale := c.get("abc", c.generation()); present || p != nil {
		t.Fatalf("stale entry served after invalidation: present=%v stale=%v p=%v", present, stale, p)
	}
}

// TestTokenCacheStaleGeneration verifies an entry built under an old generation
// is reported stale when read with the current one.
func TestTokenCacheStaleGeneration(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := newTokenCache(0)
	c.now = clock.Now

	oldGen := c.generation()
	c.put("abc", oldGen, testPrincipal())
	newGen := oldGen + 1

	if _, present, stale := c.get("abc", newGen); present || !stale {
		t.Fatalf("old-generation entry not stale: present=%v stale=%v", present, stale)
	}
}

// TestTokenCacheTTL verifies an entry older than tokenCacheTTL is stale even
// when its generation still matches.
func TestTokenCacheTTL(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := newTokenCache(0)
	c.now = clock.Now

	c.put("abc", c.generation(), testPrincipal())
	clock.tick(tokenCacheTTL + time.Second)

	if _, present, stale := c.get("abc", c.generation()); present || !stale {
		t.Fatalf("expired entry not stale: present=%v stale=%v", present, stale)
	}
}

// TestTokenCacheCapacity verifies that reaching capacity evicts the whole cache
// rather than growing without bound.
func TestTokenCacheCapacity(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := newTokenCache(2)
	c.now = clock.Now

	c.put("a", c.generation(), testPrincipal())
	c.put("b", c.generation(), testPrincipal())
	if got := c.count(); got != 2 {
		t.Fatalf("count before overflow = %d, want 2", got)
	}

	// The third put must evict everything, not grow past capacity.
	c.put("c", c.generation(), testPrincipal())
	if got := c.count(); got != 1 {
		t.Fatalf("count after overflow = %d, want 1", got)
	}
	if _, present, _ := c.get("a", c.generation()); present {
		t.Fatal("evicted entry still present")
	}
}

// TestTokenCacheConcurrentAccess exercises the cache under parallel readers
// and writers to catch data races (run with -race).
func TestTokenCacheConcurrentAccess(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := newTokenCache(64)
	c.now = clock.Now

	const workers = 8
	const ops = 2000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := "k" + string(rune('a'+i%4))
				c.put(key, c.generation(), testPrincipal())
				c.get(key, c.generation())
				if i%97 == 0 {
					c.invalidate()
				}
			}
		}()
	}
	wg.Wait()
}