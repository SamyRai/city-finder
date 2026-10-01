package coordinates

import (
	"github.com/SamyRai/cityFinder/lib/city"
)

// Finder interface that each finder should implement
type Finder interface {
	NearestPlace(lat, lon float64, rank Rank) (*city.City, float64, error)
}
