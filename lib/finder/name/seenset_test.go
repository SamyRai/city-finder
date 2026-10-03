package name

import "testing"

// TestSeenSetMarksOnceAndClearsOnRelease checks dedup, word boundaries and
// that a released set comes back empty, including when it must grow.
func TestSeenSetMarksOnceAndClearsOnRelease(t *testing.T) {
	ids := []int32{0, 63, 64, 127, 128, 999, 63}
	for round := range 3 {
		s := acquireSeenSet(1000 + round*1000)
		for i, id := range ids {
			if dup := s.testAndSet(id); dup != (i == len(ids)-1) {
				t.Fatalf("round %d: testAndSet(%d) = %v", round, id, dup)
			}
		}
		if len(s.touched) != 4 { // words 0, 1, 2 and 15
			t.Fatalf("round %d: touched %d words, want 4", round, len(s.touched))
		}
		s.release()
		for w, word := range s.words {
			if word != 0 {
				t.Fatalf("round %d: word %d not cleared", round, w)
			}
		}
	}
}
