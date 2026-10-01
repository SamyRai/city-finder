package routes

import (
	"encoding/json"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autocompleteFixtureCities seeds the name index with names whose sort order
// and prefix grouping the tests below assert on.
var autocompleteFixtureCities = []city.SpatialCity{
	{City: city.City{Name: "Paris", Latitude: 48.8566, Longitude: 2.3522, Country: "FR"}},
	{City: city.City{Name: "Parisdorf", Latitude: 48.2, Longitude: 16.4, Country: "FR"}},
	{City: city.City{Name: "Pars", Latitude: 47.5, Longitude: 15.0, Country: "FR"}},
	{City: city.City{Name: "London", Latitude: 51.5, Longitude: -0.12, Country: "FR"}},
}

func setupAutocompleteApp(t *testing.T) *fiber.App {
	t.Helper()
	s2f, err := coordinates.BuildIndex(autocompleteFixtureCities)
	require.NoError(t, err)
	app := fiber.New()
	SetupRoutes(app, &finder.Finder{
		S2Finder:   s2f,
		NameFinder: name.BuildIndex(autocompleteFixtureCities),
	})
	return app
}

func decodeAutocomplete(t *testing.T, body string) []struct {
	Name string `json:"name"`
} {
	t.Helper()
	var parsed struct {
		Matches []struct {
			Name string `json:"name"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	return parsed.Matches
}

func TestAutocomplete_PrefixSortedAndCapped(t *testing.T) {
	app := setupAutocompleteApp(t)

	status, body := get(t, app, "/autocomplete?name=Par&country-code=FR")
	require.Equal(t, 200, status)
	matches := decodeAutocomplete(t, body)
	// Sorted: Paris, Parisdorf, Pars ("Pars" shares only "Par", not "Pari").
	names := []string{}
	for _, m := range matches {
		names = append(names, m.Name)
	}
	assert.Equal(t, []string{"Paris", "Parisdorf", "Pars"}, names)

	// Explicit limit cuts the set; values outside 1-50 are rejected like
	// every other strict parameter on this API.
	_, body = get(t, app, "/autocomplete?name=Par&country-code=FR&limit=1")
	assert.Len(t, decodeAutocomplete(t, body), 1)
	status, body = get(t, app, "/autocomplete?name=Par&country-code=FR&limit=999")
	assert.Equal(t, 400, status, "limit above 50 is rejected, not clamped")
	assert.Contains(t, body, "Invalid limit")
}

func TestAutocomplete_UnknownCountryAndNoMatchAreEmptyArrays(t *testing.T) {
	app := setupAutocompleteApp(t)

	for _, q := range []string{
		"/autocomplete?name=Par&country-code=DE",
		"/autocomplete?name=ZZZ&country-code=FR",
	} {
		status, body := get(t, app, q)
		require.Equal(t, 200, status, q)
		assert.Contains(t, body, `"matches":[]`, "no matches must serialize as an empty array, not null: %s", q)
	}
}

func TestAutocomplete_Validation(t *testing.T) {
	app := setupAutocompleteApp(t)

	cases := []struct {
		query  string
		status int
		text   string
	}{
		{"", 400, "Name is required"},
		{"?country-code=FR", 400, "Name is required"},
		{"?name=Par", 400, "Country code is required"},
		{"?name=" + repeatRunes(201) + "&country-code=FR", 400, "Name too long"},
		{"?name=Par&country-code=FR&limit=0", 400, "Invalid limit"},
		{"?name=Par&country-code=FR&limit=-3", 400, "Invalid limit"},
		{"?name=Par&country-code=FR&limit=abc", 400, "Invalid limit"},
		{"?name=Par&country-code=FR&limit=51", 400, "Invalid limit"},
	}
	for _, tc := range cases {
		status, body := get(t, app, "/autocomplete"+tc.query)
		assert.Equal(t, tc.status, status, tc.query)
		assert.Contains(t, body, tc.text, tc.query)
	}

	// Boundary acceptance: 200 runes and limit=50 pass validation (and miss).
	status, _ := get(t, app, "/autocomplete?name="+repeatRunes(200)+"&country-code=FR&limit=50")
	assert.Equal(t, 200, status)
}

// repeatRunes builds a run of n 'x' runes without importing strings here.
func repeatRunes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
