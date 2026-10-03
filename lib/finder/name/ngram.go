package name

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"slices"
	"sort"
	"sync/atomic"
	"unicode/utf8"
)

// The n-gram fuzzy index (phase 2) replaces the BK-tree as the fuzzy search
// structure. The BK-tree was measured at ~650 ms per distance-2 query over
// the 17.7M-name prod keyspace — worse than the typos it rescued — and its
// sequential build stalls every concurrent lookup for minutes, so fuzzy
// matching was hard-disabled above 2M keys (FuzzyMaxNames).
//
// Structure: an immutable inverted index over the q-grams (q = 3 runes,
// computed over the name padded with q−1 sentinel runes on each side),
// stored CSR-style as one flattened id array with per-gram offsets. It is
// built once, lock-free, from a name snapshot and never mutated, so
// concurrent searches need no locking at all.
//
// Filtering chain per query (maxDistance d), with the completeness argument:
//
//  1. Gram-count filter (q-gram lemma): one rune edit destroys at most q
//     DISTINCT grams, so a name within edit distance d contains at least
//     minCommon = Gd(query) − q·d of the query's distinct padded grams
//     (Gd = distinct-gram count). A match therefore misses at most
//     Gd(query) − minCommon of them.
//  2. Rarest-lists walk: walking all but the (minCommon − 1) LARGEST posting
//     lists enumerates every true match (it must appear in at least one
//     walked list by step 1) while skipping the giant lists of ubiquitous
//     grams — at prod scale the sentinel and common trigrams hold millions
//     of ids each.
//  3. Length filter: |runes(name) − runes(query)| ≤ d. This subsumes the
//     name-side gram-count filter (Gm(name) = runes + q − 1 ≥ minCommon
//     follows from the length bound), so no per-name gram counts are stored.
//  4. Levenshtein verification of the survivors.
//  5. Candidate budget: the walk in step 2 stops after FuzzyMaxCandidates
//     posting entries have been read (see FuzzyMaxCandidates in name.go for
//     why the counter sits on raw entries, not on verifications).
//
// Postings may list a name id more than once (a gram repeated inside one
// name appends once per occurrence); search dedups with a pooled bitset,
// so duplicates only cost a redundant set probe.
//
// Completeness boundary, documented honestly: when runes(name) + q − 1 ≤ q·d
// the lemma only guarantees "≥ 0" shared grams and a true match may share
// none (every one of its grams destroyed by the edits). For q = 3 that means
// matches among names of ≤ 1 rune can be missed at distance 1, and among
// names of ≤ 4 runes at distance 2. Fuzzy is a best-effort typo-rescue layer
// (exact matches are always found by phase 1 regardless); at prod scale the
// BK-tree alternative was not merely incomplete but disabled outright.

const (
	// ngramQ is the gram length in runes.
	ngramQ = 3
	// ngramPad is the number of '\x00' sentinel runes added to each side
	// before extracting grams (q−1, enough for the lemma to bite on short
	// names). NUL does not occur in GeoNames names (TSV text); were it to
	// occur, only filter strength — never verified-result correctness —
	// would be affected.
	ngramPad = ngramQ - 1
)

// ngramIndex is an immutable q-gram inverted index over distinct names.
// Every field is written during the lock-free build and read-only after,
// making concurrent searches race-free without locks.
//
// Posting lists are delta + uvarint encoded: each gram's list holds its
// ascending name ids (repeated when a name contains the gram twice) as
// differences from the previous id, so most entries take one byte instead of
// four. postLen keeps the raw entry count per gram, which is what list
// ranking and the per-query candidate budget count — decoding yields exactly
// the ids the plain int32 layout held, in the same order.
type ngramIndex struct {
	names    []string         // id -> name (headers shared with the snapshot)
	nameLens []uint16         // id -> rune count, for the length filter
	post     []byte           // CSR payload: per gram, uvarint id deltas
	postOff  []int64          // gram id -> byte offset into post; one sentinel entry
	postLen  []int32          // gram id -> number of postings (ids) in its list
	gramIDs  map[string]int32 // distinct padded gram -> CSR column
}

// appendPostings delta-encodes one ascending id list.
func appendPostings(dst []byte, ids []int32) []byte {
	prev := int32(0)
	for _, id := range ids {
		dst = binary.AppendUvarint(dst, uint64(id-prev))
		prev = id
	}
	return dst
}

// nextPosting decodes one delta from data, returning the delta and the bytes
// consumed. Single-byte deltas (the common case) take the fast path.
func nextPosting(data []byte) (int32, int) {
	if b := data[0]; b < 0x80 {
		return int32(b), 1
	}
	v, n := binary.Uvarint(data)
	return int32(v), n
}

// ngramScratch is the reusable build/search buffer for gram extraction:
// decoding a padded name into runes and encoding each window into bytes. A
// scratch is owned by one goroutine (the builder or one search) and never
// shared.
type ngramScratch struct {
	runes []rune
	win   []byte
}

// forEachGram decodes name with padding and calls fn once per q-gram window,
// encoded as bytes in s.win. When runeLn is non-nil it receives the rune
// count of the unpadded name; it is written before any callback fires.
func (s *ngramScratch) forEachGram(name string, runeLn *int, fn func()) {
	s.runes = append(s.runes[:0], 0, 0)
	for _, r := range name {
		s.runes = append(s.runes, r)
	}
	s.runes = append(s.runes, 0, 0)
	if runeLn != nil {
		*runeLn = len(s.runes) - 2*ngramPad
	}
	for i := 0; i+ngramQ <= len(s.runes); i++ {
		s.win = utf8.AppendRune(s.win[:0], s.runes[i])
		for _, r := range s.runes[i+1 : i+ngramQ] {
			s.win = utf8.AppendRune(s.win, r)
		}
		fn()
	}
}

// buildNGramIndex constructs the index over names. It is pure with respect
// to names (string headers are shared, never copied). It fails only when the
// corpus holds more gram postings than the int32 CSR offsets can address.
func buildNGramIndex(names []string) (*ngramIndex, error) {
	gramIDs := make(map[string]int32)
	counts := make([]int32, 0, 1<<16) // gram id -> posting count
	nameLens := make([]uint16, len(names))
	var s ngramScratch
	var runeLn int

	// Total postings accumulated in int64: the per-gram int32 counters and
	// the int32 offset sums below are trusted only after this total proves
	// they cannot have wrapped.
	var totalPostings int64

	// Pass 1: assign gram ids, count postings per gram, record rune lens.
	// map[string]int32 lookups keyed by string([]byte) are allocation-free;
	// only first-seen grams allocate their key.
	for id, name := range names {
		s.forEachGram(name, &runeLn, func() {
			gid, ok := gramIDs[string(s.win)]
			if !ok {
				gid = int32(len(counts))
				gramIDs[string(s.win)] = gid
				counts = append(counts, 0)
			}
			counts[gid]++
			totalPostings++
		})
		// Rune lengths are stored as uint16 for footprint; names of 65,536+
		// runes would silently wrap. Cap instead: such a name can never pass
		// the length filter (|runes(name) − runes(query)| ≤ d) for any
		// query a caller can realistically issue, so the fuzzy layer simply
		// cannot find it — an acceptable loss for a best-effort typo-rescue
		// layer. Exact (phase 1) lookups do not consult this index and are
		// unaffected.
		if runeLn > math.MaxUint16 {
			runeLn = math.MaxUint16
		}
		nameLens[id] = uint16(runeLn)
	}

	// Explicit overflow gate: the CSR payload and offsets are int32 by
	// construction, so more than MaxInt32 postings would wrap the prefix
	// sums into negative offsets and corrupt the index. Refuse to build.
	// (Unreachable under the default FuzzyMaxNames gate; it exists so a
	// pathological corpus fails loudly instead of silently.)
	if totalPostings > math.MaxInt32 {
		return nil, fmt.Errorf("n-gram index requires %d gram postings, exceeding the int32 offset limit (%d); refusing to build a corrupt index", totalPostings, int64(math.MaxInt32))
	}

	// CSR offsets from prefix sums; total ≤ MaxInt32 was proven above.
	postOff := make([]int32, len(counts)+1)
	total := int32(0)
	for gid, c := range counts {
		postOff[gid] = total
		total += c
	}
	postOff[len(counts)] = total

	// Pass 2: fill the postings (plain int32 CSR, build-transient).
	plain := make([]int32, total)
	cursors := make([]int32, len(counts))
	copy(cursors, postOff[:len(counts)])
	for id, name := range names {
		s.forEachGram(name, nil, func() {
			gid := gramIDs[string(s.win)]
			plain[cursors[gid]] = int32(id)
			cursors[gid]++
		})
	}

	// Pass 3: delta-encode each gram's list. Ids were appended in name-id
	// order, so every list is ascending; the plain array is dropped after.
	encOff := make([]int64, len(counts)+1)
	enc := make([]byte, 0, int(total)+int(total)/2)
	for gid := range counts {
		encOff[gid] = int64(len(enc))
		enc = appendPostings(enc, plain[postOff[gid]:postOff[gid+1]])
	}
	encOff[len(counts)] = int64(len(enc))
	plain = nil

	return &ngramIndex{
		names:    names,
		nameLens: nameLens,
		post:     slices.Clip(enc),
		postOff:  encOff,
		postLen:  counts,
		gramIDs:  gramIDs,
	}, nil
}

// distinctQueryGrams returns the distinct padded grams of query, deduped
// with a linear scan (queries have at most a few dozen grams).
func distinctQueryGrams(query string) []string {
	var s ngramScratch
	var grams []string
	s.forEachGram(query, nil, func() {
		for _, g := range grams {
			if g == string(s.win) {
				return
			}
		}
		grams = append(grams, string(s.win))
	})
	return grams
}

// fuzzyBudgetTrips counts fuzzy searches whose posting walk was cut short by
// the FuzzyMaxCandidates cap. It is a process-global monotone counter, cheap
// to maintain (one atomic add per truncated search, none per untruncated
// one), exposed through FuzzyBudgetTrips for operators and tests.
var fuzzyBudgetTrips atomic.Uint64

// fuzzyBudgetLogged makes the first budget trip log exactly once, mirroring
// the one-time disable log of the FuzzyMaxNames gate: no per-query logging,
// ever.
var fuzzyBudgetLogged atomic.Bool

// fuzzyWalkedLast records the number of posting entries read by the most
// recently completed search (capped or not), and fuzzyVerifiedLast the number
// of candidates that reached Levenshtein verification. They are diagnostics
// for budget tuning — how close real queries come to FuzzyMaxCandidates, and
// whether a walk's cost sits in the walk itself or in verification — read by
// the scale gate and tests. Last-writer-wins across concurrent searches;
// never used for control flow.
var (
	fuzzyWalkedLast   atomic.Int64
	fuzzyVerifiedLast atomic.Int64
)

// FuzzyBudgetTrips reports how many fuzzy searches have returned partial
// results because the FuzzyMaxCandidates cap tripped, since process start.
// A non-zero value in a healthy deployment means the workload contains
// queries degenerate enough to hit the cap (see FuzzyMaxCandidates).
func FuzzyBudgetTrips() uint64 {
	return fuzzyBudgetTrips.Load()
}

// levenshteinChecker answers "is the rune-level edit distance between the
// query and a candidate ≤ d?" without allocating per comparison: the query
// side is decoded once per search into a reusable buffer, the candidate side
// is streamed with range-over-string rune decoding, and the DP is banded to
// ±d of the diagonal with a row-minimum early exit. It replaced
// agnivade/levenshtein in the verify step after a prod-scale profile showed
// that library's two []rune conversions per candidate (stringtoslicerune +
// memclr) cost ~half of all distance-2 search time; the library has since
// been dropped from the module entirely (the overflow scan in name.go reuses
// this checker, and the tests carry their own reference DP).
type levenshteinChecker struct {
	queryRunes []rune
	prev, curr []int
}

// prepare decodes the query once; every atMost call for the same query
// reuses the buffers.
func (c *levenshteinChecker) prepare(query string) {
	c.queryRunes = append(c.queryRunes[:0], []rune(query)...)
	if cap(c.curr) < len(c.queryRunes)+1 {
		c.curr = make([]int, len(c.queryRunes)+1)
		c.prev = make([]int, len(c.queryRunes)+1)
	}
	c.curr = c.curr[:len(c.queryRunes)+1]
	c.prev = c.prev[:len(c.queryRunes)+1]
}

// atMost reports whether levenshtein(candidate, query) ≤ d. Rows run along
// the predecoded query; cells outside the ±d band are unreachable at the
// bound and never computed. Both the length difference and the row minimum
// exit as soon as the bound is unreachable.
func (c *levenshteinChecker) atMost(candidate string, d int) bool {
	q := c.queryRunes
	n := len(q)

	candidateRunes := utf8.RuneCountInString(candidate)
	if n-candidateRunes > d || candidateRunes-n > d {
		return false
	}

	// Row 0: distance between the empty prefix and each query prefix.
	for j := 0; j <= n; j++ {
		c.prev[j] = j
	}

	i := 0
	for _, r := range candidate {
		i++
		lo, hi := max(1, i-d), min(n, i+d) // the ±d band around the diagonal
		c.curr[0] = i
		for j := 1; j < lo; j++ {
			c.curr[j] = d + 1 // left of the band
		}
		rowMin := c.curr[0]
		for j := lo; j <= hi; j++ {
			cost := 1
			if q[j-1] == r {
				cost = 0
			}
			v := c.prev[j-1] + cost // substitution / match
			if t := c.prev[j] + 1; t < v {
				v = t // deletion from candidate
			}
			if t := c.curr[j-1] + 1; t < v {
				v = t // insertion into candidate
			}
			c.curr[j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		for j := hi + 1; j <= n; j++ {
			c.curr[j] = d + 1 // right of the band
		}
		if rowMin > d {
			return false
		}
		c.prev, c.curr = c.curr, c.prev
	}
	return c.prev[n] <= d
}

// search returns the indexed names within Levenshtein distance d of query,
// in arbitrary order, plus whether the result is partial: the posting walk
// stopped early after FuzzyMaxCandidates entries (see FuzzyMaxCandidates in
// name.go). A partial result is best-effort for that one query — every name
// it does return is a verified true match — and must not be cached or
// otherwise treated as complete. See the filtering-chain comment for the
// full completeness argument.
func (ix *ngramIndex) search(query string, d int) (matches []string, truncated bool) {
	qLen := utf8.RuneCountInString(query)
	grams := distinctQueryGrams(query)
	g := len(grams)
	if g == 0 {
		return nil, false
	}

	// minCommon from the q-gram lemma; when the bound degenerates to ≤ 0
	// (very short queries at distance 2) every list must be walked.
	minCommon := g - ngramQ*d
	if minCommon < 1 {
		minCommon = 1
	}
	walkCount := g - minCommon + 1
	if walkCount > g {
		walkCount = g
	}

	// Rank the query's grams by posting-list length and walk the rarest
	// walkCount of them.
	type gramLen struct {
		gid int32
		n   int32
	}
	ranked := make([]gramLen, 0, g)
	for _, gram := range grams {
		if gid, ok := ix.gramIDs[gram]; ok {
			ranked = append(ranked, gramLen{gid, ix.postLen[gid]})
		}
	}
	// Stable order: shortest lists first, gram id as the tie-break, so the
	// walk — and therefore a budget-truncated result — is reproducible.
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n < ranked[j].n
		}
		return ranked[i].gid < ranked[j].gid
	})
	if len(ranked) < walkCount {
		walkCount = len(ranked) // unknown grams only shrink the walk
	}

	var check levenshteinChecker
	check.prepare(query)

	// The budget counts RAW posting entries read (before dedup, filter, and
	// verification): every unit of downstream work — length filter, seen-set
	// probe, Levenshtein run — happens at most once per entry read, so this
	// single counter bounds the whole walk. Counting verifications instead
	// would leave the dominant cost of degenerate short queries unbounded:
	// measured at prod, adversarial 1–3-rune queries walk up to 4.0M entries
	// but verify at most 69,727 (d2) / 7,886 (d1) — the tail lives in the
	// walk, not the verification. Read once per search, like FuzzyMaxNames;
	// negative means unlimited.
	budget := int64(FuzzyMaxCandidates)
	if budget < 0 {
		budget = math.MaxInt64
	}
	var walked, verified int64

	seen := acquireSeenSet(len(ix.names))
	defer seen.release()
walk:
	for i := 0; i < walkCount; i++ {
		gid := ranked[i].gid
		data := ix.post[ix.postOff[gid]:ix.postOff[gid+1]]
		id := int32(0)
		for len(data) > 0 {
			delta, n := nextPosting(data)
			data = data[n:]
			id += delta
			if walked >= budget {
				truncated = true
				break walk
			}
			walked++

			// Length filter first: an array read, and a property of the id,
			// so an id it rejects is rejected on every list and never needs
			// a dedup mark.
			if delta := int(ix.nameLens[id]) - qLen; delta > d || delta < -d {
				continue
			}
			if seen.testAndSet(id) {
				continue
			}
			verified++
			if check.atMost(ix.names[id], d) {
				matches = append(matches, ix.names[id])
			}
		}
	}

	// Deterministic, closest-first order: callers take the first candidate
	// that resolves in the requested country, so the order IS the answer for
	// ambiguous typos. Edit distance ascending, then name.
	if d > 1 && len(matches) > 1 {
		dist := make(map[string]int, len(matches))
		for _, m := range matches {
			dist[m] = d
			for k := 0; k < d; k++ {
				if check.atMost(m, k) {
					dist[m] = k
					break
				}
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			if dist[matches[i]] != dist[matches[j]] {
				return dist[matches[i]] < dist[matches[j]]
			}
			return matches[i] < matches[j]
		})
	} else {
		sort.Strings(matches)
	}

	fuzzyWalkedLast.Store(walked)
	fuzzyVerifiedLast.Store(verified)
	if truncated {
		fuzzyBudgetTrips.Add(1)
		// One-time summary, mirroring the FuzzyMaxNames disable log: the
		// trip itself is the rare event worth surfacing, per-query logging
		// is not.
		if fuzzyBudgetLogged.CompareAndSwap(false, true) {
			log.Printf("fuzzy search candidate budget reached (%d posting entries); the query returned partial results — raise name.FuzzyMaxCandidates (negative disables the cap) if this workload needs full completeness",
				FuzzyMaxCandidates)
		}
	}
	return matches, truncated
}

// approxBytes reports the approximate resident size of the index's own
// arrays (excluding the name strings, which are shared with the finder), for
// scale gates and logs.
func (ix *ngramIndex) approxBytes() int {
	const mapEntryBytes = 48 // Go map[string]int32 amortized bucket cost, conservative
	return len(ix.post) + len(ix.postOff)*8 + len(ix.postLen)*4 + len(ix.nameLens)*2 + len(ix.gramIDs)*mapEntryBytes
}

// postings returns the total number of posting entries (ids) across grams.
func (ix *ngramIndex) postings() int {
	n := 0
	for _, c := range ix.postLen {
		n += int(c)
	}
	return n
}
