package name

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFuzzyCacheByteBudget floods the cache with entries whose estimated size
// is a large fraction of the budget and requires the byte total to stay under
// maxFuzzyCacheBytes, the running total to equal the sum of live entries, and
// eviction to take the oldest entries first.
func TestFuzzyCacheByteBudget(t *testing.T) {
	finder := NewNameFinder()
	// 100k candidate slots = 1.6 MB estimated per entry; 200 of them is
	// 320 MB offered against a 64 MiB budget. Zero-valued strings are enough:
	// the estimate counts slots, not candidate bytes.
	const slots = 100_000
	big := make([]string, slots)
	now := time.Now()

	for i := 0; i < 200; i++ {
		key := fuzzyCacheKey{query: fmt.Sprintf("flood%04d", i), maxDistance: 2}
		finder.cacheMutex.Lock()
		finder.storeFuzzyCacheLocked(key, big, false, 1, now)
		finder.cacheMutex.Unlock()
		require.LessOrEqual(t, finder.fuzzyCacheBytes, maxFuzzyCacheBytes, "after insert %d", i)
	}

	finder.cacheMutex.RLock()
	defer finder.cacheMutex.RUnlock()
	sum := 0
	for _, e := range finder.fuzzyCache {
		sum += e.bytes
	}
	assert.Equal(t, sum, finder.fuzzyCacheBytes, "the running total must match the live entries")
	assert.Equal(t, len(finder.fuzzyCache), finder.fuzzyCacheAge.Len(), "map and age list stay in step")
	assert.Greater(t, len(finder.fuzzyCache), 10, "the budget holds many entries, not one")
	assert.Less(t, len(finder.fuzzyCache), 200, "the budget must have evicted")
	_, oldest := finder.fuzzyCache[fuzzyCacheKey{query: "flood0000", maxDistance: 2}]
	_, newest := finder.fuzzyCache[fuzzyCacheKey{query: "flood0199", maxDistance: 2}]
	assert.False(t, oldest, "the oldest entry is evicted first")
	assert.True(t, newest, "the newest entry survives")
}

// TestFuzzyCacheByteBudgetRejectsOversized proves one answer larger than the
// whole budget is not stored (it would evict everything for nothing) and
// leaves existing entries alone.
func TestFuzzyCacheByteBudgetRejectsOversized(t *testing.T) {
	finder := NewNameFinder()
	now := time.Now()
	small := fuzzyCacheKey{query: "small", maxDistance: 1}
	finder.cacheMutex.Lock()
	finder.storeFuzzyCacheLocked(small, []string{"a"}, false, 1, now)
	huge := make([]string, 0, maxFuzzyCacheBytes/fuzzyStringHeader+1)
	finder.storeFuzzyCacheLocked(fuzzyCacheKey{query: "huge", maxDistance: 2}, huge, false, 1, now)
	finder.cacheMutex.Unlock()

	finder.cacheMutex.RLock()
	defer finder.cacheMutex.RUnlock()
	_, hasSmall := finder.fuzzyCache[small]
	_, hasHuge := finder.fuzzyCache[fuzzyCacheKey{query: "huge", maxDistance: 2}]
	assert.True(t, hasSmall)
	assert.False(t, hasHuge)
	assert.Equal(t, finder.fuzzyCache[small].bytes, finder.fuzzyCacheBytes)
}

// TestFuzzyCacheBytesTrackReplaceAndExpiry covers the other two ways an entry
// leaves: replacement of an expired key and TTL eviction. Neither may leak
// bytes from the running total.
func TestFuzzyCacheBytesTrackReplaceAndExpiry(t *testing.T) {
	finder := NewNameFinder()
	t0 := time.Now()
	key := fuzzyCacheKey{query: "k", maxDistance: 1}

	finder.cacheMutex.Lock()
	finder.storeFuzzyCacheLocked(key, []string{"a", "b"}, false, 1, t0)
	finder.storeFuzzyCacheLocked(key, []string{"a"}, false, 1, t0.Add(time.Second))
	finder.cacheMutex.Unlock()
	assert.Equal(t, fuzzyEntryBytes(key, []string{"a"}), finder.fuzzyCacheBytes, "replacement subtracts the old size")

	other := fuzzyCacheKey{query: "other", maxDistance: 1}
	finder.cacheMutex.Lock()
	finder.storeFuzzyCacheLocked(other, nil, false, 1, t0.Add(2*fuzzyCacheTTL))
	finder.cacheMutex.Unlock()
	assert.Len(t, finder.fuzzyCache, 1, "the expired entry was dropped")
	assert.Equal(t, fuzzyEntryBytes(other, nil), finder.fuzzyCacheBytes, "TTL eviction subtracts its size")
}
