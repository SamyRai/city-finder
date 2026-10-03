package name

import (
	"log"
	"sync/atomic"
)

// fuzzyStats is one Finder's fuzzy-search diagnostics. Every Finder owns its
// own, so two Finders in one process never share counters; the n-gram index
// built for a Finder holds a pointer to it and records after each search.
type fuzzyStats struct {
	// trips counts fuzzy searches whose posting walk was cut short by the
	// Options.FuzzyMaxCandidates cap: monotone, one atomic add per truncated
	// search and none per untruncated one.
	trips atomic.Uint64

	// logged makes the first budget trip log exactly once, mirroring the
	// one-time disable log of the Options.FuzzyMaxNames gate: no per-query
	// logging, ever.
	logged atomic.Bool

	// walkedLast records the number of posting entries read by the most
	// recently completed search (capped or not), and verifiedLast the number
	// of candidates that reached Levenshtein verification. They are
	// diagnostics for budget tuning — how close real queries come to
	// Options.FuzzyMaxCandidates, and whether a walk's cost sits in the walk
	// itself or in verification — read by the scale gate and tests.
	// Last-writer-wins across concurrent searches; never used for control
	// flow.
	walkedLast, verifiedLast atomic.Int64

	// snapshots counts the name snapshots taken for fuzzy builds: one per
	// build attempt, never one per caller.
	snapshots atomic.Int64
}

// record notes one completed search: its work counters, and (when the walk
// was truncated at budget entries) the trip and its one-time log line.
func (s *fuzzyStats) record(walked, verified int64, truncated bool, budget int) {
	s.walkedLast.Store(walked)
	s.verifiedLast.Store(verified)
	if !truncated {
		return
	}
	s.trips.Add(1)
	// One-time summary: the trip itself is the rare event worth surfacing,
	// per-query logging is not.
	if s.logged.CompareAndSwap(false, true) {
		log.Printf("fuzzy search candidate budget reached (%d posting entries); the query returned partial results — raise Options.FuzzyMaxCandidates (negative disables the cap) if this workload needs full completeness",
			budget)
	}
}

// FuzzyBudgetTrips reports how many fuzzy searches on this Finder have
// returned partial results because the Options.FuzzyMaxCandidates cap
// tripped, since the Finder was created. A non-zero value in a healthy
// deployment means the workload contains queries degenerate enough to hit
// the cap (see Options.FuzzyMaxCandidates).
func (nf *Finder) FuzzyBudgetTrips() uint64 {
	return nf.fuzzyStats.trips.Load()
}
