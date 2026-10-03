package name

import (
	"encoding/binary"
	"testing"
)

// TestPostingsLenMatchesEncoding: the exact-size pre-pass agrees with the
// encoder for every varint width, so the posting buffer never regrows and
// never carries slack.
func TestPostingsLenMatchesEncoding(t *testing.T) {
	for _, v := range []uint64{0, 1, 0x7f, 0x80, 0x3fff, 0x4000, 1<<21 - 1, 1 << 21, 1<<28 - 1, 1 << 28, 1<<31 - 1} {
		if got, want := uvarintLen(v), len(binary.AppendUvarint(nil, v)); got != want {
			t.Errorf("uvarintLen(%d) = %d, want %d", v, got, want)
		}
	}
	ids := []int32{0, 1, 2, 130, 20000, 3_000_000, 2_000_000_000}
	if got, want := postingsLen(ids), len(appendPostings(nil, ids)); got != want {
		t.Errorf("postingsLen = %d, want %d", got, want)
	}

	idx, err := buildNGramIndex(budgetTestCorpus())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.post) != cap(idx.post) {
		t.Errorf("posting buffer: len %d, cap %d; it must be sized exactly", len(idx.post), cap(idx.post))
	}
}
