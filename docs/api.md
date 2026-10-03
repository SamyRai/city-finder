# HTTP API

The server (`cmd/server`) exposes a read-only JSON API. The machine-readable
contract is [openapi.yaml](openapi.yaml); this page is the prose guide. All
endpoints are `GET` unless noted. Invalid parameters return `400` with an
error message.

## Endpoints

### `GET /healthz`

Returns `{"status":"ok"}`. Cheap: never touches the indexes, so it answers
even while data is degraded. Use it for liveness and readiness probes.

### `GET /metrics`

Prometheus text format:

- `http_requests_total{path,status}` and the
  `http_request_duration_seconds` histogram per route pattern (scrapes of
  `/metrics` itself are not counted). Requests no route matched are labelled
  `(unrouted)`; requests the HTTP server rejects before routing (413 body
  too large, 431 headers too large, 408 read timeout, malformed requests)
  are labelled `(rejected)` and also get an access-log line with method and
  path `-`;
- `fuzzy_budget_trips_total`: fuzzy searches that returned budget-truncated,
  partial results;
- `fuzzy_build_state`: 0 not built, 1 building, 2 built, 3 disabled;
- `go_goroutines`, `go_heap_alloc_bytes`: runtime gauges sampled per scrape
  (via `runtime/metrics`, no stop-the-world).

### `GET /nearest?lat=<lat>&lon=<lon>[&rank=distance|population][&include=admin]`

The nearest city to a coordinate.

- `lat` ∈ [-90, 90] and `lon` ∈ [-180, 180]. Both must be finite.
- `rank` (default `distance`):
  - `distance`: the geographically closest city.
  - `population`: gravity-weighted; a nearby big city beats the closest
    village. The highest `population / (d² + 1)` wins (d = great-circle km),
    exactly over the whole dataset. Responses carry the winner's
    `Population`. Land queries resolve in µs–ms. Far-from-land (ocean) points
    take seconds at full scale (see [performance.md](performance.md)).
    Concurrent population queries are gated at `min(GOMAXPROCS, 8)`, and a
    saturated gate returns `503` with `Retry-After: 1`.
- `include=admin` (any other value → 400) adds the winning city's
  administrative region: `admin1_code`, `admin1_name` (only when the optional
  admin1-names dataset is loaded) and `admin2_code` (when present).
  Attribution follows the winning city, **not** polygon containment, so near a
  boundary it can report the region across the line.

Response: the city plus `distance_km` (great-circle, 2 decimals).

### `POST /nearest/batch`

Body: `{"points":[{"lat":..,"lon":..,"rank":"distance|population","include":"admin"}, ...]}`
with 1–100 points. Per-point fields have the same semantics as `GET /nearest`.

- Requires `Content-Type: application/json`. Unknown JSON fields and invalid
  points are rejected with the offending index.
- Returns `{"results":[...]}`, parallel to the input: each element is what
  `GET /nearest` returns, or `null` for a point with no indexed city.
- The population gate applies per point. If it saturates mid-batch, the whole
  request fails with `503` + `Retry-After`.

### `GET /coordinates?name=<name>&country-code=<cc>`

A city by name within a country. Both parameters are required. The name is
matched exactly first, then fuzzily (edit distance ≤ 2), so `Pars` resolves to
Paris. Surrounding whitespace is trimmed, and names over 200 runes return 400
(this bounds the fuzzy path's cost). Typo matching is unavailable for the
first ~30–90 s after a full-scale boot, while the fuzzy index builds in the
background (`fuzzy_build_state`).

### `GET /autocomplete?name=<prefix>&country-code=<cc>[&limit=1-50]`

Indexed names starting with the prefix, sorted, each with its first city.
Default limit 10. An empty `matches` array means no hit or an unknown country.

### `GET /postalCode?code=<code>&country-code=<cc>`

A city by postal code. Both parameters are required. Inner spaces are
significant; only surrounding whitespace is trimmed. Query the form the
GeoNames dump holds: for GB it keys rows on the outward code (`SW1A`), not the
full `SW1A 1AA`.

## Server behaviour

| Aspect | Value |
|---|---|
| Listen port | `PORT` env (default `3000`; validated at startup, 1-65535) |
| Timeouts | read 15 s, write 15 s, idle 60 s |
| Body limit | 1 MB (a 100-point batch is ~6 KB) |
| Connection cap | 1024 concurrent connections |
| ETag | enabled |
| Panics | recovered per request (500), never fatal |
| Access log | one line per request: method, path (no query string), status, latency, size — bodies and parameters are never logged |
| Shutdown | SIGINT/SIGTERM drain in-flight requests for up to 10 s; a second signal during the drain exits immediately |
| Profiling | opt-in `PPROF_ADDR` listener, separate from the API port (see [configuration.md](configuration.md)) |
