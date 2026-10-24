package coordinates

import (
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SamyRai/cityFinder/lib/city"
)

// badCoordinateRows are points no real place can have. The loader rejects
// them since CITY-21, but an index file written before that fix, or a
// caller of BuildIndex that skipped the loader, can still carry them.
var badCoordinateRows = map[string][2]float64{
	"NaN latitude":  {math.NaN(), 10},
	"NaN longitude": {10, math.NaN()},
	"Inf latitude":  {math.Inf(1), 10},
	"latitude > 90": {90.5, 10},
	"lon < -180":    {10, -180.5},
}

// TestDeserializeIndex_RejectsInvalidCoordinateRow: the S2 format stayed v3
// across the loader fix, so a file holding a NaN or out-of-range row must be
// reported corrupt (the initializer then rebuilds it) instead of serving the
// bad point forever.
func TestDeserializeIndex_RejectsInvalidCoordinateRow(t *testing.T) {
	silenceIndexLogs(t)
	for name, p := range badCoordinateRows {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.gob")
			cities := []city.City{
				{Name: "Fine", Country: "DE", Latitude: 52.5, Longitude: 13.4},
				{Name: "Broken", Country: "DE", Latitude: p[0], Longitude: p[1]},
			}
			writeCities(t, path, len(cities), cities)
			_, err := DeserializeIndex(path)
			if !errors.Is(err, ErrCorruptIndex) {
				t.Fatalf("err = %v, want ErrCorruptIndex", err)
			}
		})
	}
}

// TestBuildIndex_RejectsInvalidCoordinateRow: BuildIndex errors on a bad row
// and names it, rather than indexing a NaN point.
func TestBuildIndex_RejectsInvalidCoordinateRow(t *testing.T) {
	for name, p := range badCoordinateRows {
		t.Run(name, func(t *testing.T) {
			cities := []city.SpatialCity{
				{City: city.City{Name: "Fine", Country: "DE", Latitude: 52.5, Longitude: 13.4}},
				{City: city.City{Name: "Broken", Country: "DE", Latitude: p[0], Longitude: p[1]}},
			}
			f, err := BuildIndex(cities)
			if err == nil {
				t.Fatalf("BuildIndex accepted %v, got finder %v", p, f)
			}
			if !strings.Contains(err.Error(), "row 1") || !strings.Contains(err.Error(), "Broken") {
				t.Errorf("err = %q, want it to name row 1 (Broken)", err)
			}
		})
	}
}
