package routes

import (
	"io"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminRouteCities mirrors the s2 admin oracle set (see
// lib/finder/coordinates/s2_admin_test.go for the GeoNames code sourcing).
var adminRouteCities = []city.SpatialCity{
	{City: city.City{Name: "San Francisco", Latitude: 37.7749, Longitude: -122.4194, Country: "US", Population: 808437}, Admin1Code: "06", Admin2Code: "075"},
	{City: city.City{Name: "Reno", Latitude: 39.5296, Longitude: -119.8138, Country: "US", Population: 264165}, Admin1Code: "32", Admin2Code: "031"},
	{City: city.City{Name: "les Escaldes", Latitude: 42.50729, Longitude: 1.53414, Country: "AD", Population: 16316}, Admin1Code: "07"},
}

var adminRouteNames = map[string]string{
	"US.06": "California",
	"US.32": "Nevada",
	"AD.07": "Andorra la Vella",
}

// setupTestApp builds a Fiber app over an S2 finder constructed exactly as
// the initializer would leave it (BuildIndex + names attached).
func setupTestApp(t *testing.T, names map[string]string) *fiber.App {
	t.Helper()
	app, _ := setupTestHandlers(t, names)
	return app
}

// setupTestHandlers is setupTestApp plus the handlers, for tests that reach
// the population gate.
func setupTestHandlers(t *testing.T, names map[string]string) (*fiber.App, *Handlers) {
	t.Helper()
	s2f, err := coordinates.BuildIndex(adminRouteCities)
	require.NoError(t, err)
	s2f.Admin1Names = names
	app := fiber.New()
	return app, SetupRoutes(app, &finder.Finder{S2Finder: s2f})
}

// get issues a GET and returns status + body.
func get(t *testing.T, app *fiber.App, path string) (int, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", path, nil))
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// TestNearest_DefaultResponseByteIdenticalToV10 pins the wire contract: with
// no include parameter (and with an explicitly empty one), the body is
// byte-for-byte the v1.0 distance-ranked shape — capitalized City fields,
// distance_km, and NOTHING else (no admin keys, no Population).
func TestNearest_DefaultResponseByteIdenticalToV10(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	const v10Bytes = `{"Latitude":37.7749,"Longitude":-122.4194,"Name":"San Francisco","Country":"US","distance_km":0.57}`
	for _, q := range []string{
		"/nearest?lat=37.78&lon=-122.42",
		"/nearest?lat=37.78&lon=-122.42&include=",
	} {
		status, body := get(t, app, q)
		require.Equal(t, 200, status, "query %q", q)
		assert.Equal(t, v10Bytes, body, "query %q: default responses must be byte-identical to v1.0", q)
	}
}

// TestNearest_IncludeAdmin is the primary API gate: include=admin appends
// admin1_code, admin1_name, and admin2_code after distance_km for a US
// coordinate attributing to California.
func TestNearest_IncludeAdmin(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := get(t, app, "/nearest?lat=37.78&lon=-122.42&include=admin")
	require.Equal(t, 200, status)
	assert.JSONEq(t,
		`{"Latitude":37.7749,"Longitude":-122.4194,"Name":"San Francisco","Country":"US",
		  "distance_km":0.57,"admin1_code":"06","admin1_name":"California","admin2_code":"075"}`,
		body)
}

// TestNearest_IncludeAdminCodesOnly: no names dataset (nil map) serves the
// codes with admin1_name omitted — degradation, not failure.
func TestNearest_IncludeAdminCodesOnly(t *testing.T) {
	app := setupTestApp(t, nil)

	status, body := get(t, app, "/nearest?lat=37.78&lon=-122.42&include=admin")
	require.Equal(t, 200, status)
	assert.JSONEq(t,
		`{"Latitude":37.7749,"Longitude":-122.4194,"Name":"San Francisco","Country":"US",
		  "distance_km":0.57,"admin1_code":"06","admin2_code":"075"}`,
		body)
	assert.NotContains(t, body, "admin1_name", "codes-only mode must omit the name key")
}

// TestNearest_IncludeAdminNoAdmin2: a city without admin2 (les Escaldes)
// omits admin2_code rather than emitting an empty value.
func TestNearest_IncludeAdminNoAdmin2(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := get(t, app, "/nearest?lat=42.507&lon=1.534&include=admin")
	require.Equal(t, 200, status)
	assert.JSONEq(t,
		`{"Latitude":42.50729,"Longitude":1.53414,"Name":"les Escaldes","Country":"AD",
		  "distance_km":0.03,"admin1_code":"07","admin1_name":"Andorra la Vella"}`,
		body)
	assert.NotContains(t, body, "admin2", "no-admin2 cities must omit the key entirely")
}

// TestNearest_IncludeAdminWithPopulationRank: the two optional enrichments
// compose — Population (rank=population) plus the admin fields, in that
// order.
func TestNearest_IncludeAdminWithPopulationRank(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := get(t, app, "/nearest?lat=37.78&lon=-122.42&rank=population&include=admin")
	require.Equal(t, 200, status)
	assert.JSONEq(t,
		`{"Latitude":37.7749,"Longitude":-122.4194,"Name":"San Francisco","Country":"US","Population":808437,
		  "distance_km":0.57,"admin1_code":"06","admin1_name":"California","admin2_code":"075"}`,
		body)
}

// TestNearest_IncludeAdminBoundaryCaveat documents the attribution contract:
// the response reports the NEAREST city's region. A point physically closer
// to Reno attributes to Nevada even if it lies inside California —
// nearest-city attribution, not polygon containment.
func TestNearest_IncludeAdminBoundaryCaveat(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := get(t, app, "/nearest?lat=39.1&lon=-119.9&include=admin")
	require.Equal(t, 200, status)
	assert.Contains(t, body, `"admin1_name":"Nevada"`,
		"attribution follows the nearest city, which may sit across the boundary")
}

// TestNearest_InvalidInclude: every value except absent/empty/"admin" is a
// 400 with the plain-text body "Invalid include", in the handler's style.
func TestNearest_InvalidInclude(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	for _, raw := range []string{"foo", "ADMIN", "Admin", "admin ", "true", "1", "all", "admin1"} {
		status, body := get(t, app, "/nearest?lat=37.78&lon=-122.42&include="+url.QueryEscape(raw))
		assert.Equal(t, 400, status, "include=%q must be rejected", raw)
		assert.Equal(t, "Invalid include", body, "include=%q must return the exact plain-text error", raw)
	}
}

// TestNearest_ValidationOrder: include is validated after rank — an invalid
// rank wins when both are wrong (the documented handler order).
func TestNearest_ValidationOrder(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := get(t, app, "/nearest?lat=37.78&lon=-122.42&rank=bogus&include=bogus")
	assert.Equal(t, 400, status)
	assert.Equal(t, "Invalid rank", body, "rank is validated before include")
}
