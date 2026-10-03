package coordinates

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeserializeIndex_ReadsFileFromPreviousBuild loads an index written by
// the code before the header framing moved into lib/indexfile. The header
// field names and types are the wire format, so it must keep loading.
func TestDeserializeIndex_ReadsFileFromPreviousBuild(t *testing.T) {
	f, err := DeserializeIndex("testdata/compat_v3.idx")
	require.NoError(t, err)
	require.Len(t, f.Cities, len(testCities))
	got, _, err := f.NearestPlace(51.5, -0.12, RankDistance)
	require.NoError(t, err)
	require.Equal(t, "London", got.Name)
}
