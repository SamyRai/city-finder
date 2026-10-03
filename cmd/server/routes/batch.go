package routes

import (
	"math"
	"runtime"
	"sync/atomic"

	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
)

// maxBatchPoints caps a POST /nearest/batch request. The batch exists so a
// client pays one round trip instead of N; past ~100 points a batch of
// population-gated lookups (bounded by the gate) would hold a connection far
// longer than the equivalent parallel GETs, so larger batches are a 400, not
// a slowdown.
const maxBatchPoints = 100

// maxBatchWorkers bounds the POST /nearest/batch fan-out. CPU-bound lookups
// gain nothing beyond core count, and the population gate independently caps
// the expensive class, so one worker per P (capped by the batch size) is the
// honest ceiling.
var maxBatchWorkers = runtime.GOMAXPROCS(0)

// batchWorkerCount returns the fan-out for a batch of n points. A batch that
// contains population-ranked points never runs more workers than the
// population gate has slots: the gate fails fast, so a fan-out wider than the
// gate (GOMAXPROCS > 8) would let a single batch on an idle server saturate
// it and 503 itself. Distance-only batches keep the full fan-out.
func batchWorkerCount(n int, hasPopulation bool) int {
	workers := min(n, maxBatchWorkers)
	if hasPopulation {
		workers = min(workers, cap(populationGate))
	}
	return max(workers, 1)
}

// batchRequest is the POST /nearest/batch body: one point per lookup.
type batchRequest struct {
	Points []batchPoint `json:"points"`
}

// batchPoint is one entry of a POST /nearest/batch request, carrying the GET
// endpoint's parameters. lat/lon are pointers so an absent field is
// distinguishable from a legitimate 0 coordinate; rank/include are pointers
// so absence (the per-point default) differs from any explicit value.
type batchPoint struct {
	Lat     *float64 `json:"lat"`
	Lon     *float64 `json:"lon"`
	Rank    *string  `json:"rank"`
	Include *string  `json:"include"`
}

// batchResponse is the POST /nearest/batch reply: a results array parallel
// to the request's points. A not-found point is a nil entry, which marshals
// as JSON null — the batch form of the GET handler's 404.
type batchResponse struct {
	Results []*nearestCityResponse `json:"results"`
}

// validatedPoint is one batch point after validation, in the shape
// executeNearest consumes.
type validatedPoint struct {
	lat          float64
	lon          float64
	rank         coordinates.Rank
	includeAdmin bool
}

// orEmpty treats an absent JSON string field exactly like the GET handler
// treats an absent query parameter: the empty value that selects the default.
func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// validateBatchPoint applies the /nearest parameter rules to one batch point
// in the GET handler's validation order — lat, lon, lat range, lon range,
// rank, include — and returns the parsed point plus the first failure's
// plain-text reason ("" when the point is valid), so the caller can report
// "points[i]: <reason>" with the GET error texts.
func validateBatchPoint(p batchPoint) (validatedPoint, string) {
	var v validatedPoint
	if p.Lat == nil || math.IsNaN(*p.Lat) || math.IsInf(*p.Lat, 0) {
		return v, "Invalid latitude"
	}
	if p.Lon == nil || math.IsNaN(*p.Lon) || math.IsInf(*p.Lon, 0) {
		return v, "Invalid longitude"
	}
	v.lat, v.lon = *p.Lat, *p.Lon
	if v.lat < -90 || v.lat > 90 {
		return v, "Latitude must be between -90 and 90"
	}
	if v.lon < -180 || v.lon > 180 {
		return v, "Longitude must be between -180 and 180"
	}
	rank, ok := parseRank(orEmpty(p.Rank))
	if !ok {
		return v, "Invalid rank"
	}
	v.rank = rank
	v.includeAdmin, ok = parseInclude(orEmpty(p.Include))
	if !ok {
		return v, "Invalid include"
	}
	return v, ""
}

// lowerFirstFailure lowers *first to i unless it already holds a lower
// index.
func lowerFirstFailure(first *atomic.Int64, i int64) {
	for {
		cur := first.Load()
		if i >= cur || first.CompareAndSwap(cur, i) {
			return
		}
	}
}
