//go:build race

package finder_test

// raceEnabled: the race detector makes sync.Pool drop items at random, so
// pooled paths allocate more than in a normal build.
const raceEnabled = true
