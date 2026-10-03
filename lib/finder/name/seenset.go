package name

import "sync"

// seenSet is the per-search dedup set of the fuzzy walk: one bit per name
// id. It replaces a map[int32]struct{} that grew (and hashed) per posting
// read. Clearing is sparse — only the words this search set are zeroed on
// release — so a short query does not pay to clear a bitset sized for the
// whole index.
type seenSet struct {
	words   []uint64
	touched []int32 // indexes of the non-zero words, for the sparse clear
}

var seenSetPool = sync.Pool{New: func() any { return new(seenSet) }}

// acquireSeenSet returns an empty set able to hold ids in [0, n).
func acquireSeenSet(n int) *seenSet {
	s := seenSetPool.Get().(*seenSet)
	if need := (n + 63) / 64; len(s.words) < need {
		s.words = make([]uint64, need) // the old words are all zero; drop them
	}
	return s
}

// testAndSet marks id and reports whether it was already marked.
func (s *seenSet) testAndSet(id int32) bool {
	w, bit := id>>6, uint64(1)<<(uint(id)&63)
	word := s.words[w]
	if word&bit != 0 {
		return true
	}
	if word == 0 {
		s.touched = append(s.touched, w)
	}
	s.words[w] = word | bit
	return false
}

// release clears the marked words and returns the set to the pool.
func (s *seenSet) release() {
	for _, w := range s.touched {
		s.words[w] = 0
	}
	s.touched = s.touched[:0]
	seenSetPool.Put(s)
}
