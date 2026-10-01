package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/cmd/server/routes"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/initializer"
	"github.com/SamyRai/cityFinder/util"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ServerTestSuite struct {
	suite.Suite
	app    *fiber.App
	finder *finder.Finder
}

// SetupSuite builds the test indexes in a per-suite temp directory instead of
// overwriting the committed testdata/*.gob files. Only the source .txt
// datasets are read from testdata/; every index write lands in the temp dir.
func (suite *ServerTestSuite) SetupSuite() {
	rootDir, err := util.FindProjectRoot()
	require.NoError(suite.T(), err)

	cfg, err := config.LoadConfig("cmd/server/config_test.json")
	require.NoError(suite.T(), err)

	dataDir := suite.T().TempDir()
	cfg.DatasetsFolder = dataDir
	for _, name := range []string{cfg.AllCitiesFile, cfg.PostalCodesFile} {
		require.NoError(suite.T(), copyFile(filepath.Join(rootDir, "testdata", name), filepath.Join(dataDir, name)))
	}

	suite.finder, err = initializer.Initialize(cfg)
	require.NoError(suite.T(), err)

	app := fiber.New()
	routes.SetupRoutes(app, suite.finder)
	suite.app = app
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	writeErr := func() error {
		_, err := io.Copy(out, in)
		return err
	}()
	closeErr := out.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// fixtureCity loads a city from testdata/allCountries.txt by exact name and
// country. A missing fixture is a test bug and fails the suite.
func (suite *ServerTestSuite) fixtureCity(name, countryCode string) city.City {
	rootDir, err := util.FindProjectRoot()
	require.NoError(suite.T(), err)

	data, err := os.ReadFile(filepath.Join(rootDir, "testdata", "allCountries.txt"))
	require.NoError(suite.T(), err)

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 9 || fields[1] != name || fields[8] != countryCode {
			continue
		}
		lat, err := strconv.ParseFloat(fields[4], 64)
		require.NoError(suite.T(), err)
		lon, err := strconv.ParseFloat(fields[5], 64)
		require.NoError(suite.T(), err)
		return city.City{Latitude: lat, Longitude: lon, Name: fields[1], Country: fields[8]}
	}
	require.Failf(suite.T(), "fixture missing from testdata/allCountries.txt", "name=%q country=%q", name, countryCode)
	return city.City{}
}

// fixturePostal loads a postal code record from testdata/zipCodes.txt by exact
// country and code. A missing fixture is a test bug and fails the suite.
func (suite *ServerTestSuite) fixturePostal(countryCode, code string) (placeName string, lat, lon float64) {
	rootDir, err := util.FindProjectRoot()
	require.NoError(suite.T(), err)

	file, err := os.Open(filepath.Join(rootDir, "testdata", "zipCodes.txt"))
	require.NoError(suite.T(), err)
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = '\t'
	records, err := reader.ReadAll()
	require.NoError(suite.T(), err)

	for _, record := range records {
		if len(record) < 12 || record[0] != countryCode || record[1] != code {
			continue
		}
		lat, err := strconv.ParseFloat(record[9], 64)
		require.NoError(suite.T(), err)
		lon, err := strconv.ParseFloat(record[10], 64)
		require.NoError(suite.T(), err)
		return record[2], lat, lon
	}
	require.Failf(suite.T(), "fixture missing from testdata/zipCodes.txt", "country=%q code=%q", countryCode, code)
	return "", 0, 0
}

// doGet issues a request through the app under test and returns the response
// together with its fully-read body.
func (suite *ServerTestSuite) doGet(rawURL string) (*http.Response, string) {
	req := httptest.NewRequest("GET", rawURL, nil)
	resp, err := suite.app.Test(req, -1)
	require.NoError(suite.T(), err)

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(suite.T(), err)
	return resp, string(bodyBytes)
}

// query builds a query string with each parameter value escaped separately
// (the previous suite escaped the whole string into one opaque blob, which
// made every parameter invisible to the server).
func query(params map[string]string) string {
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	return values.Encode()
}

// jsonKeys unmarshals a JSON object body and returns its sorted top-level
// keys, locking response shapes against accidental field changes.
func jsonKeys(t *testing.T, body string) []string {
	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &obj))
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var (
	// cityKeys are the exact top-level keys city.City marshals to (it has no json
	// tags, so the exported field names appear verbatim).
	cityKeys = []string{"Country", "Latitude", "Longitude", "Name"}

	// nearestDistanceKeys is the exact key set of a distance-ranked /nearest
	// body: the city keys plus distance_km, and nothing else — Population
	// must stay absent so default-rank responses remain byte-compatible with
	// the pre-ranking API.
	nearestDistanceKeys = append(append([]string{}, cityKeys...), "distance_km")

	// nearestPopulationKeys is the key set of a population-ranked body: the
	// distance shape plus the winner's Population (ASCII-sorted, so the
	// capitalized Population precedes distance_km).
	nearestPopulationKeys = append(append([]string{}, cityKeys...), "Population", "distance_km")
)

func (suite *ServerTestSuite) TestHealthz() {
	resp, body := suite.doGet("/healthz")
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)
	assert.Equal(suite.T(), "application/json", resp.Header.Get("Content-Type"), body)
	assert.Equal(suite.T(), `{"status":"ok"}`, body)
}

func (suite *ServerTestSuite) TestNearestKnownGood() {
	fixture := suite.fixtureCity("Xixerella", "AD")

	resp, body := suite.doGet("/nearest?" + query(map[string]string{
		"lat": strconv.FormatFloat(fixture.Latitude, 'f', -1, 64),
		"lon": strconv.FormatFloat(fixture.Longitude, 'f', -1, 64),
	}))
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)

	// The /nearest envelope keeps city.City's four capitalized keys and adds
	// exactly one new key, distance_km (ASCII-sorted after the capitalized
	// names).
	assert.Equal(suite.T(),
		append(append([]string{}, cityKeys...), "distance_km"), jsonKeys(suite.T(), body), body)

	var got struct {
		city.City
		DistanceKm float64 `json:"distance_km"`
	}
	err := json.Unmarshal([]byte(body), &got)
	assert.NoError(suite.T(), err, body)
	assert.Equal(suite.T(), "AD", got.Country, body)
	assert.NotEmpty(suite.T(), got.Name, body)
	// Querying at the city's exact coordinates: the great-circle distance is
	// zero to floating-point precision, which rounds to 0.00.
	assert.Equal(suite.T(), 0.0, got.DistanceKm, body)
}

func (suite *ServerTestSuite) TestNearestDistance() {
	fixture := suite.fixtureCity("Xixerella", "AD")

	// ~1 km due north of the fixture city: one degree of latitude is
	// pi/180 * 6371 km on the same sphere the finder's distances use. The
	// next-nearest fixture city is over 3 km away, so the nearest result is
	// the fixture city at ~1 km.
	kmPerDegreeLat := math.Pi * 6371.0 / 180.0
	lat := fixture.Latitude + 1.0/kmPerDegreeLat
	resp, body := suite.doGet("/nearest?" + query(map[string]string{
		"lat": strconv.FormatFloat(lat, 'f', -1, 64),
		"lon": strconv.FormatFloat(fixture.Longitude, 'f', -1, 64),
	}))
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)

	var got struct {
		city.City
		DistanceKm float64 `json:"distance_km"`
	}
	err := json.Unmarshal([]byte(body), &got)
	require.NoError(suite.T(), err, body)
	assert.Equal(suite.T(), fixture.Name, got.Name, body)

	assert.Greater(suite.T(), got.DistanceKm, 0.5, body)
	assert.Less(suite.T(), got.DistanceKm, 2.0, body)

	// distance_km must agree with an independent haversine computation
	// against the returned city's own coordinates, rounded to 2 decimals.
	expected := math.Round(city.HaversineDistance(lat, fixture.Longitude, got.Latitude, got.Longitude)*100) / 100
	assert.Equal(suite.T(), expected, got.DistanceKm, body)
}

func (suite *ServerTestSuite) TestNearestBadInput() {
	cases := []struct {
		name           string
		params         map[string]string
		expectedStatus int
		expectedBody   string
	}{
		{"missing lat", map[string]string{"lon": "1.5"}, http.StatusBadRequest, "Invalid latitude"},
		{"missing lon", map[string]string{"lat": "42.5"}, http.StatusBadRequest, "Invalid longitude"},
		{"non-numeric lat", map[string]string{"lat": "abc", "lon": "1.5"}, http.StatusBadRequest, "Invalid latitude"},
		{"non-numeric lon", map[string]string{"lat": "42.5", "lon": "xyz"}, http.StatusBadRequest, "Invalid longitude"},
		{"lat above range", map[string]string{"lat": "90.1", "lon": "1.5"}, http.StatusBadRequest, "Latitude must be between -90 and 90"},
		{"lat below range", map[string]string{"lat": "-91", "lon": "1.5"}, http.StatusBadRequest, "Latitude must be between -90 and 90"},
		{"lon above range", map[string]string{"lat": "42.5", "lon": "180.1"}, http.StatusBadRequest, "Longitude must be between -180 and 180"},
		{"lon below range", map[string]string{"lat": "42.5", "lon": "-181"}, http.StatusBadRequest, "Longitude must be between -180 and 180"},
		{"lat NaN", map[string]string{"lat": "NaN", "lon": "1.5"}, http.StatusBadRequest, "Invalid latitude"},
		{"lon NaN", map[string]string{"lat": "42.5", "lon": "NaN"}, http.StatusBadRequest, "Invalid longitude"},
		{"both NaN", map[string]string{"lat": "NaN", "lon": "NaN"}, http.StatusBadRequest, "Invalid latitude"},
		{"lat +Inf", map[string]string{"lat": "Inf", "lon": "1.5"}, http.StatusBadRequest, "Invalid latitude"},
		{"lat -Inf", map[string]string{"lat": "-Inf", "lon": "1.5"}, http.StatusBadRequest, "Invalid latitude"},
		{"lon +Inf", map[string]string{"lat": "42.5", "lon": "Inf"}, http.StatusBadRequest, "Invalid longitude"},
		{"lon -Inf", map[string]string{"lat": "42.5", "lon": "-Inf"}, http.StatusBadRequest, "Invalid longitude"},
		{"lat infinity word", map[string]string{"lat": "infinity", "lon": "1.5"}, http.StatusBadRequest, "Invalid latitude"},
	}

	for _, tc := range cases {
		suite.Run(tc.name, func() {
			resp, body := suite.doGet("/nearest?" + query(tc.params))
			assert.Equal(suite.T(), tc.expectedStatus, resp.StatusCode, body)
			assert.Equal(suite.T(), tc.expectedBody, body)
		})
	}
}

func (suite *ServerTestSuite) TestNearestRankParam() {
	fixture := suite.fixtureCity("Xixerella", "AD")
	base := map[string]string{
		"lat": strconv.FormatFloat(fixture.Latitude, 'f', -1, 64),
		"lon": strconv.FormatFloat(fixture.Longitude, 'f', -1, 64),
	}

	// Valid ranks: absent, explicitly empty, and "distance" all mean the
	// default distance ranking and must keep the pre-ranking response shape.
	validRanks := []struct{ name, rank string }{
		{"absent", ""},
		{"empty", ""},
		{"explicit distance", "distance"},
	}
	for _, tc := range validRanks {
		suite.Run("rank "+tc.name, func() {
			params := map[string]string{}
			for k, v := range base {
				params[k] = v
			}
			if tc.name != "absent" {
				params["rank"] = tc.rank
			}
			resp, body := suite.doGet("/nearest?" + query(params))
			assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)
			assert.Equal(suite.T(), nearestDistanceKeys, jsonKeys(suite.T(), body), body)

			var got struct {
				city.City
				DistanceKm float64 `json:"distance_km"`
				Population *int32  `json:"Population"`
			}
			require.NoError(suite.T(), json.Unmarshal([]byte(body), &got), body)
			assert.Nil(suite.T(), got.Population, "distance-ranked body must not carry Population: %s", body)
			assert.Equal(suite.T(), fixture.Name, got.Name, body)
		})
	}

	suite.Run("rank population", func() {
		params := map[string]string{}
		for k, v := range base {
			params[k] = v
		}
		params["rank"] = "population"
		resp, body := suite.doGet("/nearest?" + query(params))
		assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)
		assert.Equal(suite.T(), nearestPopulationKeys, jsonKeys(suite.T(), body), body)

		var got struct {
			city.City
			DistanceKm float64 `json:"distance_km"`
			Population *int32  `json:"Population"`
		}
		require.NoError(suite.T(), json.Unmarshal([]byte(body), &got), body)
		require.NotNil(suite.T(), got.Population, "population-ranked body must carry Population: %s", body)
		assert.Equal(suite.T(), fixture.Name, got.Name, body)
		// The population value must be the winner's actual Population int32.
		// Every row of the fixture dataset carries population 0 (verified:
		// no nonzero field 15 exists in testdata/allCountries.txt), so the
		// winner's actual value here is 0. Weighted selection semantics are
		// covered by the coordinates package's gravity-oracle suite.
		assert.EqualValues(suite.T(), int32(0), *got.Population, body)
	})

	// Invalid ranks: anything but the exact lowercase tokens is rejected.
	for _, rank := range []string{"foo", "DISTANCE", "Population", " distance", "distance "} {
		suite.Run("rank invalid "+strconv.Quote(rank), func() {
			params := map[string]string{}
			for k, v := range base {
				params[k] = v
			}
			params["rank"] = rank
			resp, body := suite.doGet("/nearest?" + query(params))
			assert.Equal(suite.T(), http.StatusBadRequest, resp.StatusCode, body)
			assert.Equal(suite.T(), "Invalid rank", body)
		})
	}
}

func (suite *ServerTestSuite) TestCoordinatesKnownGood() {
	fixture := suite.fixtureCity("Xixerella", "AD")

	resp, body := suite.doGet("/coordinates?" + query(map[string]string{
		"name":         fixture.Name,
		"country-code": fixture.Country,
	}))
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)

	var got city.City
	err := json.Unmarshal([]byte(body), &got)
	assert.NoError(suite.T(), err, body)
	assert.Equal(suite.T(), fixture.Name, got.Name, body)
	assert.Equal(suite.T(), fixture.Country, got.Country, body)
	// Shape unchanged: exactly city.City's four keys, no distance_km.
	assert.Equal(suite.T(), cityKeys, jsonKeys(suite.T(), body), body)
}

func (suite *ServerTestSuite) TestCoordinatesNormalization() {
	fixture := suite.fixtureCity("Xixerella", "AD")

	// Surrounding whitespace is trimmed; country code is trimmed and uppercased.
	resp, body := suite.doGet("/coordinates?" + query(map[string]string{
		"name":         "  " + fixture.Name + "  ",
		"country-code": " ad ",
	}))
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)

	var got city.City
	err := json.Unmarshal([]byte(body), &got)
	assert.NoError(suite.T(), err, body)
	assert.Equal(suite.T(), fixture.Name, got.Name, body)
}

func (suite *ServerTestSuite) TestCoordinatesBadInput() {
	cases := []struct {
		name           string
		params         map[string]string
		expectedStatus int
		expectedBody   string
	}{
		{"missing name", map[string]string{"country-code": "AD"}, http.StatusBadRequest, "Name is required"},
		{"missing country code", map[string]string{"name": "Xixerella"}, http.StatusBadRequest, "Country code is required"},
		{"whitespace-only name", map[string]string{"name": "   ", "country-code": "AD"}, http.StatusBadRequest, "Name is required"},
	}

	for _, tc := range cases {
		suite.Run(tc.name, func() {
			resp, body := suite.doGet("/coordinates?" + query(tc.params))
			assert.Equal(suite.T(), tc.expectedStatus, resp.StatusCode, body)
			assert.Equal(suite.T(), tc.expectedBody, body)
		})
	}
}

func (suite *ServerTestSuite) TestPostalCodeKnownGood() {
	placeName, _, _ := suite.fixturePostal("AD", "AD100")

	resp, body := suite.doGet("/postalCode?" + query(map[string]string{
		"code":         "AD100",
		"country-code": "AD",
	}))
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)

	var got city.City
	err := json.Unmarshal([]byte(body), &got)
	assert.NoError(suite.T(), err, body)
	assert.Equal(suite.T(), placeName, got.Name, body)
	assert.Equal(suite.T(), "AD", got.Country, body)
	// Shape unchanged: exactly city.City's four keys, no distance_km.
	assert.Equal(suite.T(), cityKeys, jsonKeys(suite.T(), body), body)
}

func (suite *ServerTestSuite) TestPostalCodeNormalization() {
	placeName, _, _ := suite.fixturePostal("AD", "AD100")

	// Surrounding whitespace trimmed on the code, country code trimmed and
	// uppercased. Inner spaces inside postal codes must be preserved.
	resp, body := suite.doGet("/postalCode?" + query(map[string]string{
		"code":         " AD100 ",
		"country-code": "ad",
	}))
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)

	var got city.City
	err := json.Unmarshal([]byte(body), &got)
	assert.NoError(suite.T(), err, body)
	assert.Equal(suite.T(), placeName, got.Name, body)
}

func (suite *ServerTestSuite) TestPostalCodeBadInput() {
	cases := []struct {
		name           string
		params         map[string]string
		expectedStatus int
		expectedBody   string
	}{
		{"missing code", map[string]string{"country-code": "AD"}, http.StatusBadRequest, "Postal code is required"},
		{"missing country code", map[string]string{"code": "AD100"}, http.StatusBadRequest, "Country code is required"},
		{"whitespace-only code", map[string]string{"code": "   ", "country-code": "AD"}, http.StatusBadRequest, "Postal code is required"},
	}

	for _, tc := range cases {
		suite.Run(tc.name, func() {
			resp, body := suite.doGet("/postalCode?" + query(tc.params))
			assert.Equal(suite.T(), tc.expectedStatus, resp.StatusCode, body)
			assert.Equal(suite.T(), tc.expectedBody, body)
		})
	}
}

func (suite *ServerTestSuite) TestPostalCodeNotFound() {
	cases := []struct {
		name   string
		params map[string]string
	}{
		{"unknown code", map[string]string{"code": "ZZZZ", "country-code": "AD"}},
		// Inner spaces are significant: GeoNames postal codes such as GB
		// "SW1A 1AA" must be matched exactly, never space-stripped. "AD 100"
		// only exists if the server strips inner spaces, so it must 404.
		{"inner space preserved", map[string]string{"code": "AD 100", "country-code": "AD"}},
	}

	for _, tc := range cases {
		suite.Run(tc.name, func() {
			resp, body := suite.doGet("/postalCode?" + query(tc.params))
			assert.Equal(suite.T(), http.StatusNotFound, resp.StatusCode, body)
			assert.Equal(suite.T(), "City not found", body)
		})
	}
}

func TestServerTestSuite(t *testing.T) {
	suite.Run(t, new(ServerTestSuite))
}

// TestServerGracefulShutdownSignal builds the real server binary, starts it
// on a random port with the fixture datasets, waits for /healthz over real
// HTTP, sends SIGTERM, and asserts a clean exit 0 within a few seconds.
// syncBuffer guards a child process's output: os/exec drains Stdout/Stderr
// from goroutines that live until Wait returns, while the signal test
// snapshots the logs while the server is still running.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServerGracefulShutdownSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("process-level signal test skipped in short mode")
	}

	rootDir, err := util.FindProjectRoot()
	require.NoError(t, err)

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "cityfinder-server")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/server")
	build.Dir = rootDir
	if out, err := build.CombinedOutput(); err != nil {
		require.NoError(t, err, "go build failed: %s", out)
	}

	// The binary resolves both CONFIG_PATH and datasets_folder relative to
	// the project root (config.LoadConfig joins them with FindProjectRoot()
	// output), so the temp dirs must be referenced root-relative.
	relToRoot := func(path string) string {
		rel, err := filepath.Rel(rootDir, path)
		require.NoError(t, err)
		return rel
	}

	dataDir := t.TempDir()
	for _, name := range []string{"allCountries.txt", "zipCodes.txt"} {
		require.NoError(t, copyFile(filepath.Join(rootDir, "testdata", name), filepath.Join(dataDir, name)))
	}
	cfg := config.Config{
		DatasetsFolder:      relToRoot(dataDir),
		AllCitiesFile:       "allCountries.txt",
		PostalCodesFile:     "zipCodes.txt",
		NameIndexFile:       "name_index_proc_test.gob",
		PostalCodeIndexFile: "postal_code_index_proc_test.gob",
		S2:                  config.S2{MinLevel: 10, MaxLevel: 15, MaxCells: 8, IndexFile: "s2index_proc_test.gob"},
	}
	cfgBytes, err := json.Marshal(cfg)
	require.NoError(t, err)
	cfgPath := filepath.Join(binDir, "config_proc_test.json")
	require.NoError(t, os.WriteFile(cfgPath, cfgBytes, 0o600))

	// Grab a free port, then release it for the server to bind.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	logs := &syncBuffer{}
	cmd := exec.Command(binPath)
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(),
		"CONFIG_PATH="+relToRoot(cfgPath),
		"PORT="+strconv.Itoa(port),
	)
	cmd.Stdout = logs
	cmd.Stderr = logs
	require.NoError(t, cmd.Start())

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }() // never leak the process on a failing path

	// Readiness: poll /healthz over real HTTP (this also exercises the
	// endpoint outside httptest). Building the indexes can take a moment.
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	ready := false
	for !ready && time.Now().Before(deadline) {
		resp, getErr := client.Get(healthURL)
		if getErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			ready = resp.StatusCode == http.StatusOK
		}
		if ready {
			break
		}
		select {
		case waitErr := <-done:
			t.Fatalf("server exited before becoming healthy: %v\nlogs:\n%s", waitErr, logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	require.True(t, ready, "server did not become healthy in time; logs:\n%s", logs.String())

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	select {
	case waitErr := <-done:
		require.NoError(t, waitErr, "server must exit with code 0 after SIGTERM; logs:\n%s", logs.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("server did not exit within 10s of SIGTERM; logs:\n%s", logs.String())
	}
	assert.Contains(t, logs.String(), "shutting down")
}
