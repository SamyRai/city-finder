package routes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// post issues a POST /nearest/batch with the given Content-Type ("" sends
// none) and body, mirroring the in-package get helper.
func post(t *testing.T, app *fiber.App, contentType, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/nearest/batch", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := app.Test(req)
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// TestNearestBatch_HappyPath: one plain point and one population+admin point
// in a single round trip. Each entry is exactly the object GET /nearest
// returns for that point — the plain entry keeps the v1.0 shape (no
// Population, no admin keys) even though the next point opted in, proving
// rank/include defaults apply per point, not per batch.
func TestNearestBatch_HappyPath(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := post(t, app, "application/json", `{"points":[
		{"lat":37.78,"lon":-122.42},
		{"lat":37.78,"lon":-122.42,"rank":"population","include":"admin"}
	]}`)
	require.Equal(t, 200, status)
	assert.JSONEq(t, `{"results":[
		{"Latitude":37.7749,"Longitude":-122.4194,"Name":"San Francisco","Country":"US","distance_km":0.57},
		{"Latitude":37.7749,"Longitude":-122.4194,"Name":"San Francisco","Country":"US","Population":808437,
		 "distance_km":0.57,"admin1_code":"06","admin1_name":"California","admin2_code":"075"}
	]}`, body)
}

// TestNearestBatch_ExplicitEmptyOptionalsActAsDefault: the JSON equivalent of
// GET's rank=/include= (empty) parameters selects the plain default, not an
// error.
func TestNearestBatch_ExplicitEmptyOptionalsActAsDefault(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := post(t, app, "application/json",
		`{"points":[{"lat":37.78,"lon":-122.42,"rank":"","include":""}]}`)
	require.Equal(t, 200, status)
	assert.JSONEq(t, `{"results":[
		{"Latitude":37.7749,"Longitude":-122.4194,"Name":"San Francisco","Country":"US","distance_km":0.57}
	]}`, body)
}

// TestNearestBatch_NotFoundEntryIsNull pins the batch form of the GET 404: a
// lookup that finds no city is a JSON null entry, keeping the results array
// parallel to points. The real finder cannot produce this outcome end to
// end — coordinates.S2Finder reports "no city found" as an error, which both
// handlers serve as 500 (see TestNearestBatch_FinderErrorServedLikeGet) — so
// the null entry is pinned at the exact construction the handler emits.
func TestNearestBatch_NotFoundEntryIsNull(t *testing.T) {
	body, err := json.Marshal(batchResponse{Results: []*nearestCityResponse{nil}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"results":[null]}`, string(body))
}

// TestNearestBatch_FinderErrorServedLikeGet documents the actual no-match
// behavior over an empty index: the finder reports "no city found" as an
// error, so GET serves 500 (its nearest==nil 404 branch stays defensive) and
// the batch mirrors GET exactly.
func TestNearestBatch_FinderErrorServedLikeGet(t *testing.T) {
	s2f, err := coordinates.BuildIndex(nil)
	require.NoError(t, err)
	app := fiber.New()
	SetupRoutes(app, &finder.Finder{S2Finder: s2f})

	status, body := get(t, app, "/nearest?lat=0&lon=0")
	assert.Equal(t, 500, status)
	assert.Equal(t, "internal server error", body)

	status, body = post(t, app, "application/json", `{"points":[{"lat":0,"lon":0}]}`)
	assert.Equal(t, 500, status)
	assert.Equal(t, "internal server error", body)
}

// TestNearestBatch_InvalidPointReportsIndexAndReason: any invalid point is a
// 400 naming the offending index with the GET error text; validation runs in
// order, so the first failure wins — both across points and within a point.
func TestNearestBatch_InvalidPointReportsIndexAndReason(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"invalid rank on third point",
			`{"points":[{"lat":37.78,"lon":-122.42},{"lat":39.52,"lon":-119.81},{"lat":37.78,"lon":-122.42,"rank":"bogus"}]}`,
			"points[2]: Invalid rank"},
		{"missing lat",
			`{"points":[{"lat":37.78,"lon":-122.42},{"lon":0}]}`,
			"points[1]: Invalid latitude"},
		{"missing lon",
			`{"points":[{"lat":0}]}`,
			"points[0]: Invalid longitude"},
		{"latitude range before rank within a point",
			`{"points":[{"lat":91,"lon":0,"rank":"bogus"}]}`,
			"points[0]: Latitude must be between -90 and 90"},
		{"longitude range",
			`{"points":[{"lat":0,"lon":181}]}`,
			"points[0]: Longitude must be between -180 and 180"},
		{"invalid include",
			`{"points":[{"lat":0,"lon":0,"include":"all"}]}`,
			"points[0]: Invalid include"},
		{"case-sensitive whitelist",
			`{"points":[{"lat":0,"lon":0,"rank":"Population"}]}`,
			"points[0]: Invalid rank"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := post(t, app, "application/json", tc.body)
			assert.Equal(t, 400, status)
			assert.Equal(t, tc.want, body)
		})
	}
}

// TestNearestBatch_SizeLimits: the points array holds 1..100 entries; the
// 100-point boundary itself is served.
func TestNearestBatch_SizeLimits(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	for _, body := range []string{`{"points":[]}`, `{}`} {
		status, respBody := post(t, app, "application/json", body)
		assert.Equal(t, 400, status, "body %q", body)
		assert.Equal(t, "points must contain at least one entry", respBody)
	}

	var b strings.Builder
	b.WriteString(`{"points":[`)
	for i := 0; i < maxBatchPoints+1; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"lat":37.78,"lon":-122.42}`)
	}
	b.WriteString(`]}`)
	status, respBody := post(t, app, "application/json", b.String())
	require.Equal(t, 400, status)
	assert.Equal(t, "points must contain at most 100 entries", respBody)

	// The boundary is legal: exactly maxBatchPoints points are served.
	b.Reset()
	b.WriteString(`{"points":[`)
	for i := 0; i < maxBatchPoints; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"lat":37.78,"lon":-122.42}`)
	}
	b.WriteString(`]}`)
	status, respBody = post(t, app, "application/json", b.String())
	require.Equal(t, 200, status, "body %s", respBody)
	assert.Equal(t, maxBatchPoints, strings.Count(respBody, `"San Francisco"`),
		"the boundary batch returns one entry per point")
}

// TestNearestBatch_MalformedBodyRejected: syntax errors, an empty body,
// trailing garbage after the document, and schema type mismatches are all a
// plain-text 400.
func TestNearestBatch_MalformedBodyRejected(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	for _, body := range []string{
		`{not json`,
		``,
		`{"points":[{"lat":37.78,"lon":-122.42}]} trailing`,
		`{"points":"not-an-array"}`,
		`{"points":[{"lat":"37.78","lon":-122.42}]}`,
	} {
		status, respBody := post(t, app, "application/json", body)
		assert.Equal(t, 400, status, "body %q", body)
		assert.Equal(t, "invalid JSON body", respBody, "body %q", body)
	}
}

// TestNearestBatch_ContentTypeEnforced: like every other parameter on this
// API, the check is exact and case-sensitive — only the bare media type is
// accepted, with no parameters.
func TestNearestBatch_ContentTypeEnforced(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	for _, contentType := range []string{"", "text/plain", "application/json; charset=utf-8"} {
		status, body := post(t, app, contentType, `{"points":[{"lat":37.78,"lon":-122.42}]}`)
		assert.Equal(t, 400, status, "content-type %q", contentType)
		assert.Equal(t, "Content-Type must be application/json", body)
	}
}

// TestNearestBatch_UnknownFieldsRejected: unknown JSON fields are named in
// the 400, wherever they appear.
func TestNearestBatch_UnknownFieldsRejected(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := post(t, app, "application/json",
		`{"points":[{"lat":37.78,"lon":-122.42,"rankk":"distance"}]}`)
	require.Equal(t, 400, status)
	assert.Contains(t, body, `unknown field "rankk"`)

	status, body = post(t, app, "application/json",
		`{"points":[{"lat":37.78,"lon":-122.42}],"limit":2}`)
	require.Equal(t, 400, status)
	assert.Contains(t, body, `unknown field "limit"`)
}

// TestNearestBatch_PopulationSaturationSheds503: a saturated population gate
// mid-batch fails the whole request with 503 + Retry-After (the same shed as
// GET), and the failed request leaks no slot — with the gate drained, the
// same batch succeeds.
func TestNearestBatch_PopulationSaturationSheds503(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	for i := 0; i < populationGateConcurrency(); i++ {
		populationGate <- struct{}{}
	}
	req := httptest.NewRequest("POST", "/nearest/batch", strings.NewReader(
		`{"points":[{"lat":37.78,"lon":-122.42},{"lat":37.78,"lon":-122.42,"rank":"population"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 503, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get("Retry-After"))
	for i := 0; i < populationGateConcurrency(); i++ {
		<-populationGate
	}

	status, _ := post(t, app, "application/json",
		`{"points":[{"lat":37.78,"lon":-122.42},{"lat":37.78,"lon":-122.42,"rank":"population"}]}`)
	assert.Equal(t, 200, status, "with the gate drained the same batch succeeds")
}

// TestNearest_GetUnchangedThroughSharedCore: extracting the batch's query
// core must leave the GET wire contract untouched — the byte-stable v1.0
// default body and the plain-text validation errors.
func TestNearest_GetUnchangedThroughSharedCore(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	status, body := get(t, app, "/nearest?lat=37.78&lon=-122.42")
	require.Equal(t, 200, status)
	assert.Equal(t,
		`{"Latitude":37.7749,"Longitude":-122.4194,"Name":"San Francisco","Country":"US","distance_km":0.57}`,
		body)

	status, body = get(t, app, "/nearest?lat=abc&lon=0")
	assert.Equal(t, 400, status)
	assert.Equal(t, "Invalid latitude", body)
}

// TestNearestBatch_ParallelOrderStable pins the concurrent execution path:
// a mixed 60-point batch (distance, population, admin, ocean, duplicate
// points) must return results parallel to the request, each byte-identical
// to the GET response for the same point, regardless of completion order.
func TestNearestBatch_ParallelOrderStable(t *testing.T) {
	app := setupTestApp(t, adminRouteNames)

	type point struct {
		lat, lon float64
		rank     string
		include  string
	}
	points := make([]point, 0, 60)
	coords := [][2]float64{
		{37.7749, -122.4194}, {39.5296, -119.8138}, {42.50729, 1.53414},
		{0, -140}, {10, 90}, {-55, -120},
	}
	for i := 0; i < 60; i++ {
		c := coords[i%len(coords)]
		p := point{lat: c[0], lon: c[1]}
		switch i % 4 {
		case 1:
			p.rank = "population"
		case 2:
			p.include = "admin"
		case 3:
			p.rank, p.include = "population", "admin"
		}
		points = append(points, p)
	}

	var body bytes.Buffer
	body.WriteString(`{"points":[`)
	for i, p := range points {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"lat":%g,"lon":%g`, p.lat, p.lon)
		if p.rank != "" {
			fmt.Fprintf(&body, `,"rank":%q`, p.rank)
		}
		if p.include != "" {
			fmt.Fprintf(&body, `,"include":%q`, p.include)
		}
		body.WriteByte('}')
	}
	body.WriteString(`]}`)

	req := httptest.NewRequest("POST", "/nearest/batch", &body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, 30000)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	var parsed struct {
		Results []json.RawMessage `json:"results"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&parsed))
	require.Len(t, parsed.Results, len(points))

	for i, raw := range parsed.Results {
		p := points[i]
		query := fmt.Sprintf("/nearest?lat=%g&lon=%g", p.lat, p.lon)
		if p.rank != "" {
			query += "&rank=" + p.rank
		}
		if p.include != "" {
			query += "&include=" + p.include
		}
		st, bodyStr := get(t, app, query)
		require.Equal(t, 200, st, "point %d: %s", i, query)
		assert.JSONEq(t, bodyStr, string(raw),
			"point %d must equal the GET response for the same query", i)
	}
}
