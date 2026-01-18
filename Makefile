test:
	go test ./cmd/server

# Build with Profile-Guided Optimization (PGO)
# First collect a profile: go run -cpuprofile=cpu.prof ./cmd/server/main.go
# Then build: make build-pgo
build-pgo:
	@if [ ! -f cmd/server/default.pgo ]; then \
		echo "Error: PGO profile not found. Place cpu.prof as cmd/server/default.pgo"; \
		exit 1; \
	fi
	go build -pgo=auto ./cmd/server

# Build with Green Tea GC (experimental, Go 1.25+)
build-greentea:
	GOEXPERIMENT=greenteagc go build ./cmd/server

# Run benchmarks with standard GC
bench:
	go run benchmarks/run_benchmarks.go comprehensive

# Run benchmarks with Green Tea GC
bench-greentea:
	go run benchmarks/run_benchmarks.go greentea

# Collect PGO profile
profile:
	go run -cpuprofile=cpu.prof ./cmd/server/main.go
	@echo "Profile saved to cpu.prof"
	@echo "To use for PGO, copy to: cp cpu.prof cmd/server/default.pgo"

# Build indexes with test data (small dataset)
build-test:
	@echo "Building indexes with test data..."
	go run cmd/build-index/main.go test
	@echo "Test indexes built successfully!"
	@ls -lh testdata/*.gob

# Build indexes with production data (full dataset)
build-prod:
	@echo "Building indexes with production data (this may take several minutes)..."
	go run cmd/build-index/main.go prod
	@echo "Production indexes built successfully!"
	@ls -lh testdata/*.gob

# Rebuild test indexes from real data (legacy alias)
rebuild-test-indexes: build-test