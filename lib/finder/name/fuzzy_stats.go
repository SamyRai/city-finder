package name

import "sync/atomic"

// fuzzyBudgetTrips counts fuzzy searches whose posting walk was cut short by
// the Options.FuzzyMaxCandidates cap. It is a process-global monotone counter, cheap
// to maintain (one atomic add per truncated search, none per untruncated
// one), exposed through FuzzyBudgetTrips for operators and tests.
var fuzzyBudgetTrips atomic.Uint64

// fuzzyBudgetLogged makes the first budget trip log exactly once, mirroring
// the one-time disable log of the Options.FuzzyMaxNames gate: no per-query logging,
// ever.
var fuzzyBudgetLogged atomic.Bool

// fuzzyWalkedLast records the number of posting entries read by the most
// recently completed search (capped or not), and fuzzyVerifiedLast the number
// of candidates that reached Levenshtein verification. They are diagnostics
// for budget tuning — how close real queries come to Options.FuzzyMaxCandidates, and
// whether a walk's cost sits in the walk itself or in verification — read by
// the scale gate and tests. Last-writer-wins across concurrent searches;
// never used for control flow.
var (
	fuzzyWalkedLast   atomic.Int64
	fuzzyVerifiedLast atomic.Int64
)

// FuzzyBudgetTrips reports how many fuzzy searches have returned partial
// results because the Options.FuzzyMaxCandidates cap tripped, since process start.
// A non-zero value in a healthy deployment means the workload contains
// queries degenerate enough to hit the cap (see Options.FuzzyMaxCandidates).
func FuzzyBudgetTrips() uint64 {
	return fuzzyBudgetTrips.Load()
}
