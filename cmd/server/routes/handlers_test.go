package routes

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullTestApp serves all three indexes over a small fixture.
func fullTestApp(t *testing.T) *fiber.App {
	t.Helper()
	cities := []city.SpatialCity{
		{City: city.City{Name: "Berlin", Country: "DE", Latitude: 52.52, Longitude: 13.405}, AltNames: []string{"Berlino"}},
		{City: city.City{Name: "Bern", Country: "CH", Latitude: 46.948, Longitude: 7.4474}},
		{City: city.City{Name: "London", Country: "GB", Latitude: 51.5074, Longitude: -0.1278}},
	}
	s2f, err := coordinates.BuildIndex(cities)
	require.NoError(t, err)
	postal := postalCode.BuildIndex(map[string]map[string]dataLoader.PostalCodeEntry{
		"GB": {"SW1A 1AA": {Latitude: 51.501, Longitude: -0.1416, PlaceName: "London"}},
		"DE": {"10115": {Latitude: 52.532, Longitude: 13.3849, PlaceName: "Berlin"}},
	})
	app := fiber.New()
	SetupRoutes(app, &finder.Finder{S2Finder: s2f, NameFinder: name.BuildIndex(cities), PostalCodeFinder: postal})
	return app
}

func decodeCity(t *testing.T, body string) city.City {
	t.Helper()
	var c city.City
	require.NoError(t, json.Unmarshal([]byte(body), &c), body)
	return c
}

func TestPostalCodeRoute(t *testing.T) {
	app := fullTestApp(t)

	// Surrounding whitespace is trimmed, inner spaces are kept, and the
	// country code is case-insensitive.
	for _, q := range []string{
		"/postalCode?code=SW1A%201AA&country-code=GB",
		"/postalCode?code=%20SW1A%201AA%20&country-code=gb",
	} {
		status, body := get(t, app, q)
		require.Equal(t, 200, status, q)
		c := decodeCity(t, body)
		assert.Equal(t, "London", c.Name, q)
		assert.InDelta(t, 51.501, c.Latitude, 1e-9, q)
	}

	for q, want := range map[string]string{
		"/postalCode?country-code=GB":          "Postal code is required",
		"/postalCode?code=%20&country-code=GB": "Postal code is required",
		"/postalCode?code=10115":               "Country code is required",
	} {
		status, body := get(t, app, q)
		assert.Equal(t, 400, status, q)
		assert.Equal(t, want, body, q)
	}

	for _, q := range []string{
		"/postalCode?code=SW1A1AA&country-code=GB", // the inner space matters
		"/postalCode?code=10115&country-code=GB",   // right code, wrong country
		"/postalCode?code=10115&country-code=ZZ",
	} {
		status, body := get(t, app, q)
		assert.Equal(t, 404, status, q)
		assert.Equal(t, "City not found", body, q)
	}
}

func TestCoordinatesRoute(t *testing.T) {
	app := fullTestApp(t)

	for q, wantName := range map[string]string{
		"/coordinates?name=Berlin&country-code=DE":       "Berlin",
		"/coordinates?name=%20Berlin%20&country-code=de": "Berlin",
		"/coordinates?name=Berlino&country-code=DE":      "Berlin", // alternate name
		"/coordinates?name=London&country-code=GB":       "London",
	} {
		status, body := get(t, app, q)
		require.Equal(t, 200, status, q)
		assert.Equal(t, wantName, decodeCity(t, body).Name, q)
	}

	long := strings.Repeat("x", maxNameRunes+1)
	for q, want := range map[string]string{
		"/coordinates?country-code=DE":                   "Name is required",
		"/coordinates?name=%20&country-code=DE":          "Name is required",
		"/coordinates?name=Berlin":                       "Country code is required",
		"/coordinates?name=" + long + "&country-code=DE": "Name too long",
	} {
		status, body := get(t, app, q)
		assert.Equal(t, 400, status, q)
		assert.Contains(t, body, want, q)
	}

	for _, q := range []string{
		"/coordinates?name=Berlin&country-code=GB", // right name, wrong country
		"/coordinates?name=Nowhere&country-code=ZZ",
	} {
		status, body := get(t, app, q)
		assert.Equal(t, 404, status, q)
		assert.Equal(t, "City not found", body, q)
	}
}

func TestNearestRouteValidation(t *testing.T) {
	app := fullTestApp(t)
	for q, want := range map[string]string{
		"/nearest?lat=0":                 "Invalid longitude",
		"/nearest?lat=0&lon=abc":         "Invalid longitude",
		"/nearest?lat=NaN&lon=0":         "Invalid latitude",
		"/nearest?lat=0&lon=Inf":         "Invalid longitude",
		"/nearest?lat=90.5&lon=0":        "Latitude must be between -90 and 90",
		"/nearest?lat=-91&lon=0":         "Latitude must be between -90 and 90",
		"/nearest?lat=0&lon=180.01":      "Longitude must be between -180 and 180",
		"/nearest?lat=0&lon=-181":        "Longitude must be between -180 and 180",
		"/nearest?lat=0&lon=0&rank=x":    "Invalid rank",
		"/nearest?lat=0&lon=0&include=x": "Invalid include",
	} {
		status, body := get(t, app, q)
		assert.Equal(t, 400, status, q)
		assert.Equal(t, want, body, q)
	}
	// The range edges themselves are valid.
	for _, q := range []string{"/nearest?lat=90&lon=180", "/nearest?lat=-90&lon=-180"} {
		status, _ := get(t, app, q)
		assert.Equal(t, 200, status, q)
	}
}

func TestAutocompleteRouteValidation(t *testing.T) {
	app := fullTestApp(t)

	status, body := get(t, app, "/autocomplete?name=Ber&country-code=de&limit=5")
	require.Equal(t, 200, status)
	assert.JSONEq(t, `{"matches":[{"name":"Berlin","city":{"Latitude":52.52,"Longitude":13.405,"Name":"Berlin","Country":"DE"}},{"name":"Berlino","city":{"Latitude":52.52,"Longitude":13.405,"Name":"Berlin","Country":"DE"}}]}`, body)

	status, body = get(t, app, "/autocomplete?name=Zzz&country-code=DE")
	require.Equal(t, 200, status)
	assert.JSONEq(t, `{"matches":[]}`, body, "no match is an empty list, not null")

	for q, want := range map[string]string{
		"/autocomplete?country-code=DE":                   "Name is required",
		"/autocomplete?name=Ber":                          "Country code is required",
		"/autocomplete?name=Ber&country-code=DE&limit=0":  "Invalid limit (must be 1-50)",
		"/autocomplete?name=Ber&country-code=DE&limit=51": "Invalid limit (must be 1-50)",
		"/autocomplete?name=Ber&country-code=DE&limit=x":  "Invalid limit (must be 1-50)",
	} {
		status, body := get(t, app, q)
		assert.Equal(t, 400, status, q)
		assert.Equal(t, want, body, q)
	}
}

// TestHealthzBody pins the static liveness response.
func TestHealthzBody(t *testing.T) {
	app := fullTestApp(t)
	resp, err := app.Test(httptest.NewRequest("GET", "/healthz", nil))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, fiber.MIMEApplicationJSON, resp.Header.Get(fiber.HeaderContentType))
	status, body := get(t, app, "/healthz")
	assert.Equal(t, 200, status)
	assert.Equal(t, `{"status":"ok"}`, body)
}
