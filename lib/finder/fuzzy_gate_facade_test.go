package finder

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFuzzyGateRejectionsFacadeNil pins that the gate counter, like the other
// fuzzy diagnostics, reads 0 on a Finder without a name index.
func TestFuzzyGateRejectionsFacadeNil(t *testing.T) {
	var bare Finder
	assert.EqualValues(t, 0, bare.FuzzyGateRejections())
}
