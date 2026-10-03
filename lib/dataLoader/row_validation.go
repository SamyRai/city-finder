package dataLoader

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
)

// Reasons a loader rejects a row. They label the per-file skip summary.
const (
	reasonMissingField    = "missing required field"
	reasonShortRow        = "fewer than 12 fields"
	reasonUnparsableCoord = "unparsable coordinates"
	reasonBadCoordinate   = "non-finite or out-of-range coordinates"
	reasonNegativePop     = "negative population"
)

// maxSampleLines caps how many line numbers a skip summary names per reason.
const maxSampleLines = 5

// validCoordinate reports whether lat/lon is a usable point: finite, with
// |lat| <= 90 and |lon| <= 180 — the same bounds the HTTP layer enforces on
// query input. strconv.ParseFloat accepts "NaN", "Inf" and 999, so parsing
// alone does not keep them out of the indexes. NaN fails both comparisons.
func validCoordinate(lat, lon float64) bool {
	return math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}

// parseCoordinate parses the latitude and longitude columns and returns the
// reason the row must be skipped, or "" when the point is usable. Both
// loaders share it so the two datasets reject the same values.
func parseCoordinate(latField, lonField string) (lat, lon float64, reason string) {
	lat, err := strconv.ParseFloat(latField, 64)
	if err != nil {
		return 0, 0, reasonUnparsableCoord
	}
	lon, err = strconv.ParseFloat(lonField, 64)
	if err != nil {
		return 0, 0, reasonUnparsableCoord
	}
	if !validCoordinate(lat, lon) {
		return 0, 0, reasonBadCoordinate
	}
	return lat, lon, ""
}

// skipReport counts rows a loader rejected, per reason, and remembers the
// first few line numbers of each. The loaders hold up to ~14M rows, so a
// rejected row is never logged in full: log emits ONE summary line per file.
// The zero value is ready to use and allocates nothing until a row is added.
type skipReport struct {
	reasons []string // reasons in order of first occurrence
	counts  map[string]int
	samples map[string][]int
	total   int
}

// add records that the row on line (1-based) was rejected for reason.
func (r *skipReport) add(reason string, line int) {
	if r.counts == nil {
		r.counts = make(map[string]int)
		r.samples = make(map[string][]int)
	}
	if r.counts[reason] == 0 {
		r.reasons = append(r.reasons, reason)
	}
	r.counts[reason]++
	r.total++
	if len(r.samples[reason]) < maxSampleLines {
		r.samples[reason] = append(r.samples[reason], line)
	}
}

// log writes the summary for kind ("city", "postal") rows of path; it is
// silent when nothing was skipped.
func (r *skipReport) log(kind, path string) {
	if r.total == 0 {
		return
	}
	parts := make([]string, 0, len(r.reasons))
	for _, reason := range r.reasons {
		parts = append(parts, fmt.Sprintf("%d %s (%s)", r.counts[reason], reason,
			sampleLines(r.samples[reason], r.counts[reason])))
	}
	log.Printf("skipped %d malformed %s rows in %s: %s", r.total, kind, path, strings.Join(parts, ", "))
}

// sampleLines renders "line 7", "lines 1, 4" or, when more rows were skipped
// than sampled, "lines 1, 2, 3, 4, 5, ...".
func sampleLines(lines []int, total int) string {
	nums := make([]string, len(lines))
	for i, l := range lines {
		nums[i] = strconv.Itoa(l)
	}
	switch {
	case total > len(lines):
		return "lines " + strings.Join(nums, ", ") + ", ..."
	case len(lines) == 1:
		return "line " + nums[0]
	default:
		return "lines " + strings.Join(nums, ", ")
	}
}
