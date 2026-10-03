package name

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitFuzzyReturnsImmediatelyWhenNotBuilding(t *testing.T) {
	finder := BuildIndex(fuzzyFixtureCities())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, finder.WaitFuzzy(ctx), "not built and not building: nothing to wait for")
	assert.EqualValues(t, fuzzyNotBuilt, finder.FuzzyBuildState())
}

// TestWaitFuzzyBlocksUntilSettled parks the build behind the write lock and
// checks that every waiter is released, and only once the build has landed.
func TestWaitFuzzyBlocksUntilSettled(t *testing.T) {
	finder := BuildIndex(gateCities(2000))
	finder.mutex.Lock()
	finder.WarmFuzzy()

	const waiters = 16
	var wg sync.WaitGroup
	errs := make(chan error, waiters)
	states := make(chan int32, waiters)
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			errs <- finder.WaitFuzzy(ctx)
			states <- finder.FuzzyBuildState()
		}()
	}
	select {
	case <-errs:
		finder.mutex.Unlock()
		t.Fatal("a waiter returned while the build was still parked")
	case <-time.After(100 * time.Millisecond):
	}
	finder.mutex.Unlock()
	wg.Wait()
	close(errs)
	close(states)
	for err := range errs {
		assert.NoError(t, err)
	}
	for st := range states {
		assert.EqualValues(t, fuzzyBuilt, st)
	}
}

func TestWaitFuzzyHonorsContext(t *testing.T) {
	finder := BuildIndex(gateCities(2000))
	finder.mutex.Lock()
	finder.WarmFuzzy()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := finder.WaitFuzzy(ctx)
	finder.mutex.Unlock()
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	require.NoError(t, finder.WaitFuzzy(ctx2))
}

// TestFuzzyBuildPanicDisablesInsteadOfCrashing corrupts the index so the
// snapshot dereferences a nil table inside the build goroutine: the panic
// must be contained, logged, and leave the Finder exact-only.
func TestFuzzyBuildPanicDisablesInsteadOfCrashing(t *testing.T) {
	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	finder := BuildIndex(fuzzyFixtureCities())
	finder.countries["XX"] = nil // totalIndexKeys dereferences it

	finder.WarmFuzzy()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, finder.WaitFuzzy(ctx))

	assert.EqualValues(t, fuzzyDisabled, finder.FuzzyBuildState())
	assert.Equal(t, 1, strings.Count(logBuf.String(), "fuzzy index build panicked"))
	got := finder.CityByName("Paris", "FR")
	require.NotNil(t, got, "exact lookups survive a failed build")
	assert.Equal(t, "Paris", got.Name)
}
