package coordinates

import (
	"path/filepath"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminFixtureCities is the admin-attribution oracle set. Codes are the real
// GeoNames values for these places:
//   - San Francisco / Sacramento: US admin1 "06" (California), admin2 "075"
//     (City and County of San Francisco) / "067" (Sacramento County).
//   - les Escaldes: AD admin1 "07" (Escaldes-Engordany parish), no admin2 —
//     Andorra has no admin2 division.
//   - Roc Meler: AD admin1 "02" (Canillo), no admin2 — the no-admin2 shape
//     57.7% of dump rows share.
//   - Auckland: NZ admin1 "AUK" (Auckland Region), admin2 empty — a second
//     country whose admin1 code is alphabetic, not numeric.
//
// Boundary accuracy note (design note, "NOT polygon containment"): a query
// point physically inside California but closest to a Nevada city attributes
// to Nevada. The oracle asserts attribution OF THE NEAREST CITY, which is the
// documented contract; the routes tests pin the wording.
var adminFixtureCities = []city.SpatialCity{
	{City: city.City{Name: "San Francisco", Latitude: 37.7749, Longitude: -122.4194, Country: "US"}, Admin1Code: "06", Admin2Code: "075"},
	{City: city.City{Name: "Sacramento", Latitude: 38.5816, Longitude: -121.4944, Country: "US"}, Admin1Code: "06", Admin2Code: "067"},
	{City: city.City{Name: "Reno", Latitude: 39.5296, Longitude: -119.8138, Country: "US"}, Admin1Code: "32", Admin2Code: "031"},
	{City: city.City{Name: "les Escaldes", Latitude: 42.50729, Longitude: 1.53414, Country: "AD"}, Admin1Code: "07"},
	{City: city.City{Name: "Roc Meler", Latitude: 42.58765, Longitude: 1.7418, Country: "AD"}, Admin1Code: "02"},
	{City: city.City{Name: "Auckland", Latitude: -36.8485, Longitude: 174.7633, Country: "NZ"}, Admin1Code: "AUK"},
}

var adminFixtureNames = map[string]string{
	"US.06":  "California",
	"US.32":  "Nevada",
	"AD.07":  "Escaldes-Engordany",
	"AD.02":  "Canillo",
	"NZ.AUK": "Auckland Region",
}

// buildAdminFixtureFinder builds the oracle finder with names attached (the
// initializer's job in production; nil names = codes-only mode is tested
// separately).
func buildAdminFixtureFinder(t *testing.T, names map[string]string) *S2Finder {
	t.Helper()
	f, err := BuildIndex(adminFixtureCities)
	require.NoError(t, err)
	f.AttachAdmin1Names(names)
	return f
}

// TestBuildIndex_AdminTables asserts the interned code tables and per-city
// ids: composite "CC.CODE" keys in first-encounter order, -1 for absent
// codes, one id per distinct pair (Sacramento reuses San Francisco's US.06).
func TestBuildIndex_AdminTables(t *testing.T) {
	f := buildAdminFixtureFinder(t, nil)

	require.Len(t, f.Admin1IDs, len(adminFixtureCities))
	require.Len(t, f.Admin2IDs, len(adminFixtureCities))

	assert.Equal(t, []string{"US.06", "US.32", "AD.07", "AD.02", "NZ.AUK"}, f.Admin1Codes)
	assert.Equal(t, []string{"US.075", "US.067", "US.031"}, f.Admin2Codes)

	assert.Equal(t, []int32{0, 0, 1, 2, 3, 4}, f.Admin1IDs)
	assert.Equal(t, []int32{0, 1, 2, -1, -1, -1}, f.Admin2IDs)
}

// TestNearestPlaceWithAdmin_OracleUS is design gate 1: a US query point in
// California resolves to California with its name, and the raw (not
// composite) admin2 code of the winning city.
func TestNearestPlaceWithAdmin_OracleUS(t *testing.T) {
	f := buildAdminFixtureFinder(t, adminFixtureNames)

	c, dist, attr, err := f.NearestPlaceWithAdmin(37.78, -122.42, RankDistance)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "San Francisco", c.Name)
	assert.Positive(t, dist)
	assert.Equal(t, AdminAttribution{
		Admin1Code: "06",
		Admin1Name: "California",
		Admin2Code: "075",
	}, attr)
}

// TestNearestPlaceWithAdmin_OracleSecondCountry: a second country with a
// non-numeric admin1 code (NZ "AUK") and an alphabetic name round-trips.
func TestNearestPlaceWithAdmin_OracleSecondCountry(t *testing.T) {
	f := buildAdminFixtureFinder(t, adminFixtureNames)

	c, _, attr, err := f.NearestPlaceWithAdmin(-36.85, 174.76, RankDistance)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "Auckland", c.Name)
	assert.Equal(t, AdminAttribution{
		Admin1Code: "AUK",
		Admin1Name: "Auckland Region",
		Admin2Code: "", // NZ row carries no admin2: omitted, not zero-padded
	}, attr)
}

// TestNearestPlaceWithAdmin_NoAdmin2Case: the Andorra rows have admin1 but no
// admin2 — the 57.7%-of-rows shape. The attribution must carry the parish
// with an empty (omittable) admin2.
func TestNearestPlaceWithAdmin_NoAdmin2Case(t *testing.T) {
	f := buildAdminFixtureFinder(t, adminFixtureNames)

	c, _, attr, err := f.NearestPlaceWithAdmin(42.507, 1.534, RankDistance)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "les Escaldes", c.Name)
	assert.Equal(t, AdminAttribution{
		Admin1Code: "07",
		Admin1Name: "Escaldes-Engordany",
		Admin2Code: "",
	}, attr)
}

// TestNearestPlaceWithAdmin_CodesOnlyMode: a nil Admin1Names (the optional
// dataset absent) yields the codes with empty names — degradation, not
// failure.
func TestNearestPlaceWithAdmin_CodesOnlyMode(t *testing.T) {
	f := buildAdminFixtureFinder(t, nil)

	c, _, attr, err := f.NearestPlaceWithAdmin(37.78, -122.42, RankDistance)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, AdminAttribution{Admin1Code: "06", Admin2Code: "075"}, attr)
}

// TestAttachAdmin1Names: the one way names reach a finder. Attaching enables
// display names, attaching nil returns to codes-only mode.
func TestAttachAdmin1Names(t *testing.T) {
	f := buildAdminFixtureFinder(t, nil)
	_, _, attr, err := f.NearestPlaceWithAdmin(37.78, -122.42, RankDistance)
	require.NoError(t, err)
	assert.Empty(t, attr.Admin1Name)

	f.AttachAdmin1Names(adminFixtureNames)
	_, _, attr, err = f.NearestPlaceWithAdmin(37.78, -122.42, RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "California", attr.Admin1Name)

	f.AttachAdmin1Names(nil)
	_, _, attr, err = f.NearestPlaceWithAdmin(37.78, -122.42, RankDistance)
	require.NoError(t, err)
	assert.Empty(t, attr.Admin1Name)
	assert.Equal(t, "06", attr.Admin1Code, "codes survive codes-only mode")
}

// TestNearestPlaceWithAdmin_BoundaryFollowsNearestCity documents the
// attribution contract: a point in far eastern California that is closer to
// Reno attributes to Nevada (US.32). The nearest city's admin wins, not the
// point's polygon containment (design note: "NOT polygon containment").
func TestNearestPlaceWithAdmin_BoundaryFollowsNearestCity(t *testing.T) {
	f := buildAdminFixtureFinder(t, adminFixtureNames)

	// ~120 km east of Sacramento: physically inside California, but Reno is
	// the nearest fixture city.
	c, _, attr, err := f.NearestPlaceWithAdmin(39.1, -119.9, RankDistance)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "Reno", c.Name)
	assert.Equal(t, "32", attr.Admin1Code)
	assert.Equal(t, "Nevada", attr.Admin1Name)
}

// TestNearestPlaceWithAdmin_MatchesPlainNearestPlace: the with-admin variant
// must return the same city and distance as the historical path for both
// ranking modes — attribution rides along, it never changes the winner.
func TestNearestPlaceWithAdmin_MatchesPlainNearestPlace(t *testing.T) {
	f := buildAdminFixtureFinder(t, adminFixtureNames)

	for _, rank := range []Rank{RankDistance, RankPopulation} {
		plainCity, plainDist, err := f.NearestPlace(37.78, -122.42, rank)
		require.NoError(t, err)
		adminCity, adminDist, _, err := f.NearestPlaceWithAdmin(37.78, -122.42, rank)
		require.NoError(t, err)
		assert.Equal(t, plainCity, adminCity, "rank %d: winner must not change", rank)
		assert.InDelta(t, plainDist, adminDist, 1e-12)
	}
}

// TestSerializeDeserialize_AdminRoundTrip is design gate 2 (round-trip with
// pointer/count validation incl. admin arrays): every per-city admin id and
// both code tables survive serialize→deserialize byte-for-byte, and the
// deserialized finder attributes identically to the built one.
func TestSerializeDeserialize_AdminRoundTrip(t *testing.T) {
	built := buildAdminFixtureFinder(t, adminFixtureNames)
	path := filepath.Join(t.TempDir(), "s2index.gob")
	require.NoError(t, built.SerializeIndex(path))

	loaded, err := DeserializeIndex(path)
	require.NoError(t, err)

	assert.Equal(t, built.Admin1Codes, loaded.Admin1Codes)
	assert.Equal(t, built.Admin2Codes, loaded.Admin2Codes)
	assert.Equal(t, built.Admin1IDs, loaded.Admin1IDs)
	assert.Equal(t, built.Admin2IDs, loaded.Admin2IDs)
	// Admin1Names is deliberately not serialized: the initializer re-attaches
	// it per boot, so a warm-loaded finder starts in codes-only mode.
	assert.Nil(t, loaded.Admin1Names)

	// Attaching names after the load restores name serving (the initializer
	// does exactly this on every boot, warm or cold) — the loop below then
	// expects identical attribution end to end.
	loaded.AttachAdmin1Names(adminFixtureNames)

	for _, q := range [][2]float64{{37.78, -122.42}, {-36.85, 174.76}, {42.507, 1.534}, {42.58, 1.74}, {39.1, -119.9}} {
		builtCity, _, err := built.NearestPlace(q[0], q[1], RankDistance)
		require.NoError(t, err)
		loadedCity, _, err := loaded.NearestPlace(q[0], q[1], RankDistance)
		require.NoError(t, err)
		assert.Equal(t, builtCity.Name, loadedCity.Name)

		_, _, builtAttr, err := built.NearestPlaceWithAdmin(q[0], q[1], RankDistance)
		require.NoError(t, err)
		_, _, loadedAttr, err := loaded.NearestPlaceWithAdmin(q[0], q[1], RankDistance)
		require.NoError(t, err)
		assert.Equal(t, builtAttr.Admin1Code, loadedAttr.Admin1Code)
		assert.Equal(t, builtAttr.Admin1Name, loadedAttr.Admin1Name)
		assert.Equal(t, builtAttr.Admin2Code, loadedAttr.Admin2Code)
	}

	_, _, attr, err := loaded.NearestPlaceWithAdmin(37.78, -122.42, RankDistance)
	require.NoError(t, err)
	assert.Equal(t, "California", attr.Admin1Name)
}

// TestDeserializeIndex_AdminArrayLengthMismatch: a frame whose admin arrays
// are not parallel to Cities must be rejected as corrupt — such a file would
// attribute every later city against shifted ids.
func TestDeserializeIndex_AdminArrayLengthMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	writeFramedIndex(t, path,
		fileHeader{Magic: testIndexMagic, Version: testIndexVersion, Count: 2},
		SerializableS2Finder{
			Cities:    []city.City{{Name: "A", Latitude: 1, Longitude: 1}, {Name: "B", Latitude: 2, Longitude: 2}},
			Admin1IDs: []int32{-1}, // one short
			Admin2IDs: []int32{-1, -1},
		})

	_, err := DeserializeIndex(path)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCorruptIndex)
}

// TestDeserializeIndex_AdminIDOutOfBounds: an id pointing outside the code
// table is corruption inside a structurally valid frame; it must be caught by
// the bounds pass, not surface as a panic or a wrong-code answer.
func TestDeserializeIndex_AdminIDOutOfBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s2index.gob")
	writeFramedIndex(t, path,
		fileHeader{Magic: testIndexMagic, Version: testIndexVersion, Count: 1},
		SerializableS2Finder{
			Cities:      []city.City{{Name: "A", Latitude: 1, Longitude: 1}},
			Admin1IDs:   []int32{5}, // only 1 entry in the table below
			Admin2IDs:   []int32{-1},
			Admin1Codes: []string{"US.06"},
		})

	_, err := DeserializeIndex(path)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCorruptIndex)
	assert.Contains(t, err.Error(), "outside")
}

// TestAdminOf_ZeroValueFinder: a finder with no admin arrays (representable,
// though no constructor produces one) attributes nothing instead of
// panicking.
func TestAdminOf_ZeroValueFinder(t *testing.T) {
	f := &S2Finder{Cities: []city.City{{Name: "A"}}}
	assert.Equal(t, AdminAttribution{}, f.adminOf(0))
	assert.Equal(t, AdminAttribution{}, f.adminOf(-1))
	assert.Equal(t, AdminAttribution{}, f.adminOf(99))
}
