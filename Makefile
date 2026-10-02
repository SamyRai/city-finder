# Developer entry points. Benchmark targets wrap benchmarks/bench.sh, which
# owns the measurement protocol (see docs/benchmarking.md).

PKG   ?= ./lib/finder/coordinates
BENCH ?= .
BASE  ?= origin/main
COUNT ?= 10
TIME  ?= 1s

URL      ?= http://127.0.0.1:3000
RATES    ?= 250,500,1000,2000
WINDOW   ?= 30s
WORKLOAD ?= nearest

.PHONY: test test-race build build-pgo build-test build-prod rebuild-test-indexes \
	bench bench-ab bench-smoke bench-env loadtest

test:
	go test ./...

# Correctness under the race detector. Never benchmark a -race binary: the
# instrumentation costs 2-20x CPU and 5-10x memory.
test-race:
	go test -race ./...

build:
	go build -o nearestcityserver ./cmd/server

# Profile-guided build. cmd/server/default.pgo must be a CPU profile of a
# REPRESENTATIVE production workload — e.g. 60 s from a live server started
# with PPROF_ADDR=127.0.0.1:6060:
#   curl -o cmd/server/default.pgo 'http://127.0.0.1:6060/debug/pprof/profile?seconds=60'
# Never use a microbenchmark profile: PGO would optimize for that one loop.
build-pgo:
	@test -f cmd/server/default.pgo || { echo "cmd/server/default.pgo not found (see Makefile comment)"; exit 1; }
	go build -pgo=auto -o nearestcityserver ./cmd/server

# Benchmarks: capture COUNT samples for PKG (filtered by BENCH) on this tree.
#   make bench PKG=./lib/finder/name BENCH='CityByName$$'
bench:
	benchmarks/bench.sh run -n $(COUNT) -t $(TIME) -r '$(BENCH)' $(PKG)

# Interleaved A/B of BASE against the working tree for one package.
#   make bench-ab BASE=main PKG=./lib/finder/name BENCH='CityByNameFuzzy'
bench-ab:
	benchmarks/bench.sh ab -b $(BASE) -n $(COUNT) -t $(TIME) -r '$(BENCH)' $(PKG)

# Every benchmark once: proves they build and pass their own assertions.
bench-smoke:
	benchmarks/bench.sh smoke

bench-env:
	benchmarks/bench.sh env

# Open-model load sweep against a RUNNING server (run the client on another
# machine for capacity numbers). See docs/benchmarking.md "Load testing".
#   make loadtest URL=http://host:3000 RATES=500,1000,2000,4000 WORKLOAD=mixed
loadtest:
	go run ./cmd/loadgen -url $(URL) -rates $(RATES) -window $(WINDOW) -workload $(WORKLOAD)

# Build indexes with test data (small dataset)
build-test:
	@echo "Building indexes with test data..."
	go run ./cmd/build-index test
	@echo "Test indexes built successfully!"
	@ls -lh testdata/*.gob

# Build indexes with production data (full dataset)
build-prod:
	@echo "Building indexes with production data (this may take several minutes)..."
	go run ./cmd/build-index prod
	@echo "Production indexes built successfully!"
	@ls -lh datasets/*.gob

# Legacy alias
rebuild-test-indexes: build-test
