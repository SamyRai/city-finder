package main

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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

func (suite *ServerTestSuite) TestNearestKnownGood() {
	fixture := suite.fixtureCity("Xixerella", "AD")

	resp, body := suite.doGet("/nearest?" + query(map[string]string{
		"lat": strconv.FormatFloat(fixture.Latitude, 'f', -1, 64),
		"lon": strconv.FormatFloat(fixture.Longitude, 'f', -1, 64),
	}))
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode, body)

	var got city.City
	err := json.Unmarshal([]byte(body), &got)
	assert.NoError(suite.T(), err, body)
	assert.Equal(suite.T(), "AD", got.Country, body)
	assert.NotEmpty(suite.T(), got.Name, body)
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
