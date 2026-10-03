package coordinates

import (
	"fmt"
	"math"

	"github.com/golang/geo/s1"
	"github.com/golang/geo/s2"
)

// populationRankInitialRadiusKm is the radius of the first population-ranked
// query disc. Calibrated so a query in or near any populated place usually
// terminates on the first or second iteration (measured: metro queries
// resolve within 10 km; see the PR summary for the per-class table). It is a
// starting point for the escalation loop, not a cap on the answer.
const populationRankInitialRadiusKm = 10.0

// populationRankRadiusGrowth multiplies the search radius each escalation
// step of the population ranking. Five grows 10 km to global coverage
// (half circumference) in six steps, so even a mid-ocean query pays at most
// six radius-pruned scans before the final unbounded iteration.
const populationRankRadiusGrowth = 5.0

// populationRankAnchoredAfterKm is the escalation radius after which the
// anchored single-disc strategy (anchoredByPopulation) replaces the wide
// escalation tiers: when certification has not happened by this disc, the
// query is far from any sizable city and the wide tiers (1250 km, 6250 km,
// terminal unbounded) are the expensive ones — the anchored step settles the
// query with one data-derived disc instead. It equals the third rung of the
// ladder (initial x growth^2 = 250 km).
const populationRankAnchoredAfterKm = populationRankInitialRadiusKm * populationRankRadiusGrowth * populationRankRadiusGrowth

// anchoredRadiusEpsilon is the relative inflation applied to the anchored
// disc radius (micrometers at a 1000 km radius). It exists for the same
// reason the escalation discs pass chord.Successor() as their limit: the
// exactness argument needs every challenger strictly inside the disc and the
// anchoring city W itself inside or on its rim, but the radius→chord→radius
// round trip loses up to ~1 ULP, which can leave a rim city outside
// chord.Successor() (measured on the pinned golang/geo). The epsilon dwarfs
// that rounding by seven orders of magnitude while staying far below the
// suite's 1 m distance tolerance.
const anchoredRadiusEpsilon = 1e-9

// maxSearchRadiusKm is the great-circle half-circumference: the largest
// distance any two points on the sphere can be apart.
const maxSearchRadiusKm = math.Pi * earthRadiusKm

// populationOutsideBound returns an upper bound on the gravity score of any
// city NOT returned by the radiusKm disc query of nearestByPopulation:
//
//	UB(R) = max( best exact score among top-K cities at distance >= R,
//	             popK / (R*R + 1) )
//
// Correctness: a city outside the top-K table has population <= popK (the
// K-th largest population; 0 when fewer than K positive populations exist, in
// which case the term vanishes), and it sits at distance > R because the disc
// query — whose limit is chord(R).Successor() — already returned everything
// nearer. So its score <= popK/(R*R+1). The top-K cities outside R are scored
// exactly at their true great-circle distance. The max of both terms bounds
// every excluded city, and the winner can be declared as soon as the best
// in-radius score exceeds it. This is never larger than the single-max bound
// maxPopulation/(R*R+1) it replaces (popK <= maxPopulation and exact scores
// are computed at distances >= R), so escalation never gets worse.
//
// Two deliberate conservatisms: entries are exact-scored using the same
// chord-angle distance the query scores its own results by, and entries that
// compute as inside R still contribute population/(R*R+1) — cheap insurance
// against a rim city that the disc query's successor-epsilon limit classified
// differently. Exactness never depends on this bound: the escalation loop
// keeps its terminal unbounded full-sphere iteration.
func (f *S2Finder) populationOutsideBound(radiusKm float64, targetPoint s2.Point) float64 {
	radiusTerm := radiusKm*radiusKm + 1.0

	// popK is the K-th largest population; with fewer than K entries in the
	// table no city was excluded by the table itself, so the term is 0.
	var popK int32
	if len(f.topPopulations) >= topPopulationK {
		popK = f.topPopulations[topPopulationK-1].population
	}
	bound := float64(popK) / radiusTerm

	// The table is sorted by population descending and every entry can
	// contribute at most population/(R*R+1) (outside entries score lower the
	// farther they are), so the scan stops at the first entry that cannot
	// raise the bound.
	limit := kmToChordAngle(radiusKm)
	for _, entry := range f.topPopulations {
		if float64(entry.population)/radiusTerm <= bound {
			break
		}
		if chord := s2.ChordAngleBetweenPoints(entry.point, targetPoint); chord > limit {
			// Outside the disc (strictly beyond chord(R), which the query's
			// successor-epsilon limit treats as excluded): score it exactly
			// through the same chord->angle->km path the query's own results
			// are scored by — unless its score at the chord lower bound
			// already cannot raise the bound (the common case: far entries).
			if lower := chordLowerBoundKm(chord); float64(entry.population)/(lower*lower+1.0) <= bound {
				continue
			}
			distanceKm := chord.Angle().Radians() * earthRadiusKm
			if score := float64(entry.population) / (distanceKm*distanceKm + 1.0); score > bound {
				bound = score
			}
		} else if inside := float64(entry.population) / radiusTerm; inside > bound {
			// Inside the disc: the disc query scored it exactly. Counting it
			// at population/(R*R+1) anyway only inflates the bound (never
			// unsound) and covers the rim-seam case above.
			bound = inside
		}
	}
	return bound
}

// nearestByPopulation returns the highest gravity-scored query result over
// ALL cities — exact, no truncation — using a radius that escalates until an
// anytime bound proves no city outside it can win:
//
//  1. Query every city within radius R of the target. DistanceLimit makes
//     the traversal visit only nearby index cells (the priority queue pops
//     cells in increasing distance and stops past the limit), so the cost
//     scales with the populated area inside the disc, not the index size.
//     The limit is passed as chord.Successor() because the option's
//     semantics are exclusive ("edges whose distance is equal are not
//     returned"); the successor makes each disc inclusive of its rim.
//  2. Track the best gravity score s* among the returned candidates.
//  3. Bound every excluded city by the top-K population table
//     (populationOutsideBound): cities outside the table have population <=
//     popK so they score at most popK/(R*R+1), and the table's own cities
//     are scored exactly at their true distance. If s* already exceeds that
//     bound, the in-radius winner is the global winner and the search stops.
//     The single-max bound this replaces could only certify when
//     s* > maxPopulation/(R*R+1), which for a mid-ocean query (small s*, a
//     megacity somewhere beyond the horizon) never holds before the final
//     step — the top-K exact term is what cuts those queries short.
//  4. Otherwise — once the 250 km disc (initial x growth^2) has failed to
//     certify — the anchored single-disc strategy (anchoredByPopulation)
//     replaces the wide escalation tiers: it derives one disc radius from the
//     top-K table's own best candidate and the popK ceiling and settles the
//     query exactly with that single disc (see its correctness argument).
//     The cheap tiers and the escalation ladder both stay intact as the
//     fallback: the anchored step declines (empty table, derived radius at
//     the half-circumference, empty disc) and the loop escalates R by
//     populationRankRadiusGrowth as before. The final iteration drops the
//     distance limit entirely (an exclusive limit at exactly the
//     half-circumference could drop an antipodal city, and Successor() at the
//     straight angle degenerates), covering the whole sphere — so the loop
//     always terminates with the exact answer. Exactness never depends on
//     the bound or on the anchored step: the unbounded iteration is the
//     fallback that guarantees the brute-force winner (~seconds at prod
//     scale, measured, pre-anchored-step); in practice the top-K bound or the
//     anchored disc keeps even mid-ocean points off it.
//
// The second return value is the radius of the disc that produced the
// winner — the certified escalation radius, the anchored disc radius, or
// maxSearchRadiusKm when the answer came from the terminal unbounded
// iteration (including the degenerate all-zero-population path below). It is
// an unexported test observation hook for escalation depth; nearest discards
// it.
//
// With no population data anywhere (maxPopulation == 0), every score is 0
// and the gravity winner is simply the nearest city.
func (f *S2Finder) nearestByPopulation(targetPoint s2.Point) (s2.EdgeQueryResult, float64, error) {
	// Every disc of this request runs on one pooled query: the options are
	// shared with the query by pointer, so setting the limit reconfigures it,
	// and each FindEdges returns a freshly allocated results slice, so earlier
	// discs' results stay valid after later calls and after the Put.
	pq := f.populationQuery()
	defer f.populationQueryPool.Put(pq)
	target := s2.NewMinDistanceToPointTarget(targetPoint)
	queryAll := func(radiusKm float64, limited bool) []s2.EdgeQueryResult {
		limit := s1.InfChordAngle() // the options' default: no limit
		if limited {
			limit = kmToChordAngle(radiusKm).Successor()
		}
		pq.options.DistanceLimit(limit)
		return pq.query.FindEdges(target)
	}

	var none s2.EdgeQueryResult
	if f.maxPopulation == 0 {
		// Degenerate case: with no population data anywhere the anytime
		// bound can never certify a winner, so every rank=population query
		// pays the unbounded full-scan iteration (the loop's documented
		// worst case) — the nearest city is still returned correctly.
		results := queryAll(0, false)
		if len(results) == 0 {
			return none, maxSearchRadiusKm, fmt.Errorf("no city found")
		}
		return results[0], maxSearchRadiusKm, nil
	}

	radiusKm := populationRankInitialRadiusKm
	triedAnchored := false
	for {
		results := queryAll(radiusKm, true)
		if len(results) > 0 {
			best := f.bestPopulationRank(results)
			// Anytime bound (populationOutsideBound): every city beyond
			// radiusKm scores at most the returned bound; a strictly better
			// in-radius winner cannot be beaten (or tied) from outside.
			if f.populationRankScore(best) > f.populationOutsideBound(radiusKm, targetPoint) {
				return best, radiusKm, nil
			}
		}
		if radiusKm >= populationRankAnchoredAfterKm && !triedAnchored {
			// The cheap tiers are spent: try the anchored single disc before
			// any wide escalation. On decline, escalate (once — the anchored
			// decision is radius-independent and deterministic).
			triedAnchored = true
			if best, anchoredKm, ok := f.anchoredByPopulation(targetPoint, queryAll); ok {
				return best, anchoredKm, nil
			}
		}
		next := radiusKm * populationRankRadiusGrowth
		if next < maxSearchRadiusKm {
			radiusKm = next
			continue
		}
		// Final iteration: no distance limit — the whole sphere, exact.
		results = queryAll(0, false)
		if len(results) == 0 {
			return none, maxSearchRadiusKm, fmt.Errorf("no city found")
		}
		return f.bestPopulationRank(results), maxSearchRadiusKm, nil
	}
}

// anchoredByPopulation is the strategy that replaces the wide escalation
// tiers (1250 km, 6250 km, terminal unbounded) once the cheap discs (10, 50,
// 250 km) have failed to certify a winner — the mid-ocean query class, where
// those wide tiers return millions of edges and the terminal full-sphere scan
// costs seconds at prod scale. It settles the query with ONE disc whose radius
// is derived from the top-K population table (already held in memory) instead
// of the ladder:
//
//  1. W = the best gravity-scored city in the ENTIRE topPopulations table,
//     scored exactly at its true distance (a ~4096-entry table scan plus chord
//     distances: microseconds). s_W = score(W) > 0, d_W = distance(W).
//  2. popK = the K-th largest population (the same value populationOutsideBound
//     uses; 0 when the table holds fewer than topPopulationK entries, in which
//     case every non-table city scores <= 0 < s_W and no challenger radius
//     exists). Otherwise the challenger radius R* = sqrt(popK/s_W - 1) is the
//     distance at which a hypothetical maximum-population challenger (popK,
//     not in the table) would exactly tie s_W.
//  3. Run one disc at radius max(R*, d_W, 250 km), inflated by
//     anchoredRadiusEpsilon and passed through queryAll's existing
//     chord.Successor() inclusive-rim semantics, and return the winner by the
//     ordinary bestPopulationRank over its results.
//
// CORRECTNESS ARGUMENT (why the single disc is exact). Every city on the
// sphere is in one of three classes:
//
//   - Inside the disc: scored exactly by the query; the comparator sees it.
//
//   - A table city outside the disc: it sits beyond D >= d_W, so its score is
//     <= s_W (W is the table argmax). If strictly less, it loses outright; if
//     it bitwise ties s_W, the comparator resolves score ties toward the
//     smaller distance, and W (at d_W < D) is inside the disc and wins the
//     tie. Either way it cannot change the winner.
//
//   - A non-table city outside the disc: its population <= popK and its
//     distance > R* (strictly — the epsilon inflation plus the successor rim
//     guarantee everything at or under the limit is IN the disc), so its score
//     < popK/(R*^2+1) = s_W: it loses to W outright, with no tie possible.
//     When popK == 0 (short table) this class scores <= 0 < s_W. When
//     s_W >= popK, R* degenerates to 0: no distance lets a popK challenger
//     even tie s_W, so the disc shrinks to max(d_W, 250 km) and the argument
//     still holds through the two classes above.
//
// The answer is therefore the exact brute-force winner — the same guarantee
// the terminal unbounded iteration provides, at the cost of one disc sized by
// megacity math (W is the eventual ocean winner in practice, so the disc
// reaches W and little else; thousands of edges over open ocean instead of
// millions).
//
// Why the disc radius includes d_W (rather than only max(R*, 250 km) plus a
// merge of table candidates): s2.EdgeQueryResult's fields are unexported, so
// a table-city winner cannot be reported through the EdgeQueryResult contract
// nearestByPopulation returns. Pulling W inside the disc instead lets the
// query itself produce W's genuine result — identical ordering semantics
// (bestPopulationRank, unchanged) with no second comparator to keep in sync.
//
// The boolean reports whether the strategy applied. It declines (and the
// caller keeps escalating, the terminal unbounded iteration remaining the
// last-resort exactness guarantee) when the table is empty, when the derived
// radius reaches the half-circumference (chord.Successor() degenerates at the
// straight angle, so a near-global disc must become the terminal scan), or
// when the disc unexpectedly returns nothing (W at d_W <= D is inside it, so
// this cannot happen short of index corruption).
func (f *S2Finder) anchoredByPopulation(targetPoint s2.Point, queryAll func(radiusKm float64, limited bool) []s2.EdgeQueryResult) (s2.EdgeQueryResult, float64, bool) {
	var none s2.EdgeQueryResult
	if len(f.topPopulations) == 0 {
		return none, 0, false
	}

	sW, dW := f.tableAnchor(targetPoint)
	if sW <= 0 {
		return none, 0, false // unreachable: table entries carry population > 0
	}

	// popK: the K-th largest population, identical to populationOutsideBound's
	// derivation (0 when the table is short, so the challenger term vanishes).
	var popK int32
	if len(f.topPopulations) >= topPopulationK {
		popK = f.topPopulations[topPopulationK-1].population
	}
	challengerKm := 0.0
	if float64(popK) > sW {
		challengerKm = math.Sqrt(float64(popK)/sW - 1.0)
	}

	radiusKm := math.Max(challengerKm, dW)
	radiusKm = math.Max(radiusKm, populationRankAnchoredAfterKm)
	radiusKm *= 1 + anchoredRadiusEpsilon
	if radiusKm >= maxSearchRadiusKm {
		return none, 0, false
	}

	results := queryAll(radiusKm, true)
	if len(results) == 0 {
		return none, 0, false
	}
	return f.bestPopulationRank(results), radiusKm, true
}

// tableAnchor returns W of anchoredByPopulation: the best gravity score over
// the top-K table (sW) and that city's distance (dW), exactly scored at true
// chord distances through the same chord->angle->km path the query's own
// results are scored by. The table is population-descending, so once an
// entry's raw population cannot beat the best score found, no later entry
// can either (score <= population) and the scan stops. Entries that cannot
// beat sW even at their chord lower bound skip the asin (chordLowerBoundKm).
func (f *S2Finder) tableAnchor(targetPoint s2.Point) (sW, dW float64) {
	for _, entry := range f.topPopulations {
		if float64(entry.population) <= sW {
			break
		}
		chord := s2.ChordAngleBetweenPoints(entry.point, targetPoint)
		if lower := chordLowerBoundKm(chord); float64(entry.population)/(lower*lower+1.0) <= sW {
			continue
		}
		distanceKm := chord.Angle().Radians() * earthRadiusKm
		if score := float64(entry.population) / (distanceKm*distanceKm + 1.0); score > sW {
			sW, dW = score, distanceKm
		}
	}
	return sW, dW
}

// bestPopulationRank returns the result with the highest gravity-model score
// population / (d*d + 1). See NearestPlace for the formula and tie-breaks;
// the strictly-greater comparison resolves equal scores to the earlier
// candidate, which — because results arrive sorted by distance — is the
// nearer one, and at equal distance the lower candidate index.
func (f *S2Finder) bestPopulationRank(results []s2.EdgeQueryResult) s2.EdgeQueryResult {
	best := results[0]
	bestScore := f.populationRankScore(best)
	for _, candidate := range results[1:] {
		if score := f.populationRankScore(candidate); score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best
}

// populationRankScore scores one query result under the gravity model. An
// out-of-range edge index cannot occur with a consistently built finder (the
// point vector and the city slice come from the same source); scoring it as
// -Inf keeps a corrupt index from panicking before NearestPlace's own bounds
// check can report it.
func (f *S2Finder) populationRankScore(result s2.EdgeQueryResult) float64 {
	cityIndex := int(result.EdgeID())
	if cityIndex < 0 || cityIndex >= len(f.Cities) {
		return math.Inf(-1)
	}
	distanceKm := result.Distance().Angle().Radians() * earthRadiusKm
	return float64(f.Cities[cityIndex].Population) / (distanceKm*distanceKm + 1.0)
}
