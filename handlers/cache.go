package handlers

import (
	"sync"
	"time"

	"vidara-api/models"
)

type cacheEntry struct {
	result    *models.ResolutionResult
	expiresAt time.Time
}

// ResolutionCache provides a thread-safe in-memory cache for movie/TV resolution results.
type ResolutionCache struct {
	mu    sync.RWMutex
	items map[string]cacheEntry
}

// NewResolutionCache creates a new ResolutionCache instance.
func NewResolutionCache() *ResolutionCache {
	return &ResolutionCache{
		items: make(map[string]cacheEntry),
	}
}

// Get returns a cloned ResolutionResult if found and not expired.
func (c *ResolutionCache) Get(key string) (*models.ResolutionResult, bool) {
	c.mu.RLock()
	entry, found := c.items[key]
	c.mu.RUnlock()

	if !found {
		return nil, false
	}

	if time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return nil, false
	}

	return cloneResolutionResult(entry.result), true
}

// Set stores a clone of the ResolutionResult in cache with given TTL.
func (c *ResolutionCache) Set(key string, result *models.ResolutionResult, ttl time.Duration) {
	if result == nil || !result.Success {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = cacheEntry{
		result:    cloneResolutionResult(result),
		expiresAt: time.Now().Add(ttl),
	}
}

// Clear flushes all cached entries.
func (c *ResolutionCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]cacheEntry)
}

func cloneResolutionResult(orig *models.ResolutionResult) *models.ResolutionResult {
	if orig == nil {
		return nil
	}

	cloned := *orig
	if orig.Headers != nil {
		cloned.Headers = make(map[string]string, len(orig.Headers))
		for k, v := range orig.Headers {
			cloned.Headers[k] = v
		}
	}
	if orig.Play != nil {
		pCopy := *orig.Play
		cloned.Play = &pCopy
	}
	if orig.Season != nil {
		sCopy := *orig.Season
		cloned.Season = &sCopy
	}
	if orig.Episode != nil {
		eCopy := *orig.Episode
		cloned.Episode = &eCopy
	}
	if orig.Expiry != nil {
		expCopy := *orig.Expiry
		cloned.Expiry = &expCopy
	}

	return &cloned
}

type singleflightCall struct {
	wg  sync.WaitGroup
	val *models.ResolutionResult
	err error
}

// SingleflightGroup deduplicates concurrent resolution requests.
type SingleflightGroup struct {
	mu sync.Mutex
	m  map[string]*singleflightCall
}

// NewSingleflightGroup creates a new SingleflightGroup.
func NewSingleflightGroup() *SingleflightGroup {
	return &SingleflightGroup{
		m: make(map[string]*singleflightCall),
	}
}

// Do executes fn ensuring only one in-flight execution for a given key.
func (g *SingleflightGroup) Do(key string, fn func() (*models.ResolutionResult, error)) (*models.ResolutionResult, error) {
	g.mu.Lock()
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return cloneResolutionResult(c.val), c.err
	}

	c := new(singleflightCall)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return cloneResolutionResult(c.val), c.err
}
