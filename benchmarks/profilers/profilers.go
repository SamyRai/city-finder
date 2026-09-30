package profilers

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/SamyRai/cityFinder/benchmarks/types"
)

// Profiler manages profiling operations
type Profiler struct {
	config       types.BenchmarkConfig
	cpuFile      *os.File
	memFile      *os.File
	traceFile    *os.File
	startTime    time.Time
	initialStats runtime.MemStats
}

// NewProfiler creates a new profiler instance
func NewProfiler(config types.BenchmarkConfig) (*Profiler, error) {
	p := &Profiler{
		config: config,
	}

	// Start CPU profiling if enabled
	if config.EnableCPUProf {
		filename := fmt.Sprintf("cpu_%s_%d.prof", config.Name, time.Now().Unix())
		f, err := os.Create(filename)
		if err != nil {
			return nil, fmt.Errorf("failed to create CPU profile file: %w", err)
		}
		p.cpuFile = f

		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to start CPU profiling: %w", err)
		}
	}

	// Memory profiling will be done on demand
	if config.EnableMemProf {
		filename := fmt.Sprintf("mem_%s_%d.prof", config.Name, time.Now().Unix())
		f, err := os.Create(filename)
		if err != nil {
			p.Stop() // Clean up CPU profiling if it was started
			return nil, fmt.Errorf("failed to create memory profile file: %w", err)
		}
		p.memFile = f
	}

	// Take initial memory snapshot
	runtime.GC()
	time.Sleep(10 * time.Millisecond) // Let GC settle
	runtime.ReadMemStats(&p.initialStats)

	p.startTime = time.Now()

	return p, nil
}

// Stop stops profiling and cleans up resources
func (p *Profiler) Stop() error {
	var errs []error

	// Stop CPU profiling
	if p.cpuFile != nil {
		pprof.StopCPUProfile()
		if err := p.cpuFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close CPU profile file: %w", err))
		}
	}

	// Memory profiling is done on demand, so just close the file if it exists
	if p.memFile != nil {
		if err := p.memFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close memory profile file: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("profiler cleanup errors: %v", errs)
	}

	return nil
}

// CaptureMemoryProfile captures a memory profile snapshot
func (p *Profiler) CaptureMemoryProfile() error {
	if p.memFile == nil {
		return nil // Memory profiling not enabled
	}

	// Write memory profile
	if err := pprof.WriteHeapProfile(p.memFile); err != nil {
		return fmt.Errorf("failed to write memory profile: %w", err)
	}

	return nil
}

// GetCurrentMemoryStats returns current memory statistics
func (p *Profiler) GetCurrentMemoryStats() types.MemoryStats {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)

	// Calculate GC pauses (simplified - just the last pause)
	var gcPauses []time.Duration
	if stats.NumGC > 0 {
		gcPauses = append(gcPauses, time.Duration(stats.PauseTotalNs))
	}

	return types.MemoryStats{
		Initial:     p.initialStats.Alloc,
		Peak:        stats.Sys, // Use Sys as approximation for peak memory usage
		Final:       stats.Alloc,
		Allocations: stats.Mallocs - p.initialStats.Mallocs,
		GCCycles:    stats.NumGC - p.initialStats.NumGC,
		GCPauses:    gcPauses,
	}
}

// GetCPUStats returns CPU usage statistics
func (p *Profiler) GetCPUStats() types.CPUStats {
	elapsed := time.Since(p.startTime)

	// For now, we return basic stats. In a more advanced implementation,
	// we could use system calls to get actual CPU usage per process
	return types.CPUStats{
		UserTime:   elapsed, // Approximation
		SystemTime: 0,       // Not easily available in Go stdlib
		TotalTime:  elapsed,
		Cores:      runtime.NumCPU(),
	}
}

// MeasureOperation measures the execution time and memory impact of an operation
func (p *Profiler) MeasureOperation(name string, operation func() error) (types.OperationResult, error) {
	var startStats runtime.MemStats
	runtime.ReadMemStats(&startStats)
	startTime := time.Now()

	err := operation()

	duration := time.Since(startTime)
	var endStats runtime.MemStats
	runtime.ReadMemStats(&endStats)

	// Calculate memory delta safely to avoid underflow
	var memoryDelta uint64
	if endStats.Alloc >= startStats.Alloc {
		memoryDelta = endStats.Alloc - startStats.Alloc
	} else {
		// Memory decreased (likely due to GC), use 0 as delta for this operation
		memoryDelta = 0
	}

	// Calculate allocations and GC metrics
	allocations := endStats.Mallocs - startStats.Mallocs
	gcCycles := endStats.NumGC - startStats.NumGC
	gcTime := time.Duration(endStats.PauseTotalNs - startStats.PauseTotalNs)

	result := types.OperationResult{
		Name:        name,
		Duration:    duration,
		MemoryDelta: memoryDelta,
		Allocations: allocations,
		GCCycles:    gcCycles,
		GCTime:      gcTime,
		Status:      "completed",
		Metadata:    make(map[string]interface{}),
	}

	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	}

	return result, err
}
