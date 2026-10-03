package name

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitFuzzySettled polls until the fuzzy state leaves fuzzyBuilding, without
// nudging a new build.
func waitFuzzySettled(tb testing.TB, nf *Finder) {
	tb.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for nf.fuzzyState.Load() == fuzzyBuilding {
		if time.Now().After(deadline) {
			tb.Fatal("fuzzy build did not settle within 60s")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestWarmFuzzyDoesNotBlockOnSnapshot pins that WarmFuzzy never waits on the
// index lock: with the write lock held (as a long AddCity batch would), the
// call must still return promptly, with the build parked behind the lock.
func TestWarmFuzzyDoesNotBlockOnSnapshot(t *testing.T) {
	finder := BuildIndex(gateCities(2000))

	finder.mutex.Lock()
	returned := make(chan struct{})
	go func() {
		finder.WarmFuzzy()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		finder.mutex.Unlock()
		t.Fatal("WarmFuzzy blocked on the index lock")
	}
	assert.EqualValues(t, fuzzyBuilding, finder.FuzzyBuildState(), "the claimed build waits for the lock in its own goroutine")
	finder.mutex.Unlock()

	waitFuzzyBuilt(t, finder)
	got := finder.CityByName("GateCitt000042", "GC")
	require.NotNil(t, got)
	assert.Equal(t, "GateCity000042", got.Name)
}

// TestWarmFuzzyOverThresholdDoesNotBlockAndLogsOnce covers the gate under the
// same held lock: the disable decision is made by the build goroutine once it
// can read the key total, and is logged exactly once.
func TestWarmFuzzyOverThresholdDoesNotBlockAndLogsOnce(t *testing.T) {
	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	finder := BuildIndex(fuzzyFixtureCities(), Options{FuzzyMaxNames: 1, FuzzyMaxCandidates: DefaultFuzzyMaxCandidates})

	finder.mutex.Lock()
	returned := make(chan struct{})
	go func() {
		finder.WarmFuzzy()
		finder.WarmFuzzy()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		finder.mutex.Unlock()
		t.Fatal("WarmFuzzy blocked on the index lock")
	}
	finder.mutex.Unlock()

	waitFuzzySettled(t, finder)
	assert.EqualValues(t, fuzzyDisabled, finder.FuzzyBuildState())
	assert.Equal(t, 1, strings.Count(logBuf.String(), "fuzzy matching disabled"))
}

// TestWarmFuzzySingleSnapshotUnderConcurrentCallers pins the herd fix: only
// the caller that wins the claim pays for a name snapshot.
func TestWarmFuzzySingleSnapshotUnderConcurrentCallers(t *testing.T) {
	finder := BuildIndex(gateCities(2000))

	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < 32; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			finder.WarmFuzzy()
		}()
	}
	start.Done()
	done.Wait()

	waitFuzzyBuilt(t, finder)
	assert.EqualValues(t, 1, finder.fuzzyStats.snapshots.Load(), "exactly one name snapshot for 32 concurrent callers")
}
