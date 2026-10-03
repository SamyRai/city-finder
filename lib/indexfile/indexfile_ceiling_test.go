package indexfile_test

import (
	"bytes"
	"encoding/gob"
	"math/bits"
	"strings"
	"testing"

	"github.com/SamyRai/cityFinder/lib/indexfile"
)

// gobCeiling is encoding/gob's per-message limit (its unexported tooBig),
// spelled out per word size: 1 GiB on 32-bit platforms, 8 GiB on 64-bit.
func gobCeiling() int64 {
	if bits.UintSize == 64 {
		return 8 << 30
	}
	return 1 << 30
}

// TestDefaultBudgetIsGobCeiling pins the default budget to what gob itself
// accepts for one message. A lower default rejects a legitimate payload as
// corrupt, and the initializer then rebuilds it on every boot.
func TestDefaultBudgetIsGobCeiling(t *testing.T) {
	if got, want := indexfile.DefaultMaxPayloadBytes, gobCeiling(); got != want {
		t.Fatalf("DefaultMaxPayloadBytes = %d, want gob's ceiling %d", got, want)
	}
}

// TestGobCeilingMatchesGob checks gobCeiling against gob's behaviour: a
// message length prefix at the ceiling is refused before anything is
// allocated. (One byte below it would allocate the whole message, so only
// the refusing side is probed.)
func TestGobCeilingMatchesGob(t *testing.T) {
	n := uint64(gobCeiling())
	var prefix []byte // gob's unsigned integer encoding of n
	var be []byte
	for v := n; v > 0; v >>= 8 {
		be = append([]byte{byte(v)}, be...)
	}
	prefix = append([]byte{byte(-len(be))}, be...)
	err := gob.NewDecoder(bytes.NewReader(prefix)).Decode(new(int))
	if err == nil || !strings.Contains(err.Error(), "invalid message length") {
		t.Fatalf("a message of exactly the ceiling must be refused as an invalid length, got %v", err)
	}
}
