package name

import "runtime"

// Default fuzzy limits. See Options for what each one bounds and why it sits
// where it does.
const (
	DefaultFuzzyMaxNames      = 25_000_000
	DefaultFuzzyMaxCandidates = 4_000_000
)

// Options are a Finder's fuzzy-matching limits. They are fixed when the
// Finder is constructed (NewNameFinder, BuildIndex, DeserializeIndex take
// them as an optional trailing argument) and never change afterwards, so a
// running build or search can read them without synchronization. Passing no
// Options yields DefaultOptions; passing one uses its values verbatim, zero
// included. Zero is therefore chosen per field so that an Options literal
// written before a field existed keeps its old behavior: the zero value of
// FuzzyMaxConcurrent means "no admission limit".
type Options struct {
	// FuzzyMaxNames is the maximum number of (country, name) keys an index
	// may contain before the lazy n-gram fuzzy build is refused. Over it,
	// fuzzy matching is disabled for that Finder's lifetime and lookups
	// degrade to exact-only. The gate exists because the build is O(total
	// name runes) and leaves a >1 GiB resident structure at prod scale — a
	// bound so the initializer can never be surprised by an index far larger
	// than anything measured.
	//
	// The gate counts total (country, name) keys — the sum of the
	// per-country sorted-table name counts plus the overflow keys — not
	// unique names. That over-approximates the distinct-name set (a name
	// indexed in N countries counts N times), which is intentional: the count
	// is O(#countries) with zero allocations, and fuzzy matching is a
	// best-effort enhancement, never a correctness requirement.
	//
	// Default rationale (measured on Apple silicon, Oct 2026 GeoNames): at
	// the prod scale of 18,698,093 keys / 17,727,652 distinct names the lazy
	// n-gram build takes ~94 s (lock-free, in a background goroutine — the
	// triggering lookup returns exact-only rather than waiting it out), the
	// structure holds 1.18 GiB resident, and typo queries run at distance-1
	// p50 2.6 ms and distance-2 p50 20.6 ms / p99 749 ms — inside the <50 ms
	// p50 target with memory under the 3 GiB budget. The default therefore
	// sits above prod with ~34% key headroom. See
	// docs/design/index-format-v2.md for the full scale table.
	FuzzyMaxNames int

	// FuzzyMaxCandidates caps the posting-list work a single fuzzy search
	// may perform: the number of posting entries the rarest-lists walk may
	// read before it stops and returns the results verified so far,
	// best-effort, for that one query. It is the v1.1 clamp on the fuzzy
	// tail: at prod scale (17.73M names), distance-2 queries whose
	// length/gram filters degenerate — short or common-gram queries — were
	// measured walking multi-million-entry posting lists for up to ~8 s; the
	// budget bounds that walk per query (the default to a few seconds at
	// prod, tighter budgets to tens of milliseconds — see the measured sweep
	// in docs/design/index-format-v2.md).
	//
	// Semantics when the cap trips mid-query: no error, no panic — the
	// search returns the matches already verified (every returned name is a
	// true Levenshtein match; the cap can only lose results, never fabricate
	// them), the result is cached flagged partial (see below), and
	// Finder.FuzzyBudgetTrips increments. Exact (phase-1) lookups never
	// consult this path and are unaffected.
	//
	// A truncated result is cached, flagged partial, because the truncated
	// walk is deterministic for an unchanged index: the rarest-first walk
	// order is a total order (posting-list length, then gram id) and the cap
	// counts raw entries, so the same query truncates at the same entry and
	// returns the same matches. Without that, a degenerate query repeated N
	// times re-walked the whole budget N times (measured: 5 identical calls =
	// 5 trips, 0 cache entries). A partial hit is served without walking and
	// still increments FuzzyBudgetTrips, so the counter means "budget-truncated
	// searches answered", not "walks performed". The entry shares the cache's
	// TTL and generation rules: AddCity (which can change the index and the
	// answer) or the TTL expiring makes it a miss, and the Options are fixed
	// per Finder, so a cached partial can never outlive a budget change.
	//
	// Default rationale (Apple silicon, Oct 2026 GeoNames, 17.73M names; the
	// standard typo workload = 1k real names with 1–2 edits, the adversarial
	// workload = 1k one-to-three-rune queries): the standard workload's
	// largest posting walk is 3,539,399–3,632,442 entries across two
	// independent 1k samples (d2; ~531k at d1) while the adversarial
	// workload's is 4,000,118 — the two tails overlap, so no budget can both
	// keep the standard workload 100% complete AND clamp the adversarial p99
	// to ~100 ms; a ~500k budget measured 415 ms adversarial d2 p99 but
	// truncated 7.1% of standard d2 queries. The default sits above the
	// standard workload's observed maximum (0/1000 truncated at both
	// distances, in both samples) and is deliberately tight rather than
	// generous: every trip is loudly observable via FuzzyBudgetTrips and the
	// one-time log, so an operator with a heavier typo workload can raise it
	// on evidence, while a generous default would silently weaken the only
	// bound on degenerate queries. Its value at today's data is the
	// worst-case guarantee — no fuzzy query exceeds 4M posting entries of
	// work — not a p99 improvement; see docs/design/index-format-v2.md for
	// the full measured budget sweep. At corpora of 1M names and below the
	// cap is dormant (largest measured walk 225,469 entries).
	//
	// Negative disables the cap (v1.0 behavior: unbounded walk, full
	// completeness up to the q-gram boundary, unbounded tail).
	FuzzyMaxCandidates int

	// FuzzyMaxConcurrent is the number of fuzzy walks (cache-missing typo
	// lookups that actually search the n-gram index) one Finder runs at the
	// same time. Each walk holds a pooled seen-bitset (~2.2 MB at prod scale)
	// plus a touched list and, for degenerate queries, spends seconds of CPU;
	// ungated, 1024 concurrent connections of typo traffic mean 1024 such
	// walks at once. The gate is a non-blocking acquire: when every slot is
	// taken the lookup skips the fuzzy phase and returns the exact-only
	// result, exactly like the other degrade paths (index building, disabled
	// over FuzzyMaxNames), and Finder.FuzzyGateRejections increments. Nothing
	// queues, so a burst can never pile up latency. Cache hits and exact
	// lookups never take a slot.
	//
	// Zero or negative means unlimited (the pre-gate behavior), so Options
	// literals that predate this field behave as before. DefaultOptions sets
	// it to runtime.GOMAXPROCS(0) at the time it is called: a walk is CPU
	// bound, so more walks than cores only add contention and memory.
	FuzzyMaxConcurrent int
}

// DefaultOptions returns the production fuzzy limits.
func DefaultOptions() Options {
	return Options{
		FuzzyMaxNames:      DefaultFuzzyMaxNames,
		FuzzyMaxCandidates: DefaultFuzzyMaxCandidates,
		FuzzyMaxConcurrent: runtime.GOMAXPROCS(0),
	}
}

// resolveOptions picks the Options a constructor was given: the first one
// verbatim, or the defaults when none was passed.
func resolveOptions(opts []Options) Options {
	if len(opts) > 0 {
		return opts[0]
	}
	return DefaultOptions()
}
