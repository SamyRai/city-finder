package routes

import (
	"io"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin the CURRENT behaviour at the parameter edges so
// that a change (a stricter number parser, a normalized name) is a deliberate
// decision with a visible diff, not an accident. Rows marked "product
// decision" document behaviour that is lax or surprising but unchanged.

// TestNearest_ParamEdgeCases pins parseCoordinate and the range checks.
func TestNearest_ParamEdgeCases(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	for _, tc := range []struct {
		name   string
		query  string
		status int
		body   string // "" = not asserted
	}{
		// Syntax strconv.ParseFloat accepts beyond plain decimals. Product
		// decision (E10): harmless, but the same point gets several spellings.
		{"hex float", "lat=0x1p3&lon=0", 200, ""},
		{"underscore digits", "lat=1_0&lon=0", 200, ""},
		{"hex with underscore", "lat=0x1_0p0&lon=0", 200, ""},
		{"explicit plus", "lat=%2B52&lon=0", 200, ""},
		{"leading dot", "lat=.5&lon=0", 200, ""},
		{"trailing dot", "lat=5.&lon=0", 200, ""},
		{"underflow is zero", "lat=1e-400&lon=0", 200, ""},
		{"negative zero", "lat=-0&lon=0", 200, ""},
		// Rejections.
		{"overflow", "lat=1e999&lon=0", 400, "Invalid latitude"},
		{"leading space", "lat=%205&lon=0", 400, "Invalid latitude"},
		{"trailing space", "lat=5%20&lon=0", 400, "Invalid latitude"},
		{"empty lat", "lat=&lon=0", 400, "Invalid latitude"},
		{"empty lon", "lat=0&lon=", 400, "Invalid longitude"},
		{"NaN", "lat=NaN&lon=0", 400, "Invalid latitude"},
		{"Inf", "lat=0&lon=Inf", 400, "Invalid longitude"},
		{"just over 90", "lat=90.00000001&lon=0", 400, "Latitude must be between -90 and 90"},
		{"just over 180", "lat=0&lon=180.0000001", 400, "Longitude must be between -180 and 180"},
		{"exact bounds", "lat=-90&lon=180", 200, ""},
		{"rank is case-sensitive", "lat=0&lon=0&rank=POPULATION", 400, "Invalid rank"},
		{"include is case-sensitive", "lat=0&lon=0&include=Admin", 400, "Invalid include"},
		// Repeated parameters: the first value wins silently (product decision).
		{"repeated lat first wins", "lat=37.78&lat=-33&lon=-122.42", 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := get(t, app, "/nearest?"+tc.query)
			assert.Equal(t, tc.status, status, body)
			if tc.body != "" {
				assert.Equal(t, tc.body, body)
			}
		})
	}

	// The first of two repeated lat values is the one used.
	_, first := get(t, app, "/nearest?lat=37.78&lat=-33&lon=-122.42")
	_, only := get(t, app, "/nearest?lat=37.78&lon=-122.42")
	assert.Equal(t, only, first)
}

// TestNearestBatch_NullAndOverflowNumbers pins the decode path of the batch
// body, which classifies errors by json error type / message prefix.
func TestNearestBatch_NullAndOverflowNumbers(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"null lat is a missing lat", `{"points":[{"lat":null,"lon":0}]}`, "points[0]: Invalid latitude"},
		{"null lon is a missing lon", `{"points":[{"lat":0,"lon":null}]}`, "points[0]: Invalid longitude"},
		{"overflowing number", `{"points":[{"lat":1e999,"lon":0}]}`, "invalid JSON body"},
		{"null points", `{"points":null}`, "points must contain at least one entry"},
		{"missing points", `{}`, "points must contain at least one entry"},
		{"null body", `null`, "points must contain at least one entry"},
		{"null element", `{"points":[null]}`, "points[0]: Invalid latitude"},
		{"nested unknown field is named", `{"points":[{"lat":0,"lon":0,"bogus":1}]}`, `json: unknown field "bogus"`},
		{"string for a number", `{"points":[{"lat":"0","lon":0}]}`, "invalid JSON body"},
		{"array body", `[]`, "invalid JSON body"},
		{"BOM body", "\xef\xbb\xbf" + `{"points":[{"lat":0,"lon":0}]}`, "invalid JSON body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := post(t, app, "application/json", tc.body)
			assert.Equal(t, 400, status)
			assert.Equal(t, tc.want, body)
		})
	}

	// Explicit nulls for the optional fields select the defaults.
	status, _ := post(t, app, "application/json",
		`{"points":[{"lat":37.78,"lon":-122.42,"rank":null,"include":null}]}`)
	assert.Equal(t, 200, status)

	// Duplicate keys: the last value wins (encoding/json), so the second lat
	// is the one validated and used.
	status, body := post(t, app, "application/json",
		`{"points":[{"lat":999,"lat":37.78,"lon":-122.42}]}`)
	assert.Equal(t, 200, status, body)
}

// TestValidateBatchPoint_NaNInfDirect covers the non-finite branches JSON
// cannot carry, by calling the validator directly.
func TestValidateBatchPoint_NaNInfDirect(t *testing.T) {
	zero := 0.0
	nan, inf := math.NaN(), math.Inf(1)
	for _, tc := range []struct {
		name string
		p    batchPoint
		want string
	}{
		{"NaN lat", batchPoint{Lat: &nan, Lon: &zero}, "Invalid latitude"},
		{"+Inf lat", batchPoint{Lat: &inf, Lon: &zero}, "Invalid latitude"},
		{"-Inf lat", batchPoint{Lat: ptr(math.Inf(-1)), Lon: &zero}, "Invalid latitude"},
		{"NaN lon", batchPoint{Lat: &zero, Lon: &nan}, "Invalid longitude"},
		{"+Inf lon", batchPoint{Lat: &zero, Lon: &inf}, "Invalid longitude"},
		{"-Inf lon", batchPoint{Lat: &zero, Lon: ptr(math.Inf(-1))}, "Invalid longitude"},
		{"valid", batchPoint{Lat: &zero, Lon: &zero}, ""},
	} {
		_, reason := validateBatchPoint(tc.p)
		assert.Equal(t, tc.want, reason, tc.name)
	}
}

func ptr[T any](v T) *T { return &v }

// TestCoordinatesNameLengthIsRunesNotBytes: the cap counts runes, so 200
// three-byte runes (600 bytes) pass and 201 are rejected.
func TestCoordinatesNameLengthIsRunesNotBytes(t *testing.T) {
	app := setupNameApp(t)

	status, _ := get(t, app, "/coordinates?name="+strings.Repeat("%E6%9D%B1", 200)+"&country-code=US")
	assert.Equal(t, 404, status, "200 runes (600 bytes) pass the cap and miss normally")

	status, body := get(t, app, "/coordinates?name="+strings.Repeat("%E6%9D%B1", 201)+"&country-code=US")
	assert.Equal(t, 400, status)
	assert.Contains(t, body, "Name too long")

	// The same cap guards /autocomplete.
	status, body = get(t, app, "/autocomplete?name="+strings.Repeat("%E6%9D%B1", 201)+"&country-code=US")
	assert.Equal(t, 400, status)
	assert.Contains(t, body, "Name too long")
}

// TestCoordinatesCountryCodeShape pins that the country code is only trimmed
// and upper-cased, never shape-checked: odd values are a normal miss (404),
// not a 400. Product decision (E9).
func TestCoordinatesCountryCodeShape(t *testing.T) {
	app := setupNameApp(t)
	for _, cc := range []string{"D", "DEU", "%00", "u%C3%9F", "us%20"} {
		status, body := get(t, app, "/coordinates?name=San%20Francisco&country-code="+cc)
		if cc == "us%20" {
			assert.Equal(t, 200, status, "trimmed and upper-cased: %s", body)
			continue
		}
		assert.Equal(t, 404, status, "country-code=%q", cc)
	}
}

// TestAutocompleteLimitParsing pins limit: strconv.Atoi syntax, 1-50, empty
// means the default.
func TestAutocompleteLimitParsing(t *testing.T) {
	app := setupNameApp(t)
	for limit, want := range map[string]int{
		"":                     200, // empty = default 10
		"1":                    200,
		"50":                   200,
		"%2B5":                 200, // "+5": Atoi accepts a sign (product decision)
		"05":                   200,
		"0":                    400,
		"-1":                   400,
		"51":                   400,
		"1.5":                  400,
		"abc":                  400,
		"%205":                 400,
		"99999999999999999999": 400, // overflows int
	} {
		status, body := get(t, app, "/autocomplete?name=San&country-code=US&limit="+limit)
		assert.Equal(t, want, status, "limit=%q: %s", limit, body)
	}
	_, body := get(t, app, "/autocomplete?name=San&country-code=US&limit=0")
	assert.Equal(t, "Invalid limit (must be 1-50)", body)
}

// TestMethodsAndRoutes pins the router's answers for wrong methods and
// unknown paths. They also decide the metrics label (see metrics.RouteLabel):
// a 404/405 from the router is "(unrouted)", never a made-up path.
func TestMethodsAndRoutes(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/nearest", 405},
		{"PUT", "/nearest", 405},
		{"GET", "/nearest/batch", 405},
		{"DELETE", "/nearest/batch", 405},
		{"POST", "/healthz", 405},
		{"POST", "/coordinates", 405},
		{"HEAD", "/healthz", 200},
		{"HEAD", "/nearest?lat=37.78&lon=-122.42", 200},
		{"OPTIONS", "/nearest", 405},
		{"GET", "/nope", 404},
		// Fiber's default router is case-insensitive and ignores a trailing
		// slash (product decision), while parameters are case-sensitive.
		{"GET", "/NEAREST?lat=37.78&lon=-122.42", 200},
		{"GET", "/nearest/?lat=37.78&lon=-122.42", 200},
	} {
		resp, err := app.Test(httptest.NewRequest(tc.method, tc.path, nil))
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, tc.want, resp.StatusCode, "%s %s", tc.method, tc.path)
	}

	// A HEAD answer carries the GET headers but no body.
	resp, err := app.Test(httptest.NewRequest("HEAD", "/healthz", nil))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, body, "HEAD has no body")
}
