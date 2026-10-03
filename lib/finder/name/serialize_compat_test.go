package name

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeserializeIndex_ReadsFileFromPreviousBuild loads a v3 index written by
// the code before the header framing moved into lib/indexfile. The header
// field names and types are the wire format, so it must keep loading.
func TestDeserializeIndex_ReadsFileFromPreviousBuild(t *testing.T) {
	f, err := DeserializeIndex("testdata/compat_v3.idx")
	require.NoError(t, err)
	for name, country := range map[string]string{"Paris": "FR", "Lyon": "FR", "Berlin": "DE"} {
		got := f.CityByName(name, country)
		require.NotNil(t, got, name)
		require.Equal(t, name, got.Name)
	}
}
