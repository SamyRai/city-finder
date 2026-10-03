package postalCode

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeserializeIndex_ReadsFileFromPreviousBuild loads a v4 index written
// by the code before the header framing moved into lib/indexfile. The header
// field names and types are the wire format, so it must keep loading.
func TestDeserializeIndex_ReadsFileFromPreviousBuild(t *testing.T) {
	f, err := DeserializeIndex("testdata/compat_v4.idx")
	require.NoError(t, err)
	require.Equal(t, len(testPostalCodeEntries), f.Len())
	require.False(t, f.LegacyFormat())
	for _, e := range testPostalCodeEntries {
		got := f.CityByPostalCode(e.PostalCode, e.CountryCode)
		require.NotNil(t, got, e.PostalCode)
		require.Equal(t, e.PlaceName, got.Name)
	}
}
