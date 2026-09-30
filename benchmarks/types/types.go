package types

import (
	"time"
)

// BenchmarkConfig holds configuration for benchmark runs
type BenchmarkConfig struct {
	Name           string            `json:"name"`
	DatasetSizes   []int             `json:"dataset_sizes"`
	EnableCPUProf  bool              `json:"enable_cpu_prof"`
	EnableMemProf  bool              `json:"enable_mem_prof"`
	EnableTrace    bool              `json:"enable_trace"`
	OutputFormats  []OutputFormat    `json:"output_formats"`
	Iterations     int               `json:"iterations"`
	Tags           map[string]string `json:"tags"`
	WarmupRuns     int               `json:"warmup_runs"`
	SkipComponents map[string]bool   `json:"skip_components"` // "s2", "name", "postal"
	ConfigPath     string            `json:"config_path"`
}

// OutputFormat represents different output formats
type OutputFormat string

const (
	OutputFormatConsole OutputFormat = "console"
	OutputFormatJSON    OutputFormat = "json"
	OutputFormatCSV     OutputFormat = "csv"
	OutputFormatHTML    OutputFormat = "html"
)

// BenchmarkResult holds comprehensive benchmark results
type BenchmarkResult struct {
	Config      BenchmarkConfig        `json:"config"`
	Timestamp   time.Time              `json:"timestamp"`
	Duration    time.Duration          `json:"duration"`
	MemoryUsage MemoryStats            `json:"memory_usage"`
	CPUUsage    CPUStats               `json:"cpu_usage"`
	Operations  []OperationResult      `json:"operations"`
	SystemInfo  SystemInfo             `json:"system_info"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// OperationResult represents the result of a specific operation
type OperationResult struct {
	Name           string                 `json:"name"`
	Duration       time.Duration          `json:"duration"`
	MemoryDelta    uint64                 `json:"memory_delta"`
	Allocations    uint64                 `json:"allocations"` // Number of allocations during operation
	GCCycles       uint32                 `json:"gc_cycles"`   // GC cycles during operation
	GCTime         time.Duration          `json:"gc_time"`     // Total GC time during operation
	ItemsProcessed int                    `json:"items_processed"`
	Throughput     float64                `json:"throughput"` // items per second
	Status         string                 `json:"status"`
	Error          string                 `json:"error,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// MemoryStats holds memory usage information
type MemoryStats struct {
	Initial     uint64          `json:"initial"`
	Peak        uint64          `json:"peak"`
	Final       uint64          `json:"final"`
	Allocations uint64          `json:"allocations"`
	GCCycles    uint32          `json:"gc_cycles"`
	GCPauses    []time.Duration `json:"gc_pauses"`
}

// CPUStats holds CPU usage information
type CPUStats struct {
	UserTime   time.Duration `json:"user_time"`
	SystemTime time.Duration `json:"system_time"`
	TotalTime  time.Duration `json:"total_time"`
	Cores      int           `json:"cores"`
}

// SystemInfo holds system information
type SystemInfo struct {
	GoVersion    string `json:"go_version"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	CPUCores     int    `json:"cpu_cores"`
	MemoryTotal  uint64 `json:"memory_total"`
	Hostname     string `json:"hostname"`
}

// BenchmarkSuite represents a collection of benchmarks to run
type BenchmarkSuite struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Benchmarks  []BenchmarkConfig      `json:"benchmarks"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// ComparisonResult holds the result of comparing two benchmark runs
type ComparisonResult struct {
	Baseline    BenchmarkResult    `json:"baseline"`
	Current     BenchmarkResult    `json:"current"`
	Comparisons []MetricComparison `json:"comparisons"`
	Summary     ComparisonSummary  `json:"summary"`
}

// MetricComparison compares a specific metric between two runs
type MetricComparison struct {
	Metric        string  `json:"metric"`
	Baseline      float64 `json:"baseline"`
	Current       float64 `json:"current"`
	Change        float64 `json:"change"`
	PercentChange float64 `json:"percent_change"`
	Improvement   bool    `json:"improvement"` // true if current is better than baseline
}

// ComparisonSummary provides a high-level summary of the comparison
type ComparisonSummary struct {
	OverallImprovement bool    `json:"overall_improvement"`
	SignificantChanges int     `json:"significant_changes"`
	PerformanceDelta   float64 `json:"performance_delta"`
	MemoryDelta        float64 `json:"memory_delta"`
}
