package name

import "unicode/utf8"

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

// atMost reports whether levenshtein(candidate, query) ≤ d.
func (c *levenshteinChecker) atMost(candidate string, d int) bool {
	return c.within(candidate, d) <= d
}

// within returns levenshtein(candidate, query) when it is ≤ d, else d+1.
// Rows run along the predecoded query; cells outside the ±d band are
// unreachable at the bound and never computed (a band cell holding a value
// ≤ d is exact). Both the length difference and the row minimum exit as soon
// as the bound is unreachable.
func (c *levenshteinChecker) within(candidate string, d int) int {
	q := c.queryRunes
	n := len(q)

	candidateRunes := utf8.RuneCountInString(candidate)
	if n-candidateRunes > d || candidateRunes-n > d {
		return d + 1
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
			return d + 1
		}
		c.prev, c.curr = c.curr, c.prev
	}
	return min(c.prev[n], d+1)
}
